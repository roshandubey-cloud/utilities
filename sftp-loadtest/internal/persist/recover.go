package persist

import (
	"encoding/csv"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// RecoverInterrupted scans dir for run CSVs that have no matching meta
// JSON and synthesises a best-effort meta marked Interrupted=true.
//
// Without this, a process crash mid-run leaves an orphan <run-id>.csv on
// disk and the run never appears in /api/runs (which only walks meta
// JSONs). The synthesised meta gives the operator a row to inspect, plus
// the CSV download still works. Counts are derived from CSV rows so they
// match the data that survived; intermediate state lost in memory at
// crash time is gone.
//
// Returns the IDs that were recovered. Best-effort: a malformed CSV is
// skipped silently rather than blocking startup.
func RecoverInterrupted(dir string) ([]string, error) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	have := map[string]bool{} // base name (without .json) of every meta on disk
	csvs := []string{}        // base names of csvs we saw
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		switch {
		case strings.HasSuffix(name, ".tmp"):
			continue
		case strings.HasSuffix(name, ".json"):
			have[strings.TrimSuffix(name, ".json")] = true
		case strings.HasSuffix(name, ".csv"):
			csvs = append(csvs, strings.TrimSuffix(name, ".csv"))
		}
	}
	var recovered []string
	for _, base := range csvs {
		if have[base] {
			continue
		}
		path := filepath.Join(dir, base+".csv")
		meta, ok := synthesiseMetaFromCSV(base, path)
		if !ok {
			continue
		}
		if err := WriteMeta(dir, meta); err != nil {
			continue
		}
		recovered = append(recovered, base)
	}
	return recovered, nil
}

// synthesiseMetaFromCSV walks the CSV row by row to recover the counts
// the running process would have written. Stops at the first blank line
// (which marks the start of an analysis trailer if one was written) or at
// any malformed row. Returns ok=false for empty / unreadable CSVs.
func synthesiseMetaFromCSV(id, path string) (RunMeta, bool) {
	f, err := os.Open(path)
	if err != nil {
		return RunMeta{}, false
	}
	defer f.Close()
	cr := csv.NewReader(f)
	cr.FieldsPerRecord = -1 // tolerate trailer rows of different widths

	var (
		header     []string
		colStart   = -1
		colEnd     = -1
		colSize    = -1
		colErrCode = -1
		totalFiles int64
		failed     int64
		totalBytes int64
		earliest   time.Time
		latest     time.Time
	)
	rowNum := 0
	for {
		row, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		rowNum++
		if rowNum == 1 {
			header = row
			for i, h := range header {
				switch h {
				case "start_time":
					colStart = i
				case "end_time":
					colEnd = i
				case "size_bytes":
					colSize = i
				case "error_code":
					colErrCode = i
				}
			}
			continue
		}
		// Trailer: a row that starts with "" or "#" we treat as the end of data.
		if len(row) == 0 || (len(row) > 0 && (row[0] == "" || strings.HasPrefix(row[0], "#"))) {
			break
		}
		totalFiles++
		if colSize >= 0 && colSize < len(row) {
			if n, err := strconv.ParseInt(row[colSize], 10, 64); err == nil {
				totalBytes += n
			}
		}
		if colErrCode >= 0 && colErrCode < len(row) && strings.TrimSpace(row[colErrCode]) != "" {
			failed++
		}
		if colStart >= 0 && colStart < len(row) {
			if t, err := time.Parse(time.RFC3339Nano, row[colStart]); err == nil {
				if earliest.IsZero() || t.Before(earliest) {
					earliest = t
				}
			}
		}
		if colEnd >= 0 && colEnd < len(row) {
			if t, err := time.Parse(time.RFC3339Nano, row[colEnd]); err == nil {
				if t.After(latest) {
					latest = t
				}
			}
		}
	}
	if totalFiles == 0 {
		// Empty CSV — nothing useful to synthesise. Don't write a stub
		// meta; let the file linger and be ignored.
		return RunMeta{}, false
	}
	if latest.IsZero() {
		latest = earliest
	}
	dur := latest.Sub(earliest)
	var mbps float64
	if dur > 0 {
		mbps = float64(totalBytes) / (1024.0 * 1024.0) / dur.Seconds()
	}
	return RunMeta{
		ID:             id,
		StartedAt:      earliest,
		StoppedAt:      latest,
		TotalFiles:     totalFiles,
		TotalBytes:     totalBytes,
		OverallMBps:    mbps,
		FailedFiles:    failed,
		SucceededFiles: totalFiles - failed,
		Interrupted:    true,
	}, true
}
