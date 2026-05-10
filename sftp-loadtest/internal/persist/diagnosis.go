package persist

// Run Doctor diagnoses persist on disk per-run as line-delimited
// JSON (`<run-id>.diagnoses.jsonl`) so the panel can restore the
// conversation history when the operator reopens it later — and so
// follow-up questions can be threaded under their parent without
// the UI keeping state.
//
// Storage shape:
//   * One file per run.  No global index — listings are per-run, so
//     scanning a single small file is enough.  Empty file == no
//     diagnoses yet (Append creates it on first write).
//   * Append-only, line-delimited JSON.  Avoids re-encoding the whole
//     history on every save and survives partial writes (a corrupted
//     trailing line is dropped on read; everything before it is
//     intact).  The atomic-rename pattern used for RunMeta would
//     either rewrite the whole file each turn (expensive on
//     long conversations) or require a second on-disk index — the
//     append-only file is simpler and good enough for ≤O(100)
//     diagnoses per run.
//   * Mode 0o600, directory 0o700 — same posture as RunMeta.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Diagnosis is one Run Doctor turn — either an initial analysis or a
// follow-up Q&A under a parent diagnosis.
type Diagnosis struct {
	ID            string    `json:"id"`             // sortable; "diag-<unix-nanos>"
	RunID         string    `json:"run_id"`         // focal run this diagnosis is about
	GeneratedAt   time.Time `json:"generated_at"`
	Provider      string    `json:"provider"`       // currently "anthropic"
	Model         string    `json:"model"`          // exact model id used
	Redacted      bool      `json:"redacted"`       // whether the prompt was redacted
	BaselineIDs   []string  `json:"baseline_ids"`   // run ids included as comparison baselines
	Mode          string    `json:"mode"`           // "auto" | "all" | "pick"
	Question      string    `json:"question,omitempty"`             // operator's follow-up question (empty for the first turn)
	ParentID      string    `json:"parent_id,omitempty"`            // diagnosis this one follows; empty for the root turn
	Narrative     string    `json:"narrative"`                       // the model's response text
	PromptChars   int       `json:"prompt_chars,omitempty"`          // size of system+user prompt (for cost-on-receipt comparison)
	ResponseChars int       `json:"response_chars,omitempty"`        // size of the model's response
	ElapsedMs     int64     `json:"elapsed_ms,omitempty"`            // wall time the AI call took
	EstUSD        float64   `json:"est_usd,omitempty"`               // estimated cost; 0 if not computed
}

// DiagnosisPath returns the on-disk path for a run's diagnosis log.
// Mirrors persist.MetaPath's naming convention so the file sits
// next to the run's CSV / JSON in the reports directory.
func DiagnosisPath(dir, runID string) string {
	return filepath.Join(dir, sanitize(runID)+".diagnoses.jsonl")
}

// AppendDiagnosis writes one diagnosis to the end of the per-run
// log.  Creates the file (and directory) on first write.
func AppendDiagnosis(dir string, d Diagnosis) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir reports dir: %w", err)
	}
	if d.ID == "" {
		d.ID = fmt.Sprintf("diag-%d", time.Now().UnixNano())
	}
	if d.GeneratedAt.IsZero() {
		d.GeneratedAt = time.Now().UTC()
	}
	enc, err := json.Marshal(d)
	if err != nil {
		return fmt.Errorf("marshal diagnosis: %w", err)
	}
	enc = append(enc, '\n')

	f, err := os.OpenFile(DiagnosisPath(dir, d.RunID),
		os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open diagnosis log: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(enc); err != nil {
		return fmt.Errorf("write diagnosis: %w", err)
	}
	return f.Sync()
}

// ListDiagnoses returns every diagnosis recorded for a run, in
// chronological (append) order.  Returns an empty slice and no
// error when the file doesn't exist yet (zero-diagnoses run is
// the common case).  Corrupted lines are dropped silently;
// preserving a partial history beats erroring out on one bad write.
func ListDiagnoses(dir, runID string) ([]Diagnosis, error) {
	f, err := os.Open(DiagnosisPath(dir, runID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []Diagnosis
	scanner := bufio.NewScanner(f)
	// Some narratives can run a few KB; lift the default 64 KB line
	// cap so we don't truncate on a verbose response.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var d Diagnosis
		if err := json.Unmarshal([]byte(line), &d); err != nil {
			continue // skip corrupt line
		}
		if d.RunID != "" && d.RunID != runID {
			continue // belt-and-braces: the file is per-run already
		}
		out = append(out, d)
	}
	return out, scanner.Err()
}

// FindDiagnosis pulls one diagnosis by ID from a run's log.  Used
// when serving a follow-up so the server can attach the parent
// turn's narrative as conversational context.
func FindDiagnosis(dir, runID, diagID string) (Diagnosis, bool, error) {
	all, err := ListDiagnoses(dir, runID)
	if err != nil {
		return Diagnosis{}, false, err
	}
	for _, d := range all {
		if d.ID == diagID {
			return d, true, nil
		}
	}
	return Diagnosis{}, false, nil
}
