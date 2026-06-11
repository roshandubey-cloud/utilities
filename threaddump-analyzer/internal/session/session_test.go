package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roshandubey-cloud/utilities/threaddump-analyzer/internal/parser"
)

func loadSample(t *testing.T, name string) *parser.Dump {
	t.Helper()
	wd, _ := os.Getwd()
	var p string
	for i := 0; i < 5; i++ {
		c := filepath.Join(wd, "examples", name)
		if _, err := os.Stat(c); err == nil {
			p = c
			break
		}
		wd = filepath.Dir(wd)
	}
	if p == "" {
		t.Fatalf("sample %s not found", name)
	}
	data, _ := os.ReadFile(p)
	d, err := parser.ParseAuto(strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return d
}

func TestFrozenThreadsAcrossDuplicates(t *testing.T) {
	d1 := loadSample(t, "hang.jstack")
	d2 := loadSample(t, "hang.jstack")
	s := New("test", "test")
	s.AddDump(d1)
	s.AddDump(d2)
	frozen := s.FrozenThreads(2, 8)
	if len(frozen) < 5 {
		t.Errorf("expected >=5 frozen tomcat threads, got %d", len(frozen))
	}
}

func TestLockProgressionStableHolder(t *testing.T) {
	d1 := loadSample(t, "deadlock.jstack")
	d2 := loadSample(t, "deadlock.jstack")
	s := New("t", "t")
	s.AddDump(d1)
	s.AddDump(d2)
	prog := s.LockProgressions()
	if len(prog) == 0 {
		t.Fatal("expected at least one lock progression")
	}
	// At least one lock should appear with peak waiters > 0.
	max := 0
	for _, p := range prog {
		if p.PeakWaiters > max {
			max = p.PeakWaiters
		}
	}
	if max < 4 {
		t.Errorf("expected peak waiters >=4 (Hikari condition), got %d", max)
	}
}

func TestPersistRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	s := New("xyz123", "round-trip")
	s.AddDump(loadSample(t, "hang.jstack"))
	if err := s.Save(tmp); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := LoadAll(tmp)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 loaded session, got %d", len(loaded))
	}
	if loaded[0].Count() != 1 {
		t.Errorf("expected 1 dump in loaded session, got %d", loaded[0].Count())
	}
	if loaded[0].Label != "round-trip" {
		t.Errorf("label round-trip lost: %q", loaded[0].Label)
	}
}
