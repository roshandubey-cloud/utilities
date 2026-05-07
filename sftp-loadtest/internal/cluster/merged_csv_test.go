package cluster

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteMergedCSV_InterleavesWorkersWithLabel pins the v0.19.22
// single-report contract:
//   - Every per-worker row appears once in the merged file.
//   - First column is the worker label "worker-NN (URL)" so an
//     operator can tell at a glance which node ran each row.
//   - Remaining columns mirror the per-worker CSV header verbatim
//     (no schema drift between worker files and merged).
//   - Rows are sorted by start_time across workers so reading the
//     file gives a chronological cluster timeline.
func TestWriteMergedCSV_InterleavesWorkersWithLabel(t *testing.T) {
	dir := t.TempDir()

	// Two synthetic worker CSVs — identical header, two rows each,
	// timestamps interleaved on purpose.
	header := "user,kind,filename,filename_id,start_time,end_time,duration_sec,size_bytes,expected_bytes,incomplete,upload_mbps,upload_mbps_source,track_id,track_id_detected_at,track_id_wait_sec,processing_time_min,in_slowdown_minute,error,error_code,download_user,download_available_at,download_start,download_end,download_wait_sec,download_duration_sec,download_size_bytes,download_mbps,download_mbps_source,download_error,upload_sha256,download_sha256,hash_match\n"
	w1 := header +
		"u1,normal,a.txt,m1,2026-05-07T10:00:00Z,2026-05-07T10:00:01Z,1,1024,1024,false,1,per_file,FILENAME:m1,2026-05-07T10:00:01Z,0,0,false,,,,,,,0,0,0,0,window_rate,,h1,h1,true\n" +
		"u1,normal,c.txt,m3,2026-05-07T10:00:04Z,2026-05-07T10:00:05Z,1,1024,1024,false,1,per_file,FILENAME:m3,2026-05-07T10:00:05Z,0,0,false,,,,,,,0,0,0,0,window_rate,,h3,h3,true\n"
	w2 := header +
		"u2,large,b.bin,m2,2026-05-07T10:00:02Z,2026-05-07T10:00:03Z,1,1048576,1048576,false,5,per_file,FILENAME:m2,2026-05-07T10:00:03Z,0,0,false,,,,,,,0,0,0,0,window_rate,,h2,h2,true\n" +
		"u2,large,d.bin,m4,2026-05-07T10:00:06Z,2026-05-07T10:00:07Z,1,1048576,1048576,false,5,per_file,FILENAME:m4,2026-05-07T10:00:07Z,0,0,false,,,,,,,0,0,0,0,window_rate,,h4,h4,true\n"

	if err := os.WriteFile(filepath.Join(dir, "worker-01.csv"), []byte(w1), 0o600); err != nil {
		t.Fatalf("write w1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "worker-02.csv"), []byte(w2), 0o600); err != nil {
		t.Fatalf("write w2: %v", err)
	}
	workers := []ClusterWorkerReport{
		{URL: "http://w1:8080", FileCSV: "worker-01.csv"},
		{URL: "http://w2:8080", FileCSV: "worker-02.csv"},
	}

	n, err := writeMergedCSV(dir, workers)
	if err != nil {
		t.Fatalf("writeMergedCSV: %v", err)
	}
	if n != 4 {
		t.Fatalf("expected 4 data rows merged, got %d", n)
	}

	// Read and assert the merged file.
	f, err := os.Open(filepath.Join(dir, "merged.csv"))
	if err != nil {
		t.Fatalf("open merged: %v", err)
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatalf("read merged: %v", err)
	}
	if len(rows) != 5 { // 1 header + 4 data
		t.Fatalf("expected 5 rows (header + 4), got %d", len(rows))
	}
	hdr := rows[0]
	if hdr[0] != "worker" {
		t.Errorf("first column must be 'worker', got %q", hdr[0])
	}
	if hdr[1] != "user" || hdr[5] != "start_time" {
		t.Errorf("header columns drifted: %v", hdr[:6])
	}

	// Chronological order: m1, m2, m3, m4.
	wantOrder := []string{"a.txt", "b.bin", "c.txt", "d.bin"}
	for i, want := range wantOrder {
		got := rows[i+1][3] // filename column shifted by +1 because of `worker`
		if got != want {
			t.Errorf("row %d filename: got %q want %q", i, got, want)
		}
	}
	// Worker labels: rows 1,3 → worker-01; rows 2,4 → worker-02.
	for i, wantLabel := range []string{"worker-01", "worker-02", "worker-01", "worker-02"} {
		got := rows[i+1][0]
		if !strings.HasPrefix(got, wantLabel) {
			t.Errorf("row %d worker label: got %q want prefix %q", i, got, wantLabel)
		}
	}
}

// TestWriteMergedCSV_EmptyWhenNoWorkerFiles pins the no-op path:
// archival happens during a partial outage where every worker CSV
// fetch failed. writeMergedCSV must return (0, nil) and NOT create
// merged.csv — the meta then surfaces no MergedCSV pointer to the UI
// instead of an empty file the operator might mistake for "no data".
func TestWriteMergedCSV_EmptyWhenNoWorkerFiles(t *testing.T) {
	dir := t.TempDir()
	workers := []ClusterWorkerReport{
		{URL: "http://w1:8080" /* FileCSV intentionally empty */},
		{URL: "http://w2:8080"},
	}
	n, err := writeMergedCSV(dir, workers)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 rows when no per-worker CSVs present, got %d", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "merged.csv")); !os.IsNotExist(err) {
		t.Errorf("merged.csv should not be created when there are no rows; stat err=%v", err)
	}
}
