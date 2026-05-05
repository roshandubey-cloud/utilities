package report

import (
	"testing"
	"time"
)

// TestStore_HasUpload_PerRunOwnershipFilter pins the v0.19.x download
// ownership check: a run can only "own" files it actually uploaded.
// HasUploadByFilenameID and HasUploadByBasename are the O(1) lookups
// the download poller uses to refuse files left behind by previous
// runs. Without this gate, a server that doesn't drain its outbox
// would cause every poll tick to redownload every leftover.
func TestStore_HasUpload_PerRunOwnershipFilter(t *testing.T) {
	s := NewStore()
	now := time.Now()
	s.AddUpload(FileRecord{
		User:       "u1",
		Filename:   "invoice_slt_abc123def456_.csv",
		FilenameID: "abc123def456",
		StartTime:  now,
	})

	// Filename-mode: a marker we uploaded must be ours.
	if !s.HasUploadByFilenameID("abc123def456") {
		t.Error("HasUploadByFilenameID returned false for our own marker")
	}
	// A marker we never uploaded (leftover from a prior run) must NOT be.
	if s.HasUploadByFilenameID("zzzzzzzzzzzz") {
		t.Error("HasUploadByFilenameID returned true for a marker we never uploaded")
	}
	// Empty marker is never ours (defensive).
	if s.HasUploadByFilenameID("") {
		t.Error("HasUploadByFilenameID(empty) must be false")
	}

	// Trackid-mode: the basename (without #trackid suffix) must be ours.
	s.AddUpload(FileRecord{
		User:      "u1",
		Filename:  "report.txt",
		StartTime: now,
	})
	if !s.HasUploadByBasename("report.txt") {
		t.Error("HasUploadByBasename returned false for our own basename")
	}
	if s.HasUploadByBasename("leftover-from-yesterday.txt") {
		t.Error("HasUploadByBasename returned true for a basename we never uploaded")
	}
	if s.HasUploadByBasename("") {
		t.Error("HasUploadByBasename(empty) must be false")
	}
}

// TestStore_HasUpload_PrunedAfterSettledFlush pins the v0.19.3 heap
// fix: when a record's round-trip is settled (download attached, or
// upload-failed-with-no-download-path, or download-error-final) and
// the record is flushed to disk, the byKey / byBasename / byFilenameID
// entries are pruned. pprof on an 8 h run showed AddUpload retaining
// ~600 B per record across these indices — at 152 k records that's
// 91 MB of dead weight. Pruning here keeps long-run heap flat for
// the dominant case (uploads succeed, downloads complete).
//
// The v0.19.2 retention contract is preserved for records that aren't
// settled — see TestStore_HasUpload_SurvivesFlush.
func TestStore_HasUpload_PrunedAfterSettledFlush(t *testing.T) {
	s := NewStore()
	stream, err := NewCSVStreamWriter(t.TempDir() + "/x.csv")
	if err != nil {
		t.Fatalf("NewStream: %v", err)
	}
	s.SetStream(stream)
	now := time.Now()
	s.AddUpload(FileRecord{
		User:              "u1",
		Filename:          "settled_slt_donedownload_.csv",
		FilenameID:        "donedownload",
		StartTime:         now,
		EndTime:           now.Add(10 * time.Millisecond),
		TrackID:           "FILENAME:donedownload",
		DownloadStartTime: now.Add(2 * time.Second),
		DownloadEndTime:   now.Add(3 * time.Second), // round-trip complete
	})

	// Sanity: index is populated before flush.
	if !s.HasUploadByFilenameID("donedownload") {
		t.Fatal("setup: HasUploadByFilenameID should be true before flush")
	}

	// Flush all (the record qualifies — DownloadEndTime is non-zero).
	flushed, err := s.FlushFinalized(func(*FileRecord) bool { return true }, nil, nil)
	if err != nil {
		t.Fatalf("FlushFinalized: %v", err)
	}
	if flushed != 1 {
		t.Fatalf("expected 1 record flushed, got %d", flushed)
	}

	// Prune assertion: indices were freed because the round-trip is
	// settled and no late arrival is possible.
	if s.HasUploadByFilenameID("donedownload") {
		t.Error("v0.19.3: HasUploadByFilenameID still true after settled flush — index not pruned")
	}
	if s.HasUploadByBasename("settled_slt_donedownload_.csv") {
		t.Error("v0.19.3: HasUploadByBasename still true after settled flush — index not pruned")
	}
}

// TestStore_HasUpload_PrunedAfterUploadFailure pins the second prune
// case: an upload that failed before the trackid stage was reached
// has no possible download path, so its indices are dead weight too.
func TestStore_HasUpload_PrunedAfterUploadFailure(t *testing.T) {
	s := NewStore()
	stream, err := NewCSVStreamWriter(t.TempDir() + "/x.csv")
	if err != nil {
		t.Fatalf("NewStream: %v", err)
	}
	s.SetStream(stream)
	now := time.Now()
	s.AddUpload(FileRecord{
		User:       "u1",
		Filename:   "broken_slt_failedupload_.csv",
		FilenameID: "failedupload",
		StartTime:  now,
		EndTime:    now.Add(10 * time.Millisecond),
		ErrorCode:  "DIAL_FAILED", // upload failed pre-trackid
		// TrackID empty — no rename ever happened
	})

	flushed, err := s.FlushFinalized(func(*FileRecord) bool { return true }, nil, nil)
	if err != nil {
		t.Fatalf("FlushFinalized: %v", err)
	}
	if flushed != 1 {
		t.Fatalf("expected 1 record flushed, got %d", flushed)
	}
	if s.HasUploadByFilenameID("failedupload") {
		t.Error("v0.19.3: failed-upload index not pruned after flush")
	}
}

// TestStore_HasUpload_SurvivesFlush pins that the ownership lookup
// remains accurate after FlushFinalized has released the live record
// when the round-trip is NOT yet settled (no DownloadEndTime, no
// upload error). v0.19.2 retention contract — late-arriving round
// trips still need their marker recognised. The v0.19.3 prune is
// scoped to settled records only.
func TestStore_HasUpload_SurvivesFlush(t *testing.T) {
	s := NewStore()
	now := time.Now()
	s.AddUpload(FileRecord{
		User:       "u1",
		Filename:   "early_slt_oldmarker001_.csv",
		FilenameID: "oldmarker001",
		StartTime:  now,
		EndTime:    now.Add(10 * time.Millisecond),
	})

	// Flush everything finalisable. With no stream attached we don't
	// actually write to disk, but the in-memory release path still
	// nils s.records[idx] — the index map is what's load-bearing.
	_, _ = s.FlushFinalized(func(*FileRecord) bool { return true }, nil, nil)

	// Lookup must still succeed even though the live record was
	// released — otherwise the download poller would orphan our own
	// late round-trips on long high-fpm runs.
	if !s.HasUploadByFilenameID("oldmarker001") {
		t.Error("HasUploadByFilenameID lost the marker after flush — late round-trips would now orphan")
	}
	if !s.HasUploadByBasename("early_slt_oldmarker001_.csv") {
		t.Error("HasUploadByBasename lost the basename after flush")
	}
}

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

// v0.18.0 — when both sides hashed and the values agree, HashMatch
// flips true and the row reports a clean download. This is the happy
// path for VerifyHashes runs.
func TestStore_AttachDownload_HashMatchSetsTrue(t *testing.T) {
	s := NewStore()
	now := time.Now()
	hash := "abcd1234"
	s.AddUpload(FileRecord{
		User: "u1", Filename: "x_slt_marker_.csv", FilenameID: "marker",
		StartTime: now, EndTime: now.Add(10 * time.Millisecond), SizeBytes: 64,
		UploadSHA256: hash,
	})
	if !s.AttachDownloadByFilenameID("marker", DownloadResult{
		DownloadUser: "d1", StartTime: now.Add(20 * time.Millisecond),
		EndTime: now.Add(30 * time.Millisecond), SizeBytes: 64, SHA256: hash,
	}) {
		t.Fatal("attach should succeed")
	}
	got := s.Snapshot()[0]
	if !got.HashMatch {
		t.Errorf("HashMatch=true expected; got false (upload=%s download=%s)", got.UploadSHA256, got.DownloadSHA256)
	}
	if got.DownloadError != "" {
		t.Errorf("clean match should leave DownloadError empty; got %q", got.DownloadError)
	}
}

// v0.18.0 — mismatch path: download produced a different hash than
// upload. Row keeps both hashes for forensics; DownloadError is
// stamped HASH_MISMATCH so the operator finds it via normal error chips.
func TestStore_AttachDownload_HashMismatchSetsError(t *testing.T) {
	s := NewStore()
	now := time.Now()
	s.AddUpload(FileRecord{
		User: "u1", Filename: "x_slt_marker2_.csv", FilenameID: "marker2",
		StartTime: now, EndTime: now.Add(10 * time.Millisecond), SizeBytes: 64,
		UploadSHA256: "expected",
	})
	if !s.AttachDownloadByFilenameID("marker2", DownloadResult{
		DownloadUser: "d1", StartTime: now.Add(20 * time.Millisecond),
		EndTime: now.Add(30 * time.Millisecond), SizeBytes: 64, SHA256: "actually-different",
	}) {
		t.Fatal("attach should succeed even on hash mismatch")
	}
	got := s.Snapshot()[0]
	if got.HashMatch {
		t.Errorf("HashMatch=false expected for differing hashes")
	}
	if got.DownloadError != "HASH_MISMATCH" {
		t.Errorf("DownloadError=HASH_MISMATCH expected; got %q", got.DownloadError)
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
