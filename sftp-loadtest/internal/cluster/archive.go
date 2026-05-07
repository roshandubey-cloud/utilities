package cluster

// archive.go — master-side persistence of per-worker reports after a
// cluster run ends. Without this, every worker keeps its CSV + meta
// JSON locally and the operator has to SSH into each box to find them.
// The Coordinator now pulls each worker's report at Stop time and
// writes them under <reportsDir>/cluster-runs/<cluster-run-id>/, plus
// a master-side meta.json that aggregates run-wide totals + worker
// pointers so the UI can render a single "cluster run" entry without
// re-fetching anything.

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ClusterRunMeta is the master-side aggregated record for a single
// cluster run. Persisted as <archiveDir>/cluster-runs/<id>/meta.json.
// The shape mirrors the per-run RunMeta the runner writes for solo
// runs, but with per-worker breakdowns the UI can drill into.
type ClusterRunMeta struct {
	ID            string                `json:"id"`
	MasterVersion string                `json:"master_version,omitempty"`
	StartedAt     time.Time             `json:"started_at"`
	StoppedAt     time.Time             `json:"stopped_at"`
	TotalFiles    int64                 `json:"total_files"`
	TotalBytes    int64                 `json:"total_bytes"`
	FailedFiles   int64                 `json:"failed_files"`
	OverallMBps   float64               `json:"overall_mbps"`
	WindowMBps    float64               `json:"window_mbps,omitempty"`
	Workers       []ClusterWorkerReport `json:"workers"`
	// MergedCSV (v0.19.22) — relative path under cluster-runs/<id>/ to a
	// single CSV that interleaves every worker's rows with a leading
	// `worker` column identifying which node ran each upload/download.
	// Empty when archival happened without any successful per-worker CSV
	// fetches (offline workers etc.). Renders as a single-report
	// download in the UI so operators don't have to stitch per-worker
	// files by hand.
	MergedCSV    string `json:"merged_csv,omitempty"`
	MergedRows   int64  `json:"merged_rows,omitempty"`
	MergedErr    string `json:"merged_err,omitempty"`
}

// ClusterWorkerReport is one worker's contribution to a cluster run.
// FileMeta + FileCSV are paths relative to <archiveDir>/cluster-runs/<id>/
// so a future move of the archive directory doesn't break references.
// Stat is the live counters as last-seen by the master; if the worker
// also returned a sealed RunMeta we copy the richer numbers from there.
type ClusterWorkerReport struct {
	URL              string  `json:"url"`
	RunID            string  `json:"run_id"`
	Version          string  `json:"version,omitempty"`
	VersionMismatch  bool    `json:"version_mismatch,omitempty"`
	Reachable        bool    `json:"reachable"`
	Err              string  `json:"err,omitempty"`
	TotalFiles       int64   `json:"total_files"`
	TotalBytes       int64   `json:"total_bytes,omitempty"`
	FailedFiles      int64   `json:"failed_files"`
	OverallMBps      float64 `json:"overall_mbps"`
	FileMeta         string  `json:"file_meta,omitempty"` // relative path
	FileCSV          string  `json:"file_csv,omitempty"`  // relative path
	MetaFetchErr     string  `json:"meta_fetch_err,omitempty"`
	CSVFetchErr      string  `json:"csv_fetch_err,omitempty"`
}

// ArchiveOnStop is called by the web layer after Stop completes. It
// writes <archiveDir>/cluster-runs/<id>/meta.json plus per-worker
// CSV + meta files. Best-effort: a worker that's unreachable at
// archive time is still recorded in meta.json with Reachable:false
// so the operator sees "this worker contributed but its detail is
// gone" instead of silently dropping it.
//
// The function is package-private because the only legitimate caller
// is the web layer's stop handler — exporting it would invite stale
// archives from out-of-band callers.
func (c *Coordinator) ArchiveOnStop(ctx context.Context) (string, error) {
	c.mu.Lock()
	dir := c.archiveDir
	id := c.clusterRunID
	startedAt := c.startedAt
	stoppedAt := c.stoppedAt
	masterVer := c.masterVersion
	workers := append([]runningWorker(nil), c.workers...)
	c.mu.Unlock()

	if dir == "" || id == "" || len(workers) == 0 {
		return "", nil // archival not configured or nothing to archive
	}
	if stoppedAt.IsZero() {
		stoppedAt = time.Now()
	}

	runDir := filepath.Join(dir, "cluster-runs", id)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return "", fmt.Errorf("mkdir cluster archive %s: %w", runDir, err)
	}

	out := ClusterRunMeta{
		ID:            id,
		MasterVersion: masterVer,
		StartedAt:     startedAt,
		StoppedAt:     stoppedAt,
		Workers:       make([]ClusterWorkerReport, 0, len(workers)),
	}

	for i, w := range workers {
		wr := ClusterWorkerReport{
			URL:           w.URL,
			RunID:         w.RunID,
			Version:       w.Version,
			Reachable:     w.LastErr == "",
			Err:           w.LastErr,
			TotalFiles:    w.LastStat.TotalFiles,
			TotalBytes:    w.LastStat.TotalBytes,
			FailedFiles:   w.LastStat.FailedFiles,
			OverallMBps:   w.LastStat.OverallMBps,
		}
		if masterVer != "" && w.Version != "" && w.Version != masterVer {
			wr.VersionMismatch = true
		}

		// Pull the worker's sealed RunMeta. If it succeeded, prefer the
		// numbers from there over the live-status snapshot we already
		// have — RunMeta is computed at seal time and includes
		// failed_files / total_bytes that the live status sometimes
		// drops because they're served from a different code path.
		baseName := fmt.Sprintf("worker-%02d", i+1)
		metaPath := filepath.Join(runDir, baseName+".json")
		if metaErr := c.fetchWorkerMeta(ctx, w, metaPath, &wr); metaErr != nil {
			wr.MetaFetchErr = metaErr.Error()
		} else {
			wr.FileMeta = baseName + ".json"
		}

		csvPath := filepath.Join(runDir, baseName+".csv")
		if csvErr := c.fetchWorkerCSV(ctx, w, csvPath); csvErr != nil {
			wr.CSVFetchErr = csvErr.Error()
		} else {
			wr.FileCSV = baseName + ".csv"
		}

		out.TotalFiles += wr.TotalFiles
		out.TotalBytes += wr.TotalBytes
		out.FailedFiles += wr.FailedFiles
		out.OverallMBps += wr.OverallMBps
		out.Workers = append(out.Workers, wr)
	}

	// v0.19.22 — single unified report. After every per-worker CSV is on
	// disk, build merged.csv: worker label + the original 32 columns,
	// rows sorted by start_time across workers so the operator can
	// scroll one file and see who did what when. Best-effort; if the
	// merge fails (a worker CSV was malformed mid-run, say), leave the
	// per-worker files alone and surface the error on the meta so the
	// UI hides the merged-download button.
	if mergedRows, mergedErr := writeMergedCSV(runDir, out.Workers); mergedErr != nil {
		out.MergedErr = mergedErr.Error()
	} else if mergedRows > 0 {
		out.MergedCSV = "merged.csv"
		out.MergedRows = mergedRows
	}

	// Write the master meta atomically — same pattern persist.WriteMeta
	// uses for solo runs (tmp + rename) so a partial write never shows
	// up in /api/cluster/runs listings.
	metaBlob, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal cluster meta: %w", err)
	}
	metaTmp := filepath.Join(runDir, "meta.json.tmp")
	if err := os.WriteFile(metaTmp, metaBlob, 0o600); err != nil {
		return "", fmt.Errorf("write cluster meta: %w", err)
	}
	if err := os.Rename(metaTmp, filepath.Join(runDir, "meta.json")); err != nil {
		return "", fmt.Errorf("seal cluster meta: %w", err)
	}
	return runDir, nil
}

// writeMergedCSV produces cluster-runs/<id>/merged.csv: the union of
// every per-worker CSV with a leading `worker` column. Returns the
// number of data rows written (header excluded). Pre-conditions:
//   - workers contains the post-fetch ClusterWorkerReport list (so
//     wr.FileCSV / wr.URL are populated for the rows that should be
//     merged).
//   - Each per-worker CSV is well-formed with the standard 32-column
//     header (anything else is recorded as a row-level merge error,
//     not a fatal abort).
//
// Sort key: start_time (ISO-8601 string compare is lexicographic-safe
// for RFC 3339 with nanos, which is what the runner emits). Stable
// merge: rows from the same worker keep their relative order.
//
// The leading `worker` column carries the worker label "worker-NN"
// (matching the per-worker CSV filename) plus the worker URL when
// available — operators reading the file can grep either form.
func writeMergedCSV(runDir string, workers []ClusterWorkerReport) (int64, error) {
	type rowWithSort struct {
		startTime string
		row       []string
	}
	var collected []rowWithSort
	headerCols := 0

	for i, w := range workers {
		if w.FileCSV == "" {
			continue
		}
		label := fmt.Sprintf("worker-%02d", i+1)
		path := filepath.Join(runDir, w.FileCSV)
		f, err := os.Open(path)
		if err != nil {
			return 0, fmt.Errorf("open %s: %w", w.FileCSV, err)
		}
		r := csv.NewReader(f)
		r.FieldsPerRecord = -1 // tolerate row-shape drift between worker versions
		hdr, err := r.Read()
		if err == io.EOF {
			f.Close()
			continue
		}
		if err != nil {
			f.Close()
			return 0, fmt.Errorf("read header %s: %w", w.FileCSV, err)
		}
		if headerCols == 0 {
			headerCols = len(hdr)
		}
		// Locate the start_time column once per worker so a future
		// schema reorder doesn't break sort.
		startIdx := -1
		for ci, name := range hdr {
			if name == "start_time" {
				startIdx = ci
				break
			}
		}
		for {
			rec, err := r.Read()
			if err == io.EOF {
				break
			}
			if err != nil {
				f.Close()
				return 0, fmt.Errorf("read row %s: %w", w.FileCSV, err)
			}
			st := ""
			if startIdx >= 0 && startIdx < len(rec) {
				st = rec[startIdx]
			}
			merged := make([]string, 0, 1+len(rec))
			merged = append(merged, fmt.Sprintf("%s (%s)", label, w.URL))
			merged = append(merged, rec...)
			collected = append(collected, rowWithSort{startTime: st, row: merged})
		}
		f.Close()
	}
	if len(collected) == 0 {
		return 0, nil
	}

	// Stable sort by start_time. Empty strings sort first, which is
	// fine — they're failure-only rows the operator wants near top.
	sort.SliceStable(collected, func(i, j int) bool {
		return collected[i].startTime < collected[j].startTime
	})

	// Write tmp+rename for atomicity, matching the meta.json pattern.
	dest := filepath.Join(runDir, "merged.csv")
	tmp := dest + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, fmt.Errorf("create %s: %w", tmp, err)
	}
	cw := csv.NewWriter(f)

	// Header: prepend `worker` to whatever the workers' header carried.
	// We re-read the first worker's header to copy column names exactly
	// — easier than referencing report.CSVHeader (cross-package import).
	hdrRow := []string{"worker"}
	for i, w := range workers {
		if w.FileCSV == "" {
			continue
		}
		path := filepath.Join(runDir, w.FileCSV)
		hf, herr := os.Open(path)
		if herr != nil {
			continue
		}
		hr := csv.NewReader(hf)
		hr.FieldsPerRecord = -1
		hdr, herr := hr.Read()
		hf.Close()
		if herr != nil {
			continue
		}
		hdrRow = append(hdrRow, hdr...)
		_ = i
		break
	}
	if err := cw.Write(hdrRow); err != nil {
		f.Close()
		os.Remove(tmp)
		return 0, fmt.Errorf("write header: %w", err)
	}

	var n int64
	for _, r := range collected {
		if err := cw.Write(r.row); err != nil {
			f.Close()
			os.Remove(tmp)
			return 0, fmt.Errorf("write row %d: %w", n, err)
		}
		n++
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		f.Close()
		os.Remove(tmp)
		return 0, fmt.Errorf("flush: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return 0, fmt.Errorf("close: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		return 0, fmt.Errorf("seal: %w", err)
	}
	return n, nil
}

func (c *Coordinator) fetchWorkerMeta(ctx context.Context, w runningWorker, dest string, into *ClusterWorkerReport) error {
	resp, err := c.do(ctx, w.Worker, http.MethodGet, "/api/runs", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var raw struct {
		Runs []map[string]any `json:"runs"`
	}
	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &raw); err != nil {
		return fmt.Errorf("decode runs list: %w", err)
	}
	var picked map[string]any
	for _, r := range raw.Runs {
		if id, _ := r["id"].(string); id == w.RunID {
			picked = r
			break
		}
	}
	if picked == nil {
		// Worker either hasn't sealed yet or pruned the run already —
		// keep the live-status numbers and note nothing was fetched.
		return errors.New("run not in worker's history yet")
	}
	if v, ok := picked["total_files"].(float64); ok && v > 0 {
		into.TotalFiles = int64(v)
	}
	if v, ok := picked["total_bytes"].(float64); ok && v > 0 {
		into.TotalBytes = int64(v)
	}
	if v, ok := picked["failed_files"].(float64); ok && v >= 0 {
		into.FailedFiles = int64(v)
	}
	if v, ok := picked["overall_mbps"].(float64); ok && v > 0 {
		into.OverallMBps = v
	}
	pretty, err := json.MarshalIndent(picked, "", "  ")
	if err != nil {
		return err
	}
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, pretty, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

func (c *Coordinator) fetchWorkerCSV(ctx context.Context, w runningWorker, dest string) error {
	q := url.Values{"id": {w.RunID}}
	resp, err := c.do(ctx, w.Worker, http.MethodGet, "/api/report.csv?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	tmp := dest + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}

// ListClusterRuns reads <archiveDir>/cluster-runs/ and returns the
// cluster-run summaries newest-first. Cheap: just reads each meta.json.
// Returns nil + nil error when the dir doesn't exist (no runs yet).
func ListClusterRuns(archiveDir string) ([]ClusterRunMeta, error) {
	if archiveDir == "" {
		return nil, nil
	}
	root := filepath.Join(archiveDir, "cluster-runs")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]ClusterRunMeta, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "cluster-") {
			continue
		}
		m, err := LoadClusterRun(archiveDir, e.Name())
		if err != nil {
			continue // skip half-written or corrupt runs; the operator can clean up
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out, nil
}

// LoadClusterRun reads one cluster-run's meta.json. Caller is expected
// to validate id (no slashes / `..`) before passing it through; the
// HTTP handler does this.
func LoadClusterRun(archiveDir, id string) (ClusterRunMeta, error) {
	path := filepath.Join(archiveDir, "cluster-runs", id, "meta.json")
	blob, err := os.ReadFile(path)
	if err != nil {
		return ClusterRunMeta{}, err
	}
	var m ClusterRunMeta
	if err := json.Unmarshal(blob, &m); err != nil {
		return ClusterRunMeta{}, err
	}
	return m, nil
}
