package web

import (
	"context"
	"embed"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/config"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/hostinfo"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/persist"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/proc"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/report"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/runner"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/sftpx"
)

//go:embed static
var staticFS embed.FS

// maxRetainedRuns bounds in-memory history so a long-running server doesn't
// accumulate unbounded state. Older runs are dropped oldest-first.
const maxRetainedRuns = 10

type Server struct {
	mu             sync.Mutex
	runs           map[string]*runner.Run
	order          []string // chronological, index 0 = oldest
	procMon        *proc.Monitor
	reportsDir     string
	schedules      *scheduleStore // nil if -schedules-dir wasn't provided
	stopCh         chan struct{}  // closed on shutdown to stop background tickers
	knownHostsPath string         // set by main.go from -known-hosts; "" = TOFU disabled
}

// NewServer constructs the HTTP server. schedulesDir may be empty, in which
// case the scheduler endpoints return 503 and no ticker runs.
func NewServer(reportsDir, schedulesDir string) *Server {
	s := &Server{
		runs:       map[string]*runner.Run{},
		procMon:    proc.New(),
		reportsDir: reportsDir,
		stopCh:     make(chan struct{}),
	}
	if schedulesDir != "" {
		s.schedules = newScheduleStore(schedulesDir)
		go s.scheduleTicker(s.stopCh)
	}
	return s
}

// Shutdown stops background tickers. Call before exiting.
func (s *Server) Shutdown() { close(s.stopCh) }

// SetKnownHostsPath records the path the operator passed via -known-hosts so
// the probe handler can offer Trust-On-First-Use enrollment. Pass "" when
// the operator launched in -insecure-host-key mode — TOFU isn't meaningful
// there and the probe handler will reject TOFU requests with a clear error.
func (s *Server) SetKnownHostsPath(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.knownHostsPath = path
}

func (s *Server) getKnownHostsPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.knownHostsPath
}

// startedAt is recorded once at construction for the /healthz uptime field.
var processStart = time.Now()

// /api/probe — quick connectivity check before a real run. Tries (in
// order): TCP dial → SSH handshake → SFTP subsystem → optional folder
// listing. Returns per-stage timings and the friendly error of whichever
// stage failed first. Never holds the connection open — opens, validates,
// closes within ~15 s.
//
// Body: {"host","port","username","password","folder"}. Username/password
// optional — if absent, only the TCP stage runs. Folder optional too.
func (s *Server) handleProbe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Host             string `json:"host"`
		Port             int    `json:"port"`
		Username         string `json:"username"`
		Password         string `json:"password"`
		Folder           string `json:"folder"`
		// TrustOnFirstUse, when true, instructs the probe to ADD this server's
		// host key to the known_hosts file if it isn't there yet. A key that's
		// already known and matches is silently accepted. A key that's already
		// known and DIFFERENT is refused (MITM signal). Requires the server
		// was launched with -known-hosts <path>.
		TrustOnFirstUse  bool `json:"trust_on_first_use"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Host == "" || req.Port <= 0 {
		http.Error(w, "host and port required", http.StatusBadRequest)
		return
	}

	addr := net.JoinHostPort(req.Host, fmt.Sprintf("%d", req.Port))
	out := map[string]any{"ok": false, "host": req.Host, "port": req.Port}

	// Stage 1 — TCP dial. Tightest timeout of the bunch; if we can't reach
	// the box at all, no point trying SSH.
	t0 := time.Now()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		out["stage"] = "tcp"
		out["error"] = err.Error()
		out["tcp_ms"] = time.Since(t0).Milliseconds()
		writeJSON(w, out)
		return
	}
	out["tcp_ms"] = time.Since(t0).Milliseconds()
	conn.Close()

	// If no creds given, stop here — TCP-only probe.
	if req.Username == "" {
		out["ok"] = true
		out["stage"] = "tcp"
		out["note"] = "TCP only — supply username + password to verify SSH/SFTP/auth"
		writeJSON(w, out)
		return
	}

	// Stage 2 + 3 — SSH handshake + SFTP subsystem.
	//
	// Three modes, chosen per-request:
	//   a) TrustOnFirstUse=true + known_hosts set:
	//      Use TOFUCallback — auto-append unknown keys, accept after.
	//   b) TrustOnFirstUse=false + known_hosts set:
	//      Use CapturePreviewCallback — strict check, but on "key unknown"
	//      capture the fingerprint AND respond with requires_consent=true
	//      so the UI can show an explicit Accept/Reject prompt.
	//   c) -insecure-host-key mode (no known_hosts path):
	//      Fall through to the process-wide callback (insecure).
	t1 := time.Now()
	var dialOpts sftpx.DialOpts
	var capturedFP string
	khPath := s.getKnownHostsPath()
	switch {
	case req.TrustOnFirstUse:
		if khPath == "" {
			out["stage"] = "ssh_or_sftp"
			out["error"] = "trust_on_first_use requires the server to have been launched with -known-hosts <path>"
			writeJSON(w, out)
			return
		}
		cb, cberr := sftpx.TOFUCallback(khPath, func(host, fp string) {
			capturedFP = fp
		})
		if cberr != nil {
			out["stage"] = "ssh_or_sftp"
			out["error"] = "tofu setup: " + cberr.Error()
			writeJSON(w, out)
			return
		}
		dialOpts.HostKeyCallback = cb
	case khPath != "":
		// Capture-preview path — strict check + capture fingerprint on
		// unknown-host so the UI can prompt the user.
		cb, cberr := sftpx.CapturePreviewCallback(khPath, func(host, fp string) {
			capturedFP = fp
		})
		if cberr != nil {
			out["stage"] = "ssh_or_sftp"
			out["error"] = "host-key check setup: " + cberr.Error()
			writeJSON(w, out)
			return
		}
		dialOpts.HostKeyCallback = cb
	}
	c, err := sftpx.DialWithOpts(req.Host, req.Port, req.Username, req.Password, dialOpts)
	if err != nil {
		out["stage"] = "ssh_or_sftp"
		out["error"] = err.Error()
		out["ssh_ms"] = time.Since(t1).Milliseconds()
		// If the failure was specifically the "user consent required" sentinel
		// from CapturePreviewCallback AND we successfully captured a
		// fingerprint, surface it so the UI can render an Accept/Reject
		// prompt. Same shape regardless of whether the SSH error wraps the
		// sentinel directly or in a transport-layer message.
		if capturedFP != "" && (errors.Is(err, sftpx.ErrHostKeyConsentRequired) ||
			strings.Contains(err.Error(), "user consent required") ||
			strings.Contains(err.Error(), "knownhosts: key is unknown") ||
			strings.Contains(err.Error(), "ssh: handshake failed")) {
			out["requires_consent"] = true
			out["captured_fingerprint"] = capturedFP
			out["captured_for_host"] = req.Host
			// User-friendly headline; the raw err.Error() is technical SSH stderr.
			out["error"] = "Server presented a new host key. Verify the fingerprint and accept to continue."
		}
		writeJSON(w, out)
		return
	}
	out["ssh_sftp_ms"] = time.Since(t1).Milliseconds()
	defer c.Close()
	if capturedFP != "" && req.TrustOnFirstUse {
		out["captured_fingerprint"] = capturedFP
		out["captured_for_host"] = req.Host
		// The process-wide host-key callback was loaded once at startup from
		// the known_hosts file. We just appended a new entry — reload so the
		// next /api/start (and every existing run's reconnect) sees it.
		if err := sftpx.UseKnownHosts(s.getKnownHostsPath()); err != nil {
			log.Printf("reload known_hosts after TOFU: %v", err)
		}
	}

	// Stage 4 — optional folder list. Validates the remote path exists +
	// the user can read it. Common configuration mistake to catch early.
	if req.Folder != "" {
		t2 := time.Now()
		_, err := c.List(req.Folder)
		out["list_ms"] = time.Since(t2).Milliseconds()
		if err != nil {
			out["stage"] = "list"
			out["error"] = fmt.Sprintf("list %q: %v", req.Folder, err)
			writeJSON(w, out)
			return
		}
	}

	out["ok"] = true
	out["stage"] = "complete"
	writeJSON(w, out)
}

// /api/host — one-shot snapshot of the client machine's capacity. Called
// once at UI load (and any time the operator wants to refresh) so testers
// always see the real ceilings (FD limit, cores, RAM, NICs) of the box
// they're running on. Cheap — no subprocess, no probes; just rlimit +
// /dev/fd readdir + /proc reads.
func (s *Server) handleHost(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, hostinfo.Snapshot())
}

// /healthz — cheap liveness probe. Always 200 while the HTTP server is up.
// Includes uptime + whether a run is active so orchestrators can tell the
// difference between "idle but alive" and "executing a test".
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	active := s.activeRun()
	out := map[string]any{
		"status":     "ok",
		"uptime_sec": int64(time.Since(processStart).Seconds()),
		"active_run": active != nil,
	}
	if active != nil {
		out["active_run_id"] = active.ID
	}
	writeJSON(w, out)
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	sub, _ := fs.Sub(staticFS, "static")
	mux.Handle("/", http.FileServer(http.FS(sub)))

	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/api/host", s.handleHost)
	mux.HandleFunc("/api/probe", s.handleProbe)
	mux.HandleFunc("/api/start", s.handleStart)
	mux.HandleFunc("/api/stop", s.handleStop)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/report.csv", s.handleReportCSV)
	mux.HandleFunc("/api/runs", s.handleRuns)
	mux.HandleFunc("/api/schedule", s.handleScheduleCreate)
	mux.HandleFunc("/api/schedules", s.handleScheduleList)
	mux.HandleFunc("/api/schedule/cancel", s.handleScheduleCancel)
	return mux
}

// latest returns the most recently started run, or nil if none.
func (s *Server) latest() *runner.Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.order) == 0 {
		return nil
	}
	return s.runs[s.order[len(s.order)-1]]
}

// pick resolves a run by the `run` query param, or falls back to the latest.
func (s *Server) pick(r *http.Request) *runner.Run {
	if id := r.URL.Query().Get("run"); id != "" {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.runs[id]
	}
	return s.latest()
}

// activeRun returns the currently-running run, if any.
func (s *Server) activeRun() *runner.Run {
	run := s.latest()
	if run == nil {
		return nil
	}
	if run.IsActive() {
		return run
	}
	return nil
}

type startReq struct {
	Host                   string  `json:"host"`
	Port                   int     `json:"port"`
	UploadFolder           string  `json:"upload_folder"`
	ParallelStreams        int     `json:"parallel_streams"`
	DurationHours          float64 `json:"duration_hours"`
	PollSeconds            int     `json:"poll_seconds"`
	TrackIDTimeoutS        int     `json:"track_id_timeout_seconds"`
	MaxConsecutiveFailures int     `json:"max_consecutive_failures"`

	NormalEnabled     bool   `json:"normal_enabled"`
	FilesPerMinute    int    `json:"files_per_minute"`
	NormalMinMB       int    `json:"normal_min_mb"`
	NormalMaxMB       int    `json:"normal_max_mb"`
	NormalContentType string `json:"normal_content_type"`
	NormalUsersCSV    string `json:"normal_users_csv"`

	LargeEnabled    bool   `json:"large_enabled"`
	LargeMin        int    `json:"large_min"`
	LargeMax        int    `json:"large_max"`
	LargeUnit       string `json:"large_unit"` // "MB" or "GB"
	IntervalMinutes int    `json:"interval_minutes"`
	LargeUsersCSV   string `json:"large_users_csv"`

	DownloadEnabled         bool   `json:"download_enabled"`
	DownloadFolder          string `json:"download_folder"`
	DownloadParallelStreams int    `json:"download_parallel_streams"`
	DownloadUsersCSV        string `json:"download_users_csv"`
}

// buildRunConfig converts a startReq to a RunConfig, parsing the embedded
// user CSVs. Returns a friendly error suitable for 4xx responses.
func buildRunConfig(req startReq) (*config.RunConfig, error) {
	cfg := &config.RunConfig{
		Host:                   req.Host,
		Port:                   req.Port,
		UploadFolder:           req.UploadFolder,
		ParallelStreams:        req.ParallelStreams,
		DurationHours:          req.DurationHours,
		PollInterval:           time.Duration(req.PollSeconds) * time.Second,
		TrackIDTimeout:         time.Duration(req.TrackIDTimeoutS) * time.Second,
		MaxConsecutiveFailures: req.MaxConsecutiveFailures,
	}
	if req.NormalEnabled {
		cfg.Normal = &config.NormalLoad{
			FilesPerMinute: req.FilesPerMinute,
			MinSizeMB:      req.NormalMinMB,
			MaxSizeMB:      req.NormalMaxMB,
			ContentType:    req.NormalContentType,
		}
		users, err := config.ParseUsersCSV(strings.NewReader(req.NormalUsersCSV))
		if err != nil {
			return nil, fmt.Errorf("normal users csv: %w", err)
		}
		cfg.NormalUsers = users
	}
	if req.LargeEnabled {
		cfg.LargeFile = &config.LargeFileLoad{
			MinSize:         req.LargeMin,
			MaxSize:         req.LargeMax,
			Unit:            req.LargeUnit,
			IntervalMinutes: req.IntervalMinutes,
		}
		users, err := config.ParseUsersCSV(strings.NewReader(req.LargeUsersCSV))
		if err != nil {
			return nil, fmt.Errorf("large users csv: %w", err)
		}
		cfg.LargeFileUsers = users
	}
	if req.DownloadEnabled {
		cfg.Download = &config.DownloadLoad{
			Folder:          req.DownloadFolder,
			ParallelStreams: req.DownloadParallelStreams,
		}
		users, err := config.ParseUsersCSV(strings.NewReader(req.DownloadUsersCSV))
		if err != nil {
			return nil, fmt.Errorf("download users csv: %w", err)
		}
		cfg.DownloadUsers = users
	}
	return cfg, nil
}

// startRun is the single code path that creates a Run from a startReq and
// registers it in the server's in-memory map. Both /api/start and the
// scheduler go through here so they stay in lockstep. startedBy tags the
// run ("manual" or "schedule") so the UI can badge it.
func (s *Server) startRun(req startReq, startedBy string) (*runner.Run, error) {
	if s.activeRun() != nil {
		return nil, fmt.Errorf("a run is already active — stop it first")
	}
	cfg, err := buildRunConfig(req)
	if err != nil {
		return nil, err
	}
	run, err := runner.StartWithPersist(context.Background(), cfg, s.reportsDir)
	if err != nil {
		return nil, err
	}
	run.StartedBy = startedBy
	s.mu.Lock()
	s.runs[run.ID] = run
	s.order = append(s.order, run.ID)
	for len(s.order) > maxRetainedRuns {
		oldID := s.order[0]
		old := s.runs[oldID]
		if old != nil && old.IsActive() {
			break
		}
		delete(s.runs, oldID)
		s.order = s.order[1:]
	}
	s.mu.Unlock()
	return run, nil
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req startReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	run, err := s.startRun(req, "manual")
	if err != nil {
		code := http.StatusBadRequest
		if strings.Contains(err.Error(), "already active") {
			code = http.StatusConflict
		}
		http.Error(w, err.Error(), code)
		return
	}
	writeJSON(w, map[string]any{"run_id": run.ID})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	run := s.activeRun()
	if run == nil {
		http.Error(w, "no active run", http.StatusNotFound)
		return
	}
	run.Stop()
	writeJSON(w, map[string]any{"stopped": true})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	run := s.pick(r)
	procStats := s.procMon.Sample()
	if run == nil {
		// If the caller asked for a specific run that only lives on disk,
		// surface its metadata so the UI can still show totals alongside the
		// CSV link. Otherwise return the "no active run" shape.
		if id := r.URL.Query().Get("run"); id != "" && s.reportsDir != "" {
			if metas, err := persist.ListMeta(s.reportsDir); err == nil {
				for _, m := range metas {
					if m.ID == id {
						writeJSON(w, map[string]any{
							"active":     false,
							"run_id":     m.ID,
							"started_at": m.StartedAt,
							"historical": true,
							"metrics": map[string]any{
								"total_files":  m.TotalFiles,
								"total_bytes":  m.TotalBytes,
								"overall_mbps": m.OverallMBps,
							},
							"proc": procStats,
						})
						return
					}
				}
			}
		}
		writeJSON(w, map[string]any{"active": false, "proc": procStats})
		return
	}
	active := run.IsActive()
	// Tail snapshot only — the status poll returns at most the last 200 rows,
	// so copying the full in-memory slice every 2s (O(total)) was wasted work
	// on long runs. SnapshotTail is O(200) regardless of run length.
	recs := run.Report.SnapshotTail(200)
	writeJSON(w, map[string]any{
		"active":              active,
		"run_id":              run.ID,
		"started_at":          run.StartedAt,
		"started_by":          run.StartedBy,
		"metrics":             run.Metrics.Snapshot(),
		"slowdowns_enriched":  run.EnrichSlowdowns(),
		"records":             recs,
		"pending_trackids":    run.Watcher.PendingCount(),
		"dispatch_skips":      run.DispatchSkips.Load(),
		"download_completed":  run.DownloadCompleted.Load(),
		"download_orphans":    run.DownloadOrphans.Load(),
		"download_in_queue":   run.DownloadQueueDepth(), // 0 under pairless poll model
		"download_dropped":    run.DownloadDropped.Load(),
		"failed_files":        run.FailedFiles.Load(),
		"failed_bytes":        run.FailedBytes.Load(),
		"errors_by_code":      run.ErrorsByCode(),
		"disabled_users":      run.DisabledUsers(),
		"records_in_memory":   run.Report.LiveCount(),
		"records_flushed":     run.Report.FlushedCount(),
		"proc":                procStats,
	})
}

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	// Live / in-memory runs.
	s.mu.Lock()
	live := make([]map[string]any, 0, len(s.order))
	liveIDs := map[string]bool{}
	for _, id := range s.order {
		run, ok := s.runs[id]
		if !ok {
			continue
		}
		liveIDs[id] = true
		snap := run.Metrics.Snapshot()
		live = append(live, map[string]any{
			"id":           run.ID,
			"started_at":   run.StartedAt,
			"started_by":   run.StartedBy, // "manual" or "schedule"
			"active":       run.IsActive(),
			"total_files":  snap.TotalFiles,
			"total_bytes":  snap.TotalBytes,
			"overall_mbps": snap.OverallMBps,
			"source":       "memory",
		})
	}
	s.mu.Unlock()
	// Historical runs from disk (skip any that are already in-memory).
	var historical []map[string]any
	if s.reportsDir != "" {
		metas, _ := persist.ListMeta(s.reportsDir)
		for _, m := range metas {
			if liveIDs[m.ID] {
				continue
			}
			historical = append(historical, map[string]any{
				"id":                        m.ID,
				"started_at":                m.StartedAt,
				"stopped_at":                m.StoppedAt,
				"active":                    false,
				"total_files":               m.TotalFiles,
				"total_bytes":               m.TotalBytes,
				"overall_mbps":              m.OverallMBps,
				"failed_files":              m.FailedFiles,
				"succeeded_files":           m.SucceededFiles,
				"upload_users":              m.UploadUsers,
				"download_users":            m.DownloadUsers,
				"parallel_streams":          m.ParallelStreams,
				"download_parallel_streams": m.DownloadParallelStreams,
				"files_per_minute":          m.FilesPerMinute,
				"download_enabled":          m.DownloadEnabled,
				"source":                    "disk",
			})
		}
	}
	// Newest first: reverse live, append historical (already sorted newest-first).
	for i, j := 0, len(live)-1; i < j; i, j = i+1, j-1 {
		live[i], live[j] = live[j], live[i]
	}
	writeJSON(w, map[string]any{"runs": append(live, historical...)})
}

func (s *Server) handleReportCSV(w http.ResponseWriter, r *http.Request) {
	runID := r.URL.Query().Get("run")
	run := s.pick(r)
	// Live / in-memory run: serve the streaming CSV file (rows already
	// sealed) + the in-memory tail (rows still mutable). This keeps the
	// download correct even on multi-hour runs where the in-memory slice
	// alone would miss most of the data.
	if run != nil {
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.csv"`, run.ID))
		snap := run.Metrics.Snapshot()
		eff := func(bytes int64, startTime time.Time, dur time.Duration) float64 {
			return runner.EffectiveSpeedMBps(bytes, startTime, dur, snap)
		}
		streamPath := run.ReportStreamPath()
		if streamPath != "" {
			// Active streaming path: the on-disk file is append-only. Read its
			// current size, stream those bytes (frozen — flushes only ever
			// extend past this mark), then append the in-memory tail as CSV
			// rows. If the file is empty yet (no row has been flushed), write
			// the header ourselves so clients always get a valid file.
			if data, err := os.ReadFile(streamPath); err == nil && len(data) > 0 {
				_, _ = w.Write(data)
			} else {
				cw := csv.NewWriter(w)
				_ = cw.Write(report.CSVHeader)
				cw.Flush()
			}
			cw := csv.NewWriter(w)
			_ = run.Report.WriteRemainingCSV(cw, run.SlowdownMinutes(), eff)
			cw.Flush()
			return
		}
		// Run is in-memory but its stream writer has been closed (finalized).
		// The fully-flushed file lives on disk; stream that. We only fall back
		// to the in-memory snapshot when no reports-dir is configured at all
		// (and even then, after seal, the snapshot is typically drained).
		if s.reportsDir != "" {
			if f, err := os.Open(persist.CSVPath(s.reportsDir, run.ID)); err == nil {
				defer f.Close()
				_, _ = io.Copy(w, f)
				return
			}
		}
		if err := report.WriteCSV(w, run.Report.Snapshot(), run.SlowdownMinutes(), eff); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	// Historical run → stream the file from disk.
	if runID == "" || s.reportsDir == "" {
		http.Error(w, "no run", http.StatusNotFound)
		return
	}
	path := persist.CSVPath(s.reportsDir, runID)
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "report not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.csv"`, runID))
	// Stream the file contents; client disconnects are swallowed as usual.
	_, _ = io.Copy(w, f)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

