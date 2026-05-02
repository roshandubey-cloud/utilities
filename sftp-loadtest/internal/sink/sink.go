// Package sink provides pluggable destinations for the download phase.
//
// Until v0.14.0 every download was drained into io.Discard — the test
// only cared about throughput, not the bytes themselves. Real-world QA
// needs the bytes on disk: customers want to diff downloaded files
// against an expected fixture, archive the day's pulls for audit, or
// route per-tenant files into per-tenant folders for downstream
// pipelines. This package adds that flexibility without breaking the
// throughput-only default.
//
// Two implementations:
//
//   * Discard   — io.Discard wrapped as a WriteCloser. Default,
//                 zero-cost path. Drop-in for the pre-v0.14 behaviour.
//   * LocalDisk — writes to <root>/<rendered template>. The template
//                 supports per-user, per-trackid, per-date partitioning
//                 so an operator can shape the on-disk layout to match
//                 their post-test analysis pipeline.
//
// Template variables (case-insensitive, both {user} and ${user} accepted):
//
//   {user}      — download user (the CSV row that pulled this file)
//   {filename}  — full original basename including extension
//   {basename}  — filename without extension
//   {ext}       — extension WITH leading dot (".pdf"), empty if none
//   {trackid}   — track ID (filename mode = the marker; trackid mode = the id)
//   {run_id}    — current run id (run-<unix-ts>)
//   {date}      — YYYY-MM-DD at the moment the download landed
//   {datetime}  — YYYY-MM-DD_HH-MM-SS at the moment the download landed
//
// Path-component sanitisation: rendered values that contain "/" or
// ".." are escaped to underscore — operator-supplied templates can't
// punch out of <root>.

package sink

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Request is the context the sink needs to render a destination path
// for one downloaded file.
type Request struct {
	User      string
	Filename  string    // original basename as it landed in the outbox
	TrackID   string    // upload-side track-id (empty in pure download-orphan paths)
	RunID     string    // run-<unix-ts>
	StartedAt time.Time // when the download started (used by {datetime})
}

// FileSink is the contract every download destination implements.
// Implementations must be safe for concurrent calls.
type FileSink interface {
	// Open returns a writer for the given request. The runner will
	// io.Copy the download body into it and call Close exactly once.
	// Open may write through to /dev/null (Discard) or open a real
	// file (LocalDisk).
	Open(req Request) (io.WriteCloser, error)
}

// Discard is the default sink — writes go to io.Discard, byte counts
// are still tracked by the runner via a counting wrapper.
type Discard struct{}

// nopWriteCloser wraps io.Discard. Every Open returns the SAME instance
// because io.Discard is stateless — callers can Write/Close
// independently without contention.
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func (Discard) Open(_ Request) (io.WriteCloser, error) {
	return nopWriteCloser{Writer: io.Discard}, nil
}

// LocalDisk writes downloads to disk under Root, organised by
// Template. The default template ("{user}/{filename}") gives one
// folder per download user with original filenames preserved.
type LocalDisk struct {
	Root      string // base directory; created (0700) if it doesn't exist
	Template  string // path template, see package comment for variables
	Overwrite bool   // when false, an existing target file errors instead of being clobbered
}

// NewLocalDisk validates Root + Template and returns a ready-to-use
// sink. Mkdir's Root if missing.
func NewLocalDisk(root, template string, overwrite bool) (*LocalDisk, error) {
	if root == "" {
		return nil, errors.New("local-disk: root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("local-disk: resolve %q: %w", root, err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, fmt.Errorf("local-disk: mkdir %q: %w", abs, err)
	}
	if template == "" {
		template = "{user}/{filename}"
	}
	return &LocalDisk{
		Root:      abs,
		Template:  template,
		Overwrite: overwrite,
	}, nil
}

func (l *LocalDisk) Open(req Request) (io.WriteCloser, error) {
	rel := renderTemplate(l.Template, req)
	if rel == "" {
		return nil, errors.New("local-disk: template rendered to empty path")
	}
	full := filepath.Join(l.Root, rel)
	// Defence-in-depth against template variables that contain ".."
	// or absolute-path tricks: the rendered path must stay under Root.
	rel2, err := filepath.Rel(l.Root, full)
	if err != nil || strings.HasPrefix(rel2, "..") || filepath.IsAbs(rel2) {
		return nil, fmt.Errorf("local-disk: rendered path %q escapes root %q", rel, l.Root)
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return nil, fmt.Errorf("local-disk: mkdir %q: %w", filepath.Dir(full), err)
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if !l.Overwrite {
		flags = os.O_WRONLY | os.O_CREATE | os.O_EXCL
	}
	f, err := os.OpenFile(full, flags, 0o600)
	if err != nil {
		return nil, fmt.Errorf("local-disk: open %q: %w", full, err)
	}
	return f, nil
}

// renderTemplate substitutes {var} placeholders. Both {var} and ${var}
// shapes are accepted because operators copy-paste from shell snippets
// and we don't want a $ to break the substitution.
func renderTemplate(tpl string, req Request) string {
	if tpl == "" {
		return ""
	}
	t := req.StartedAt
	if t.IsZero() {
		t = time.Now()
	}
	ext := filepath.Ext(req.Filename)
	base := strings.TrimSuffix(req.Filename, ext)
	repl := map[string]string{
		"user":     sanitisePathComponent(req.User),
		"filename": sanitisePathComponent(req.Filename),
		"basename": sanitisePathComponent(base),
		"ext":      sanitisePathComponent(ext),
		"trackid":  sanitisePathComponent(req.TrackID),
		"run_id":   sanitisePathComponent(req.RunID),
		"date":     t.Format("2006-01-02"),
		"datetime": t.Format("2006-01-02_15-04-05"),
	}
	out := tpl
	for k, v := range repl {
		out = strings.ReplaceAll(out, "{"+k+"}", v)
		out = strings.ReplaceAll(out, "${"+k+"}", v)
	}
	return out
}

// sanitisePathComponent replaces directory separators and traversal
// characters with underscore so a template variable can never punch
// out of Root. Empty input returns "_" so a missing trackid still
// produces a usable path component.
func sanitisePathComponent(s string) string {
	if s == "" {
		return "_"
	}
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch r {
		case '/', '\\', 0:
			out = append(out, '_')
		default:
			out = append(out, r)
		}
	}
	cleaned := string(out)
	for strings.Contains(cleaned, "..") {
		cleaned = strings.ReplaceAll(cleaned, "..", "__")
	}
	return cleaned
}
