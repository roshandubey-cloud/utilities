package sink

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDiscard_AlwaysOK pins the default behaviour: Discard is the
// pre-v0.14 path — never errors, all bytes go to /dev/null. The runner
// relies on this so download throughput tests don't need a writable
// disk path.
func TestDiscard_AlwaysOK(t *testing.T) {
	w, err := Discard{}.Open(Request{})
	if err != nil {
		t.Fatalf("Discard.Open: %v", err)
	}
	if _, err := w.Write([]byte("hello")); err != nil {
		t.Errorf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// TestLocalDisk_TemplateRendersUserAndFilename pins the default template
// "{user}/{filename}" — the operator's most common case. The downloaded
// bytes land at <root>/<user>/<filename> with mode 0600.
func TestLocalDisk_TemplateRendersUserAndFilename(t *testing.T) {
	root := t.TempDir()
	s, err := NewLocalDisk(root, "{user}/{filename}", false)
	if err != nil {
		t.Fatalf("NewLocalDisk: %v", err)
	}
	w, err := s.Open(Request{User: "alice", Filename: "invoice.pdf"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := w.Write([]byte("PDF")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	w.Close()
	got := filepath.Join(root, "alice", "invoice.pdf")
	if _, err := os.Stat(got); err != nil {
		t.Fatalf("expected file at %s: %v", got, err)
	}
	body, _ := os.ReadFile(got)
	if string(body) != "PDF" {
		t.Errorf("body=%q want PDF", body)
	}
}

// TestLocalDisk_DefaultTemplateIncludesRunID pins the v0.17.1 default
// template upgrade: passing template="" now yields
// "{run_id}/{user}/{filename}" so two concurrent runs writing to the
// same Root land in disjoint folders. Single-run callers see one extra
// directory level (acceptable cost for default-safe concurrency).
func TestLocalDisk_DefaultTemplateIncludesRunID(t *testing.T) {
	root := t.TempDir()
	s, err := NewLocalDisk(root, "", false) // empty -> default
	if err != nil {
		t.Fatalf("NewLocalDisk: %v", err)
	}
	if !strings.Contains(s.Template, "{run_id}") {
		t.Fatalf("default template lacks {run_id}: %q", s.Template)
	}
	w, err := s.Open(Request{RunID: "run-42-1", User: "alice", Filename: "x.txt"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	w.Write([]byte("ok"))
	w.Close()
	got := filepath.Join(root, "run-42-1", "alice", "x.txt")
	if _, err := os.Stat(got); err != nil {
		t.Fatalf("expected file at %s: %v", got, err)
	}
}

// TestLocalDisk_TemplateAllVariables exercises every supported
// substitution including the {date}/{datetime} clock-derived ones.
func TestLocalDisk_TemplateAllVariables(t *testing.T) {
	root := t.TempDir()
	s, err := NewLocalDisk(root, "{date}/{run_id}/{user}/{trackid}_{basename}{ext}", false)
	if err != nil {
		t.Fatalf("NewLocalDisk: %v", err)
	}
	when := time.Date(2026, 5, 1, 18, 30, 0, 0, time.UTC)
	w, err := s.Open(Request{
		User: "bob", Filename: "ack.xml",
		TrackID: "tid42", RunID: "run-X", StartedAt: when,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	w.Close()
	want := filepath.Join(root, "2026-05-01", "run-X", "bob", "tid42_ack.xml")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected file at %s: %v", want, err)
	}
}

// TestLocalDisk_RejectsTraversal pins the security guard: a template
// variable containing ".." or "/" is sanitised to underscore so an
// operator (or a hostile remote filename) can never punch out of root.
func TestLocalDisk_RejectsTraversal(t *testing.T) {
	root := t.TempDir()
	s, err := NewLocalDisk(root, "{user}/{filename}", false)
	if err != nil {
		t.Fatalf("NewLocalDisk: %v", err)
	}
	w, err := s.Open(Request{User: "../escape", Filename: "../../etc/passwd"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	w.Close()
	// The file must exist somewhere under root — both ".." segments
	// were sanitised to underscores so it cannot escape.
	walked := false
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		walked = true
		rel, _ := filepath.Rel(root, p)
		if strings.HasPrefix(rel, "..") {
			t.Errorf("file landed at %q (escapes root)", p)
		}
		return nil
	})
	if !walked {
		t.Error("expected a file to land somewhere under root")
	}
}

// TestLocalDisk_OverwriteFalseRefusesExisting pins the safety default:
// when Overwrite=false, opening a path that already exists returns an
// error so a re-run doesn't silently clobber the prior pull.
func TestLocalDisk_OverwriteFalseRefusesExisting(t *testing.T) {
	root := t.TempDir()
	s, err := NewLocalDisk(root, "{user}/{filename}", false)
	if err != nil {
		t.Fatalf("NewLocalDisk: %v", err)
	}
	req := Request{User: "u", Filename: "f.txt"}
	w, _ := s.Open(req)
	w.Close()
	if _, err := s.Open(req); err == nil {
		t.Error("expected second Open to fail with Overwrite=false")
	}
	// With Overwrite=true the second Open succeeds.
	s2, _ := NewLocalDisk(root, "{user}/{filename}", true)
	w2, err := s2.Open(req)
	if err != nil {
		t.Fatalf("Overwrite=true: %v", err)
	}
	w2.Close()
}
