// Package cluster is the master-side coordinator for fan-out runs.
//
// A single sftp-loadtest instance acts as the master: it owns the
// operator's UI and a list of worker URLs. When a cluster run starts,
// the master divides the requested files-per-minute across workers and
// posts the per-worker config to each /api/start endpoint. While the run
// is active the master polls each worker's /api/status on a ticker and
// exposes an aggregated view via /api/cluster/status.
//
// Workers are ordinary sftp-loadtest instances — no special build, no
// extra flags. They can be on different hosts, on the same host with
// different ports, or in containers; anything reachable over HTTP is a
// candidate. Each worker keeps its own reports directory and its own
// trust store; the master's job is solely to fan out + sum.
//
// MVP scope:
//   - One cluster run per master at a time
//   - Polling-based aggregation (no streaming protocol)
//   - No worker discovery (operator supplies URLs)
//   - Best-effort: a single failing worker is logged but does not abort
//     the cluster run; the operator decides whether to stop early.
package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// Worker identifies one downstream sftp-loadtest instance.
type Worker struct {
	URL      string `json:"url"`             // e.g. "http://10.0.0.5:8080"
	AuthUser string `json:"auth_user,omitempty"`
	AuthPass string `json:"auth_pass,omitempty"`
}

// StartReq is the master-side cluster-start payload. Config is the
// unified RunConfig the operator filled in via the UI. The master
// derives per-worker variants (fpm divided across N) before posting.
type StartReq struct {
	Workers []Worker        `json:"workers"`
	Config  json.RawMessage `json:"config"`
}

// Coordinator owns the live state of one cluster run. The master web
// server holds at most one Coordinator at a time; concurrent cluster
// runs would conflict on the workers' single-active-run constraint.
type Coordinator struct {
	mu            sync.Mutex
	active        bool
	startedAt     time.Time
	stoppedAt     time.Time
	workers       []runningWorker
	httpc         *http.Client
	masterVersion string // captured once at New() — never mutated
}

// runningWorker tracks a worker's id from /api/start so the master can
// poll the right run on subsequent calls. Even though a worker only
// runs one active run at a time, capturing the id keeps us robust to
// future "list of recent runs" semantics.
type runningWorker struct {
	Worker
	RunID    string `json:"run_id"`
	Version  string `json:"version,omitempty"` // worker's reported platform version, captured once at /api/start
	LastErr  string `json:"last_err,omitempty"`
	LastStat Status `json:"last_stat,omitempty"`
}

// Status is the aggregated view exposed by /api/cluster/status. It is
// the sum-or-merge of the workers' /api/status payloads, with per-
// worker rows attached so operators can see who is healthy.
type Status struct {
	Active            bool             `json:"active"`
	StartedAt         time.Time        `json:"started_at,omitempty"`
	MasterVersion     string           `json:"master_version,omitempty"` // master's own platform version, for skew detection
	TotalFiles        int64            `json:"total_files"`
	TotalBytes        int64            `json:"total_bytes"`
	FailedFiles       int64            `json:"failed_files"`
	DispatchSkips     int64            `json:"dispatch_skips"`
	OverallMBps       float64          `json:"overall_mbps"`
	WindowMBps        float64          `json:"window_mbps"`
	Workers           []WorkerStatus   `json:"workers"`
}

// WorkerStatus is one row of Status.Workers.
type WorkerStatus struct {
	URL             string  `json:"url"`
	RunID           string  `json:"run_id"`
	Version         string  `json:"version,omitempty"`           // worker's platform version (empty if pre-version-negotiation)
	VersionMismatch bool    `json:"version_mismatch,omitempty"`  // true when worker.Version != master.Version (both non-empty)
	Active          bool    `json:"active"`
	TotalFiles      int64   `json:"total_files"`
	FailedFiles     int64   `json:"failed_files"`
	DispatchSkips   int64   `json:"dispatch_skips"`
	OverallMBps     float64 `json:"overall_mbps"`
	WindowMBps      float64 `json:"window_mbps"`
	Reachable       bool    `json:"reachable"`
	Err             string  `json:"err,omitempty"`
}

// New returns a Coordinator with sensible HTTP timeouts. masterVersion
// is the calling process's own platform version; when non-empty, every
// worker's version is compared against it so the UI can flag skew.
// Pass "" if version negotiation is not desired (the cluster will still
// capture worker versions but skip the mismatch flag).
func New(masterVersion string) *Coordinator {
	return &Coordinator{
		httpc:         &http.Client{Timeout: 15 * time.Second},
		masterVersion: masterVersion,
	}
}

// Start fans out the run. The unified config is split across len(workers)
// instances by dividing files_per_minute (the dominant load knob); other
// fields replicate. Returns an error if any worker rejected /api/start.
//
// On partial failure (some workers started, others didn't), Start
// fan-out-stops the successful ones so the cluster never enters a
// half-running state — the operator gets a clear failure they can
// remediate before retrying.
func (c *Coordinator) Start(ctx context.Context, req StartReq) ([]string, error) {
	c.mu.Lock()
	if c.active {
		c.mu.Unlock()
		return nil, errors.New("cluster run already active")
	}
	if len(req.Workers) == 0 {
		c.mu.Unlock()
		return nil, errors.New("at least one worker is required")
	}
	c.mu.Unlock()

	perWorker, err := splitConfig(req.Config, len(req.Workers))
	if err != nil {
		return nil, fmt.Errorf("split config: %w", err)
	}

	rws := make([]runningWorker, len(req.Workers))
	ids := make([]string, len(req.Workers))
	for i, w := range req.Workers {
		runID, serr := c.startOne(ctx, w, perWorker)
		if serr != nil {
			// Roll back already-started workers so the cluster never
			// limps along half-up. Best-effort: stop errors are logged
			// but don't change the returned error.
			for j := 0; j < i; j++ {
				_ = c.stopOne(ctx, rws[j])
			}
			return nil, fmt.Errorf("worker %s: %w", w.URL, serr)
		}
		// Best-effort version negotiation. A worker that doesn't expose
		// /healthz?detail=1 (older release, restricted by auth) leaves
		// Version empty — we still let the run proceed, since version
		// skew is informational, not gating.
		ver, _ := c.negotiateOne(ctx, w)
		rws[i] = runningWorker{Worker: w, RunID: runID, Version: ver}
		ids[i] = runID
	}

	c.mu.Lock()
	c.active = true
	c.startedAt = time.Now()
	c.stoppedAt = time.Time{}
	c.workers = rws
	c.mu.Unlock()
	return ids, nil
}

// Status polls every worker concurrently and merges the results. Workers
// that timed out or returned errors are flagged Reachable=false but the
// merge proceeds with whatever the others reported.
func (c *Coordinator) Status(ctx context.Context) Status {
	c.mu.Lock()
	if !c.active {
		stoppedAt := c.stoppedAt
		started := c.startedAt
		workers := append([]runningWorker(nil), c.workers...)
		c.mu.Unlock()
		// Even after stop, expose the last-seen counters so the UI
		// can show "the run completed at N files".
		return mergeWorkerStatuses(false, started, stoppedAt, c.masterVersion, workers)
	}
	workers := append([]runningWorker(nil), c.workers...)
	started := c.startedAt
	c.mu.Unlock()

	var wg sync.WaitGroup
	out := make([]runningWorker, len(workers))
	for i, w := range workers {
		i, w := i, w
		wg.Add(1)
		go func() {
			defer wg.Done()
			stat, err := c.statusOne(ctx, w)
			out[i] = w
			if err != nil {
				out[i].LastErr = err.Error()
				return
			}
			out[i].LastStat = stat
			out[i].LastErr = ""
		}()
	}
	wg.Wait()

	c.mu.Lock()
	c.workers = out
	c.mu.Unlock()
	return mergeWorkerStatuses(true, started, time.Time{}, c.masterVersion, out)
}

// Stop fans out /api/stop to every worker. Errors are accumulated but
// every worker is contacted regardless so a single dead host never
// strands the others.
func (c *Coordinator) Stop(ctx context.Context) error {
	c.mu.Lock()
	if !c.active {
		c.mu.Unlock()
		return nil
	}
	workers := append([]runningWorker(nil), c.workers...)
	c.mu.Unlock()

	var firstErr error
	for _, w := range workers {
		if err := c.stopOne(ctx, w); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	c.mu.Lock()
	c.active = false
	c.stoppedAt = time.Now()
	c.mu.Unlock()
	return firstErr
}

// Active reports whether a cluster run is currently in flight.
func (c *Coordinator) Active() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active
}

// startOne posts the per-worker config and returns the run id.
func (c *Coordinator) startOne(ctx context.Context, w Worker, body []byte) (string, error) {
	resp, err := c.do(ctx, w, http.MethodPost, "/api/start", body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(b))
	}
	var out struct {
		RunID string `json:"run_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode start response: %w", err)
	}
	if out.RunID == "" {
		return "", errors.New("start response had no run_id")
	}
	return out.RunID, nil
}

// statusOne fetches /api/status and projects it onto the WorkerStatus
// shape. Failures bubble up so Status can flag the worker unreachable.
func (c *Coordinator) statusOne(ctx context.Context, rw runningWorker) (Status, error) {
	resp, err := c.do(ctx, rw.Worker, http.MethodGet, "/api/status", nil)
	if err != nil {
		return Status{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return Status{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var raw struct {
		Active        bool    `json:"active"`
		DispatchSkips int64   `json:"dispatch_skips"`
		FailedFiles   int64   `json:"failed_files"`
		Metrics       struct {
			TotalFiles    int64   `json:"total_files"`
			TotalBytes    int64   `json:"total_bytes"`
			OverallMBps   float64 `json:"overall_mbps"`
			LastWindowMBps float64 `json:"last_minute_mbps"`
		} `json:"metrics"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return Status{}, fmt.Errorf("decode status: %w", err)
	}
	return Status{
		Active:        raw.Active,
		TotalFiles:    raw.Metrics.TotalFiles,
		TotalBytes:    raw.Metrics.TotalBytes,
		OverallMBps:   raw.Metrics.OverallMBps,
		WindowMBps:    raw.Metrics.LastWindowMBps,
		FailedFiles:   raw.FailedFiles,
		DispatchSkips: raw.DispatchSkips,
	}, nil
}

// negotiateOne asks a worker for its platform version via
// /healthz?detail=1. Returns "" with a non-nil error when the worker
// doesn't expose version (older release, network blip, auth refusal) —
// callers treat that as "version unknown" rather than fatal.
func (c *Coordinator) negotiateOne(ctx context.Context, w Worker) (string, error) {
	resp, err := c.do(ctx, w, http.MethodGet, "/healthz?detail=1", nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var raw struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return "", fmt.Errorf("decode healthz: %w", err)
	}
	return raw.Version, nil
}

// stopOne posts /api/stop. The worker's reply is ignored beyond a 2xx
// status check.
func (c *Coordinator) stopOne(ctx context.Context, rw runningWorker) error {
	resp, err := c.do(ctx, rw.Worker, http.MethodPost, "/api/stop", []byte("{}"))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// do issues an authenticated request with the master-side X-Requested-With
// header so the worker's CSRFGuard accepts it.
func (c *Coordinator) do(ctx context.Context, w Worker, method, path string, body []byte) (*http.Response, error) {
	url := w.URL + path
	var rdr io.Reader
	if len(body) > 0 {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Requested-With", "sftp-loadtest")
	req.Header.Set("Accept", "application/json")
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if w.AuthUser != "" {
		req.SetBasicAuth(w.AuthUser, w.AuthPass)
	}
	return c.httpc.Do(req)
}

// splitConfig divides a unified config across N workers. It is
// intentionally narrow: only files_per_minute is split (the dominant
// load knob). Everything else replicates as-is so the workers exercise
// the same scenario, just at 1/N intensity each.
//
// We work on the raw JSON to keep the cluster package independent of
// the config struct's evolving shape — when the operator adds new
// fields they propagate to workers without touching this code.
func splitConfig(raw []byte, n int) ([]byte, error) {
	if n <= 0 {
		return nil, errors.New("worker count must be > 0")
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("config not an object: %w", err)
	}
	if v, ok := m["files_per_minute"]; ok {
		switch t := v.(type) {
		case float64:
			perWorker := int(t) / n
			if perWorker < 1 {
				perWorker = 1
			}
			m["files_per_minute"] = perWorker
		}
	}
	return json.Marshal(m)
}

// mergeWorkerStatuses sums the cluster-wide totals from per-worker
// snapshots and tags each worker with reachability info for the UI.
// throughput is summed (each worker is independently driving load
// against the same target), counts are summed, dispatch skips are summed.
func mergeWorkerStatuses(active bool, started, stopped time.Time, masterVersion string, workers []runningWorker) Status {
	out := Status{Active: active, StartedAt: started, MasterVersion: masterVersion, Workers: make([]WorkerStatus, 0, len(workers))}
	for _, w := range workers {
		ws := WorkerStatus{URL: w.URL, RunID: w.RunID, Version: w.Version, Reachable: w.LastErr == ""}
		if masterVersion != "" && w.Version != "" && w.Version != masterVersion {
			ws.VersionMismatch = true
		}
		if w.LastErr != "" {
			ws.Err = w.LastErr
		}
		ws.Active = w.LastStat.Active
		ws.TotalFiles = w.LastStat.TotalFiles
		ws.FailedFiles = w.LastStat.FailedFiles
		ws.DispatchSkips = w.LastStat.DispatchSkips
		ws.OverallMBps = w.LastStat.OverallMBps
		ws.WindowMBps = w.LastStat.WindowMBps
		out.TotalFiles += ws.TotalFiles
		out.TotalBytes += w.LastStat.TotalBytes
		out.FailedFiles += ws.FailedFiles
		out.DispatchSkips += ws.DispatchSkips
		out.OverallMBps += ws.OverallMBps
		out.WindowMBps += ws.WindowMBps
		out.Workers = append(out.Workers, ws)
	}
	return out
}
