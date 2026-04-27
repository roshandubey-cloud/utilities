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
	Disabled    []DisabledUser `json:"disabled,omitempty"`
}

// DisabledUser is one row in RunMeta.Disabled.
type DisabledUser struct {
	User        string    `json:"user"`
	Kind        string    `json:"kind"` // "upload" or "download"
	At          time.Time `json:"at"`
	Consecutive int64     `json:"consecutive"`
	TotalFailed int64     `json:"total_failed"`
	LastCode    string    `json:"last_code"`
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
