// Package persist writes finished run reports to a local directory so that
// history survives process restarts and so the reports are downloadable over
// plain HTTP (which works through any port-forward / tunnel).
package persist

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// RunMeta is the compact summary stored alongside each run's CSV.
// It's what the UI "Previous runs" panel displays for historical entries.
type RunMeta struct {
	ID          string         `json:"id"`
	StartedAt   time.Time      `json:"started_at"`
	StoppedAt   time.Time      `json:"stopped_at"`
	TotalFiles  int64          `json:"total_files"`
	TotalBytes  int64          `json:"total_bytes"`
	OverallMBps float64        `json:"overall_mbps"`

	// Success / failure breakdown so the UI can show success rate at a
	// glance instead of recomputing from the CSV every render.
	FailedFiles    int64 `json:"failed_files"`
	SucceededFiles int64 `json:"succeeded_files"`

	// Configured workload — captured at seal time so the Previous-runs
	// overview can show what was attempted (vs. only what completed).
	UploadUsers             int  `json:"upload_users"`
	DownloadUsers           int  `json:"download_users"`
	ParallelStreams         int  `json:"parallel_streams"`
	DownloadParallelStreams int  `json:"download_parallel_streams"`
	FilesPerMinute          int    `json:"files_per_minute"`
	// Workload toggles captured at run start so historical reports + the
	// UI can branch on what the run actually exercised. Without these,
	// a download-disabled run still showed an empty "Downloads" tile and
	// the analyzer's download-stalled suggestion logic had no way to
	// distinguish "0 stalled because feature off" from "0 stalled because
	// it worked perfectly." Same problem with normal vs large uploads.
	NormalEnabled           bool   `json:"normal_enabled"`
	LargeEnabled            bool   `json:"large_enabled"`
	DownloadEnabled         bool   `json:"download_enabled"`
	DownloadMatchMode       string `json:"download_match_mode,omitempty"`

	// DispatchSkips records how many file uploads were SKIPPED at dispatch
	// time because every parallel SSH slot was busy. Non-zero means the run
	// hit a capacity ceiling and the actual upload rate fell below the
	// requested files-per-minute target. Surfaced in the Previous-runs UI
	// as a "Throttled" badge so operators immediately see when a config
	// can't sustain its requested workload.
	DispatchSkips int64 `json:"dispatch_skips"`

	// DownloadStalled counts upload rows whose matching download never
	// arrived from the server before teardown. These rows are stamped with
	// download_error=DOWNLOAD_TIMEOUT_LOCAL in the CSV. Non-zero usually
	// means downloads couldn't keep up with the upload rate (raise
	// download.parallel_streams or add download users) or that server-side
	// routing didn't deliver the file to any download user's outbox.
	DownloadStalled int64 `json:"download_stalled,omitempty"`

	// Local-host capacity peaks captured during the run by a 2-second
	// sampler. Persisted so the analysis trailer in the CSV and the
	// Previous-runs UI can tell the operator whether the bottleneck was
	// the local box (CPU, FDs) or somewhere else (network, server).
	PeakCPUPercent float64 `json:"peak_cpu_percent,omitempty"`
	AvgCPUPercent  float64 `json:"avg_cpu_percent,omitempty"`
	PeakFDInUse    int64   `json:"peak_fd_in_use,omitempty"`
	PeakGoroutines int     `json:"peak_goroutines,omitempty"`
	PeakHeapMB     float64 `json:"peak_heap_mb,omitempty"`
	PeakWindowMBps float64 `json:"peak_window_mbps,omitempty"`
	NumCPU         int     `json:"num_cpu,omitempty"`

	// ConcurrentRunsAtPeak (v0.17.0) records the highest concurrent-run
	// count observed by the sampler during this run's lifetime.
	// Without it, a multi-run setup where the box was shared between N
	// loads attributes the whole machine's peak CPU/FD/heap to each
	// run individually — making each one look like a runaway. Surfaced
	// in the Previous-runs UI as "shared with N runs at peak" so an
	// operator can interpret high-CPU numbers in context. 1 (or 0) =
	// solo run, behaviour identical to the pre-v0.17 single-run case.
	ConcurrentRunsAtPeak int `json:"concurrent_runs_at_peak,omitempty"`

	// Suggestions is the analyst's narrative for this run — what slowed it
	// down and what to change next time. Generated at seal time by the
	// internal/analyze package and embedded so the CSV and UI render
	// the exact same advice.
	Suggestions []Suggestion `json:"suggestions,omitempty"`

	// Latency holds per-stage percentile snapshots captured by the
	// runner's lock-free histograms. Populated at seal time so SLA
	// reports against the persisted meta are deterministic and don't
	// need to re-derive percentiles from the CSV.
	Latency *LatencyReport `json:"latency,omitempty"`

	// Interrupted is true when the meta was synthesised by the boot-time
	// recovery path because the process exited before sealAllAndWriteMeta
	// could run. Counts may be approximate (built from CSV rows still on
	// disk); the run did NOT complete cleanly.
	Interrupted bool `json:"interrupted,omitempty"`

	Disabled []DisabledUser `json:"disabled,omitempty"`
}

// LatencyReport is the per-stage percentile summary for one run.
// Values are nanoseconds for portability (JSON consumers / dashboards
// can divide as they prefer). UploadCOR is the coordinated-omission-
// corrected upload latency: end-time minus the dispatcher's *intended*
// start, which includes any queue wait when the parallel-stream
// semaphore was full. It is the latency a service consumer actually
// perceives, in contrast to the raw transfer time on the SFTP wire.
type LatencyReport struct {
	Upload    *LatencyStage `json:"upload,omitempty"`
	UploadCOR *LatencyStage `json:"upload_cor,omitempty"`
	Dial      *LatencyStage `json:"dial,omitempty"`
}

// LatencyStage is one histogram's percentile points. Count is the
// number of observations the percentiles were computed from. All time
// fields are in nanoseconds.
type LatencyStage struct {
	Count uint64 `json:"count"`
	P50   int64  `json:"p50_ns"`
	P95   int64  `json:"p95_ns"`
	P99   int64  `json:"p99_ns"`
	P999  int64  `json:"p999_ns"`
	Max   int64  `json:"max_ns"`
	Mean  int64  `json:"mean_ns"`
}

// Suggestion is one finding the analyzer produced. Severity drives sort
// order and UI colour; Action is the concrete config change to try next.
type Suggestion struct {
	Severity string `json:"severity"` // "critical" | "warn" | "info"
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Action   string `json:"action,omitempty"`
}

// DisabledUser is one row in RunMeta.Disabled.
type DisabledUser struct {
	User        string    `json:"user"`
	Kind        string    `json:"kind"` // "upload" or "download"
	At          time.Time `json:"at"`
	Consecutive int64     `json:"consecutive"`
	TotalFailed int64     `json:"total_failed"`
	LastCode    string    `json:"last_code"`
	LastFile    string    `json:"last_file"` // basename of the most recent file involved (empty for non-file failures)
	LastAt      time.Time `json:"last_at"`
}

// CSVPath returns the absolute path where the report CSV is stored for a run.
func CSVPath(dir, id string) string {
	return filepath.Join(dir, sanitize(id)+".csv")
}

// MetaPath returns the path where the metadata JSON lives.
func MetaPath(dir, id string) string {
	return filepath.Join(dir, sanitize(id)+".json")
}

// WriteMeta writes the RunMeta JSON atomically (temp file + rename). Files
// land at 0o600 and the directory at 0o700 because the metadata can include
// disabled-user lists and (transitively) anything the run wants to surface;
// keep it owner-only on shared hosts.
func WriteMeta(dir string, m RunMeta) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir reports dir: %w", err)
	}
	tmp := MetaPath(dir, m.ID) + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, MetaPath(dir, m.ID))
}

// ListMeta returns all metadata files in dir, newest StartedAt first.
func ListMeta(dir string) ([]RunMeta, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []RunMeta
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || strings.HasSuffix(e.Name(), ".tmp") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var m RunMeta
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		if m.ID == "" {
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out, nil
}

// sanitize makes a filename-safe token out of an arbitrary run id.
func sanitize(s string) string {
	repl := func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-' || r == '_' || r == '.':
			return r
		default:
			return '_'
		}
	}
	return strings.Map(repl, s)
}
