package gclog

import (
	"os"
	"path/filepath"
	"testing"
)

func resolveSample(t *testing.T) string {
	t.Helper()
	wd, _ := os.Getwd()
	for i := 0; i < 5; i++ {
		p := filepath.Join(wd, "examples", "sample.gclog")
		if _, err := os.Stat(p); err == nil {
			return p
		}
		wd = filepath.Dir(wd)
	}
	t.Fatalf("sample.gclog not found")
	return ""
}

func TestParseUnified(t *testing.T) {
	f, err := os.Open(resolveSample(t))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	l, err := Parse(f)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(l.Pauses) != 5 {
		t.Fatalf("expected 5 pauses, got %d", len(l.Pauses))
	}
	s := l.Stats()
	if s.FullCount != 1 {
		t.Errorf("FullCount=%d, want 1", s.FullCount)
	}
	if s.MaxDuration.Milliseconds() < 1800 {
		t.Errorf("MaxDuration=%v, want >=1800ms", s.MaxDuration)
	}
}
