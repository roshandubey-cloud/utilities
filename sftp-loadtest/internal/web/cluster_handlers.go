package web

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/cluster"
)

// clusterCoord is the master-side coordinator instance held by the
// Server. Lazily constructed because the cluster surface is a no-op for
// instances that never act as a master (i.e. workers never reach these
// handlers, the operator just doesn't post to them).
var (
	clusterCoordOnce sync.Once
	clusterCoord     *cluster.Coordinator
)

func getClusterCoord() *cluster.Coordinator {
	clusterCoordOnce.Do(func() { clusterCoord = cluster.New() })
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
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	ids, err := getClusterCoord().Start(ctx, req)
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
	writeJSON(w, getClusterCoord().Status(ctx))
}

// /api/cluster/stop — fan out /api/stop to every worker.
func (s *Server) handleClusterStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := getClusterCoord().Stop(ctx); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
