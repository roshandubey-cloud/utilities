package cpu

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseTop(t *testing.T) {
	wd, _ := os.Getwd()
	var path string
	for i := 0; i < 5; i++ {
		p := filepath.Join(wd, "examples", "sample.top")
		if _, err := os.Stat(p); err == nil {
			path = p
			break
		}
		wd = filepath.Dir(wd)
	}
	if path == "" {
		t.Fatal("sample.top not found")
	}
	f, _ := os.Open(path)
	defer f.Close()
	s, err := Parse(f)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(s.Threads) != 6 {
		t.Fatalf("expected 6 threads, got %d", len(s.Threads))
	}
	if s.Threads[0].Percent != 88.0 {
		t.Errorf("Threads[0].Percent=%v, want 88", s.Threads[0].Percent)
	}
	if s.Threads[0].NID != "47105" {
		t.Errorf("Threads[0].NID=%q", s.Threads[0].NID)
	}
}

func TestJoinByNIDHexToDec(t *testing.T) {
	s := &Sample{
		Threads: []ThreadCPU{
			{NID: "47105", Percent: 88}, {NID: "47106", Percent: 42.5},
		},
	}
	pairs := []struct{ Name, NID string }{
		{"hot-thread", "0xb801"},  // 47105 in decimal
		{"warm-thread", "0xb802"}, // 47106 in decimal
		{"cold-thread", "0xb800"}, // not in sample
	}
	out := s.JoinByNID(pairs)
	if len(out) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(out))
	}
	if out[0].Name != "hot-thread" {
		t.Errorf("sort order broken: %+v", out)
	}
}
