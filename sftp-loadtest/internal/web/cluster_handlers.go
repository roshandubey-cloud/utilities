package web

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/cluster"
)

// clusterFanOutTimeout caps how long /api/cluster/start and
// /api/cluster/stop wait for the master to fan out to every worker.
// Pre-v0.19.6 this was hard-coded at 30 s. The 8 h cluster validation
// surfaced the limit: with 21+ users × 4 streams per worker, dial
// setup at the worker's own /api/start exceeds 30 s and the master's
// fan-out times out + rolls back. Operators on real clusters with
// many users can override via -cluster-timeout (main.go). Default
// raised to 5 min, which covers the practical worst case (50 users
// × 4 streams ≈ 200 SSH dials at ~1.5 s each = 300 s).
var clusterFanOutTimeout = 5 * time.Minute

// SetClusterFanOutTimeout overrides the default. Called by main.go
// when the operator passes -cluster-timeout. Idempotent and safe to
// call before the server starts; not thread-safe at runtime (no
// expected production caller).
func SetClusterFanOutTimeout(d time.Duration) {
	if d > 0 {
		clusterFanOutTimeout = d
	}
}

// CONCURRENT-RUNS NOTE (v0.17.0): the per-worker /api/start handler no
// longer blocks a second concurrent run on a single worker (gate was
// lifted in v0.16.0 to allow operator-initiated parallel loads). The
// cluster Coordinator continues to call /api/start exactly ONCE per
// clusterStart per worker — the contract here is "one cluster
// orchestrates one run per worker". If a future change sends a
// concurrent start to a worker, that worker will now happily accept it
// and the cluster status panel will show two run rows for that worker.
// Tests cover the single-run-per-worker path; the Coordinator does not
// currently exercise the concurrent-per-worker path.

// clusterCoord is the master-side coordinator instance. Lazily
// constructed via sync.Once because the cluster surface is a no-op for
// instances that never act as a master (i.e. workers never reach these
// handlers, the operator just doesn't post to them).
//
// We hold it at package level rather than on Server because the
// coordinator's lifetime spans the whole process, not a single request,
// and the Server's identity isn't carried into the handlers' shared
// state. When constructed, it captures the Server's platform version
// for worker-skew negotiation; subsequent calls return the same coord.
var (
	clusterCoordOnce sync.Once
	clusterCoord     *cluster.Coordinator
)

func (s *Server) getClusterCoord() *cluster.Coordinator {
	clusterCoordOnce.Do(func() {
		clusterCoord = cluster.New(s.getVersion())
		// Wire the master's reports dir into the coordinator so per-worker
		// reports get pulled into <reportsDir>/cluster-runs/<id>/ on Stop.
		// Without this the cluster status panel was the only place
		// per-worker numbers ever surfaced — the moment Stop fired they
		// were gone unless the operator SSH'd into each worker.
		if s.reportsDir != "" {
			clusterCoord.SetArchiveDir(s.reportsDir)
		}
	})
	return clusterCoord
}

// /api/cluster/start — fan out a unified config to N workers.
//
// Body: {"workers":[{"url":"http://host:port","auth_user":"...","auth_pass":"..."}],
//        "config":{...same shape /api/start expects...}}
//
// Response: {"run_ids":["run-abc","run-xyz"]}.
func (s *Server) handleClusterStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req cluster.StartReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), clusterFanOutTimeout)
	defer cancel()
	ids, err := s.getClusterCoord().Start(ctx, req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"run_ids": ids})
}

// /api/cluster/status — aggregated view across all workers.
func (s *Server) handleClusterStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	writeJSON(w, s.getClusterCoord().Status(ctx))
}

// /api/cluster/stop — fan out /api/stop to every worker, then archive
// each worker's report into <reportsDir>/cluster-runs/<id>/ so the
// master UI's Runs panel can show the cluster run alongside solo runs.
func (s *Server) handleClusterStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), clusterFanOutTimeout)
	defer cancel()
	coord := s.getClusterCoord()
	if err := coord.Stop(ctx); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	// Archival is best-effort and runs even when Stop returned early —
	// a single unreachable worker shouldn't block the others' reports
	// from being persisted. Errors here are logged but don't propagate
	// to the caller; the cluster Stop already succeeded.
	if dir, aerr := coord.ArchiveOnStop(ctx); aerr != nil {
		writeJSON(w, map[string]any{"ok": true, "archive_warning": aerr.Error()})
		return
	} else if dir != "" {
		writeJSON(w, map[string]any{"ok": true, "archive_dir": dir})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// /api/cluster/runs — list every archived cluster run, newest first.
// /api/cluster/runs?id=<id> — full ClusterRunMeta for one run.
func (s *Server) handleClusterRuns(w http.ResponseWriter, r *http.Request) {
	if s.reportsDir == "" {
		writeJSON(w, map[string]any{"runs": []any{}})
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		runs, err := cluster.ListClusterRuns(s.reportsDir)
		if err != nil {
			http.Error(w, "list cluster runs: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"runs": runs})
		return
	}
	if !validClusterRunID(id) {
		http.Error(w, "bad cluster run id", http.StatusBadRequest)
		return
	}
	meta, err := cluster.LoadClusterRun(s.reportsDir, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, meta)
}

// /api/cluster/runs/file?id=<id>&name=<file> — download one of the
// per-worker artifacts archived under cluster-runs/<id>/ (worker-NN.csv
// or worker-NN.json). Strict path-component validation: id and name
// must not contain "/", "\\", "..", or any non-alphanumeric / dash /
// dot characters. Anything else is a 400.
func (s *Server) handleClusterRunFile(w http.ResponseWriter, r *http.Request) {
	if s.reportsDir == "" {
		http.Error(w, "no reports dir configured", http.StatusNotFound)
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if !validClusterRunID(id) || !validClusterFileName(name) {
		http.Error(w, "bad id or filename", http.StatusBadRequest)
		return
	}
	abs := filepath.Join(s.reportsDir, "cluster-runs", id, name)
	// Belt + braces against ../ traversal: the resolved path must still
	// live inside the cluster-runs root. validClusterRunID + validClusterFileName
	// already reject ".." but a defence-in-depth check is cheap.
	root := filepath.Join(s.reportsDir, "cluster-runs")
	rel, err := filepath.Rel(root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		http.Error(w, "path escapes cluster-runs root", http.StatusBadRequest)
		return
	}
	http.ServeFile(w, r, abs)
}

// validClusterRunID accepts cluster-<digits> as written by Coordinator.Start.
func validClusterRunID(id string) bool {
	if !strings.HasPrefix(id, "cluster-") || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !(r == '-' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z')) {
			return false
		}
	}
	return true
}

// validClusterFileName accepts only the names archive.go writes:
// "meta.json", "worker-NN.json", "worker-NN.csv".
func validClusterFileName(name string) bool {
	if name == "meta.json" {
		return true
	}
	if !strings.HasPrefix(name, "worker-") {
		return false
	}
	if !(strings.HasSuffix(name, ".csv") || strings.HasSuffix(name, ".json")) {
		return false
	}
	if strings.Contains(name, "..") || strings.ContainsAny(name, "/\\") {
		return false
	}
	if len(name) > 64 {
		return false
	}
	return true
}
