package report

import (
	"testing"
	"time"
)

func TestStore_AttachDownloadByFilenameID_RoundTrip(t *testing.T) {
	s := NewStore()
	now := time.Now()
	s.AddUpload(FileRecord{
		User:       "u1",
		Filename:   "invoice123_slt_abc123xyz789_.csv",
		FilenameID: "abc123xyz789",
		StartTime:  now,
		EndTime:    now.Add(50 * time.Millisecond),
		SizeBytes:  1024,
		TrackID:    "FILENAME:abc123xyz789",
	})
	ok := s.AttachDownloadByFilenameID("abc123xyz789", DownloadResult{
		DownloadUser: "dl1",
		StartTime:    now.Add(time.Second),
		EndTime:      now.Add(time.Second + 30*time.Millisecond),
		SizeBytes:    1024,
		AvailableAt:  now.Add(900 * time.Millisecond),
	})
	if !ok {
		t.Fatal("AttachDownloadByFilenameID should match a known marker")
	}
	got := s.Snapshot()
	if len(got) != 1 || got[0].DownloadUser != "dl1" {
		t.Errorf("attach did not write through: %+v", got)
	}
	if got[0].DownloadSizeBytes != 1024 {
		t.Errorf("download size lost: %d", got[0].DownloadSizeBytes)
	}
	// Wait time = StartTime - AvailableAt = 100ms.
	if d := got[0].DownloadWait; d != 100*time.Millisecond {
		t.Errorf("download_wait wrong: %s", d)
	}
}

func TestStore_AttachDownloadByFilenameID_OrphanReturnsFalse(t *testing.T) {
	s := NewStore()
	s.AddUpload(FileRecord{Filename: "a", FilenameID: "id-a"})
	if s.AttachDownloadByFilenameID("nope", DownloadResult{}) {
		t.Error("attach should return false on unknown marker")
	}
}

func TestStore_AddUpload_NoFilenameID_IsExcludedFromIndex(t *testing.T) {
	// In trackid mode FilenameID is empty; the marker index must not
	// be polluted by an empty key (which would alias multiple uploads).
	s := NewStore()
	s.AddUpload(FileRecord{Filename: "x"})
	s.AddUpload(FileRecord{Filename: "y"})
	if s.AttachDownloadByFilenameID("", DownloadResult{}) {
		t.Error("empty marker must never match — would silently misroute downloads in trackid mode")
	}
}

func TestCSVHeader_IncludesFilenameID(t *testing.T) {
	// The column was added in v0.8.1 (filename-mode round-trip) and the
	// recover path relies on it being present. Lock it down so a future
	// reorder doesn't drop it.
	found := false
	for _, h := range CSVHeader {
		if h == "filename_id" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("CSVHeader missing filename_id column: %v", CSVHeader)
	}
}
