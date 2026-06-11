package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/roshandubey-cloud/utilities/threaddump-analyzer/internal/parser"
)

// On-disk shape. Sessions live under <dataDir>/sessions/<id>/. Each session
// directory contains:
//   meta.json            — id, label, created_at, dump filenames in order
//   dump-NN.txt          — raw uploaded dump text, exactly as received
//   gclog.txt (optional) — uploaded GC log
//   cpu.txt   (optional) — uploaded CPU sample
//
// We keep the raw uploads, not parsed JSON, so future analyzer changes can
// re-derive everything without losing fidelity. The cost is a bit more disk;
// the gain is forward compatibility for an audit-grade tool.

type sessionMeta struct {
	ID         string    `json:"id"`
	Label      string    `json:"label"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	DumpFiles  []string  `json:"dump_files"`
	GCLogFile  string    `json:"gc_log_file,omitempty"`
	CPUFile    string    `json:"cpu_file,omitempty"`
}

// Save persists the session to dataDir. Idempotent — overwrites the meta.json
// and writes any not-yet-written dump files. The directory is created with
// 0o700 (owner-only) since dumps can contain sensitive stack traces with
// path / hostname / parameter detail.
func (s *Session) Save(dataDir string) error {
	if dataDir == "" {
		return nil
	}
	dir := filepath.Join(dataDir, "sessions", sanitiseID(s.ID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	meta := sessionMeta{
		ID:        s.ID,
		Label:     s.Label,
		UpdatedAt: time.Now().UTC(),
	}
	// Created at first save only.
	if existing, err := readMeta(dir); err == nil && !existing.CreatedAt.IsZero() {
		meta.CreatedAt = existing.CreatedAt
	} else {
		meta.CreatedAt = meta.UpdatedAt
	}
	for i, d := range s.dumps {
		name := fmt.Sprintf("dump-%02d.txt", i)
		fpath := filepath.Join(dir, name)
		// Write only if missing — dump content never mutates after upload.
		if _, err := os.Stat(fpath); errors.Is(err, os.ErrNotExist) {
			if err := os.WriteFile(fpath, []byte(d.Raw), 0o600); err != nil {
				return fmt.Errorf("write %s: %w", fpath, err)
			}
		}
		meta.DumpFiles = append(meta.DumpFiles, name)
	}
	return writeMetaAtomic(dir, meta)
}

// SaveAuxiliary writes the GC log + CPU sample raw text. Either may be empty;
// non-empty content is written and recorded in meta.json.
func (s *Session) SaveAuxiliary(dataDir, gcLogText, cpuText string) error {
	if dataDir == "" {
		return nil
	}
	dir := filepath.Join(dataDir, "sessions", sanitiseID(s.ID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	meta, err := readMeta(dir)
	if err != nil {
		meta = sessionMeta{ID: s.ID, Label: s.Label}
	}
	if gcLogText != "" {
		fpath := filepath.Join(dir, "gclog.txt")
		if err := os.WriteFile(fpath, []byte(gcLogText), 0o600); err != nil {
			return err
		}
		meta.GCLogFile = "gclog.txt"
	}
	if cpuText != "" {
		fpath := filepath.Join(dir, "cpu.txt")
		if err := os.WriteFile(fpath, []byte(cpuText), 0o600); err != nil {
			return err
		}
		meta.CPUFile = "cpu.txt"
	}
	meta.UpdatedAt = time.Now().UTC()
	return writeMetaAtomic(dir, meta)
}

// LoadAll scans dataDir/sessions/*/meta.json and rehydrates every session,
// re-parsing each dump from its on-disk file. Returns the loaded sessions
// in arbitrary order — caller is expected to index them by ID.
func LoadAll(dataDir string) ([]*Session, error) {
	root := filepath.Join(dataDir, "sessions")
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := []*Session{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		meta, err := readMeta(dir)
		if err != nil {
			continue // skip malformed; don't block startup
		}
		s := New(meta.ID, meta.Label)
		names := append([]string(nil), meta.DumpFiles...)
		sort.Strings(names) // dump-NN.txt sorts chronologically already
		for _, n := range names {
			data, err := os.ReadFile(filepath.Join(dir, n))
			if err != nil {
				continue
			}
			d, err := parser.ParseAuto(strings.NewReader(string(data)))
			if err != nil {
				continue
			}
			s.AddDump(d)
		}
		out = append(out, s)
	}
	return out, nil
}

func readMeta(dir string) (sessionMeta, error) {
	data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return sessionMeta{}, err
	}
	var m sessionMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return sessionMeta{}, err
	}
	return m, nil
}

func writeMetaAtomic(dir string, m sessionMeta) error {
	tmp := filepath.Join(dir, "meta.json.tmp")
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "meta.json"))
}

// sanitiseID restricts the session id to filename-safe characters. The
// generator already only emits hex, but tighten anyway so an external API
// caller can't traverse paths by injecting "../".
func sanitiseID(s string) string {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '-', c == '_':
			b = append(b, c)
		default:
			b = append(b, '_')
		}
	}
	return string(b)
}
