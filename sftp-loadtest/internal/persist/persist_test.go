package persist

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteAndListMeta_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().Truncate(time.Microsecond) // JSON serialisation drops sub-µs
	want := RunMeta{
		ID:             "run-42",
		StartedAt:      now,
		StoppedAt:      now.Add(30 * time.Second),
		TotalFiles:     17,
		TotalBytes:     17 * 1024 * 1024,
		OverallMBps:    9.5,
		FailedFiles:    1,
		SucceededFiles: 16,
		DispatchSkips:  3,
		Suggestions: []Suggestion{
			{Severity: "warn", Title: "test", Detail: "d", Action: "a"},
		},
	}
	if err := WriteMeta(dir, want); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}
	got, err := ListMeta(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("ListMeta returned %d entries, want 1", len(got))
	}
	g := got[0]
	if g.ID != want.ID || g.TotalFiles != want.TotalFiles || g.DispatchSkips != want.DispatchSkips {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", g, want)
	}
	if len(g.Suggestions) != 1 || g.Suggestions[0].Severity != "warn" {
		t.Errorf("suggestions did not round-trip: %+v", g.Suggestions)
	}
}

func TestListMeta_NewestFirst(t *testing.T) {
	dir := t.TempDir()
	older := RunMeta{ID: "older", StartedAt: time.Now().Add(-2 * time.Hour)}
	newer := RunMeta{ID: "newer", StartedAt: time.Now()}
	for _, m := range []RunMeta{older, newer} {
		if err := WriteMeta(dir, m); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ListMeta(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].ID != "newer" {
		t.Errorf("ListMeta order wrong: got %s first, want 'newer'", got[0].ID)
	}
}

func TestListMeta_IgnoresTmpAndNonJSON(t *testing.T) {
	dir := t.TempDir()
	// A stray .tmp from an interrupted atomic write must not surface as a run.
	_ = os.WriteFile(filepath.Join(dir, "junk.json.tmp"), []byte(`{"id":"junk"}`), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hi"), 0o600)
	if err := WriteMeta(dir, RunMeta{ID: "real", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	got, _ := ListMeta(dir)
	if len(got) != 1 || got[0].ID != "real" {
		t.Errorf("ListMeta did not filter cruft: %+v", got)
	}
}

func TestRecoverInterrupted_BuildsMetaFromCSV(t *testing.T) {
	dir := t.TempDir()
	csv := strings.Join([]string{
		"user,kind,filename,start_time,end_time,duration_sec,size_bytes,expected_bytes,incomplete,upload_mbps,upload_mbps_source,track_id,track_id_detected_at,track_id_wait_sec,processing_time_min,in_slowdown_minute,error,error_code,download_user,download_available_at,download_start,download_end,download_wait_sec,download_duration_sec,download_size_bytes,download_mbps,download_mbps_source,download_error",
		"u1,normal,a.txt,2026-04-28T10:00:00Z,2026-04-28T10:00:01Z,1,1024,1024,false,1.0,per_file,t1,2026-04-28T10:00:01Z,0,0,false,,,,,,,,,,,,",
		"u1,normal,b.txt,2026-04-28T10:00:01Z,2026-04-28T10:00:02Z,1,2048,2048,false,2.0,per_file,t2,2026-04-28T10:00:02Z,0,0,false,oops,WRITE,,,,,,,,,,",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "run-zombie.csv"), []byte(csv), 0o600); err != nil {
		t.Fatal(err)
	}
	rec, err := RecoverInterrupted(dir)
	if err != nil {
		t.Fatalf("RecoverInterrupted: %v", err)
	}
	if len(rec) != 1 || rec[0] != "run-zombie" {
		t.Errorf("expected to recover run-zombie, got %v", rec)
	}
	got, _ := ListMeta(dir)
	if len(got) != 1 {
		t.Fatalf("expected 1 meta after recovery, got %d", len(got))
	}
	m := got[0]
	if !m.Interrupted {
		t.Error("recovered meta must be tagged Interrupted=true")
	}
	if m.TotalFiles != 2 || m.FailedFiles != 1 || m.SucceededFiles != 1 {
		t.Errorf("count synthesis wrong: total=%d failed=%d ok=%d", m.TotalFiles, m.FailedFiles, m.SucceededFiles)
	}
	if m.TotalBytes != 1024+2048 {
		t.Errorf("byte sum wrong: %d", m.TotalBytes)
	}
}

func TestRecoverInterrupted_SkipsCSVsWithExistingMeta(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "run-1.csv"), []byte("user\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteMeta(dir, RunMeta{ID: "run-1", StartedAt: time.Now(), TotalFiles: 1}); err != nil {
		t.Fatal(err)
	}
	rec, err := RecoverInterrupted(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec) != 0 {
		t.Errorf("recovery must skip CSVs that already have a meta JSON; got %v", rec)
	}
}

func TestRecoverInterrupted_StopsAtAnalysisTrailer(t *testing.T) {
	// A run with the new "# RUN ANALYSIS" trailer must still be recoverable —
	// the trailer marker should stop counting cleanly.
	dir := t.TempDir()
	csv := strings.Join([]string{
		"user,kind,filename,start_time,end_time,duration_sec,size_bytes,expected_bytes,incomplete,upload_mbps,upload_mbps_source,track_id,track_id_detected_at,track_id_wait_sec,processing_time_min,in_slowdown_minute,error,error_code,download_user,download_available_at,download_start,download_end,download_wait_sec,download_duration_sec,download_size_bytes,download_mbps,download_mbps_source,download_error",
		"u1,normal,a.txt,2026-04-28T10:00:00Z,2026-04-28T10:00:01Z,1,500,500,false,1,per_file,t,2026-04-28T10:00:01Z,0,0,false,,,,,,,,,,,,",
		"",
		"# RUN ANALYSIS",
		"# total_files,1",
	}, "\n") + "\n"
	_ = os.WriteFile(filepath.Join(dir, "run-with-trailer.csv"), []byte(csv), 0o600)
	rec, err := RecoverInterrupted(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec) != 1 {
		t.Fatalf("expected recovery, got %v", rec)
	}
	got, _ := ListMeta(dir)
	if got[0].TotalFiles != 1 {
		t.Errorf("trailer rows leaked into the count: %d", got[0].TotalFiles)
	}
}
