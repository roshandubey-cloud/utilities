package report

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"
)

type FileRecord struct {
	User         string
	Kind         string // "normal" or "large"
	Filename     string
	StartTime    time.Time
	EndTime      time.Time
	SizeBytes    int64 // bytes actually transferred (may be < ExpectedSize on failure)
	ExpectedSize int64 // bytes we planned to send
	Incomplete   bool  // true when upload failed mid-stream or never finished
	SpeedMBps    float64
	TrackID      string
	TrackIDAt    time.Time
	TrackIDWait  time.Duration
	InSlowdown   bool
	Error        string
	ErrorCode    string // stable code: POOL_EMPTY, DIAL, AUTH, CREATE, WRITE, CLOSE, PANIC, TRACKID_TIMEOUT, DOWNLOAD

	// FilenameID is the 12-char marker the runner injected into the
	// upload filename when the run is configured for filename-mode
	// round-trip tracking (Download.MatchMode == "filename"). The
	// download worker matches files in the destination outbox by
	// scanning for this marker as a substring, so it works even when
	// the SFTP server prefixes or suffixes the filename in transit.
	// Empty in trackid mode (the default).
	FilenameID string

	// Download phase — populated if download test is enabled and this file
	// (matched by trackID) was selected for download.
	DownloadUser      string
	// DownloadAvailableAt is when the download worker first observed the
	// file in the destination outbox (LIST result), separate from
	// TrackIDAt which is when the upload-side track-id watcher detected
	// it. The two can differ when the download worker has its own
	// poll cadence or queue depth — surfacing both lets the operator
	// separate "server made it available slow" from "client picked it
	// up slow." Defaults to the zero time when not yet observed.
	DownloadAvailableAt time.Time
	DownloadStartTime time.Time
	DownloadEndTime   time.Time
	DownloadSizeBytes int64
	DownloadSpeedMBps float64
	DownloadWait      time.Duration // from TrackIDAt to DownloadStartTime
	DownloadError     string

	// UploadSHA256 / DownloadSHA256 (v0.18.0) — populated only when
	// the run was started with VerifyHashes=true. Hex-encoded SHA-256
	// of the bytes that were physically streamed in / out (the runner
	// wraps both source and sink with hashing copies). HashMatch is
	// derived: true when both sides produced a hash and they match.
	// Zero values are the default; CSV writer renders them as "" for
	// rows that didn't participate in hashing.
	UploadSHA256   string
	DownloadSHA256 string
	HashMatch      bool
}

type DownloadResult struct {
	DownloadUser string
	StartTime    time.Time
	EndTime      time.Time
	SizeBytes    int64
	SpeedMBps    float64
	AvailableAt  time.Time
	Error        string

	// SHA256 (v0.18.0) — hex-encoded SHA-256 of the bytes streamed
	// from the server. Empty when the run had VerifyHashes off.
	// AttachDownloadBy* copies this into the matching FileRecord and
	// derives FileRecord.HashMatch from comparison with UploadSHA256.
	SHA256 string
}

// Per-file raw speed is stored as bytes/duration — always computed, so the
// Speed column is never blank. But for a small file or a sub-100 ms transfer,
// that raw number is dominated by SFTP open/close overhead and is not a
// faithful measure of network throughput. IsReliablePerFileSpeed reports
// whether the raw per-file number can be trusted on its own.
const (
	MinReliableBytes    = 1 << 20 // 1 MiB
	MinReliableDuration = 100 * time.Millisecond
)

func RawSpeedMBps(bytes int64, dur time.Duration) float64 {
	if bytes <= 0 || dur <= 0 {
		return 0
	}
	return float64(bytes) / (1024.0 * 1024.0) / dur.Seconds()
}

func IsReliablePerFileSpeed(bytes int64, dur time.Duration) bool {
	return bytes >= MinReliableBytes && dur >= MinReliableDuration
}

// Store holds the in-memory per-file ledger for an active run. Entries are
// pointers so a finalized row can be nil'd out after being flushed to disk
// by the streaming writer — keeps RAM flat on long high-fpm runs.
type Store struct {
	mu           sync.Mutex
	records      []*FileRecord
	byKey        map[string]int  // user|filename -> index
	byBasename   map[string]int  // filename -> index
	byFilenameID map[string]int  // filename-mode marker -> index
	byMinute     map[int64][]int // unix-minute of StartTime -> record indexes
	stream     *CSVStreamWriter
	// Monotonic counters for observability.
	flushed int64
	// recentTail keeps a copy of the last N records that were flushed to
	// disk, so the UI's "Recent uploads" pane stays populated even though
	// most records have been released from the live in-memory slice.
	// Bounded — see recentTailCap.
	recentTail    []FileRecord
	recentTailCap int
}

// RecentTailCap is the default maximum number of recently-flushed records
// the Store keeps in memory for the UI. The UI requests at most 200 rows
// per poll, so 256 covers any reasonable display while keeping memory
// pressure trivial (~128 KB at ~500 B/row).
const RecentTailCap = 256

func NewStore() *Store {
	return &Store{
		byKey:         map[string]int{},
		byBasename:    map[string]int{},
		byFilenameID:  map[string]int{},
		byMinute:      map[int64][]int{},
		recentTailCap: RecentTailCap,
	}
}

// SetStream attaches a streaming CSV sink. Call once before any AddUpload.
// Passing nil disables streaming.
func (s *Store) SetStream(w *CSVStreamWriter) { s.mu.Lock(); s.stream = w; s.mu.Unlock() }

func (s *Store) AddUpload(r FileRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := len(s.records)
	rec := r // copy
	s.records = append(s.records, &rec)
	s.byKey[rec.User+"|"+rec.Filename] = idx
	s.byBasename[rec.Filename] = idx
	if rec.FilenameID != "" {
		s.byFilenameID[rec.FilenameID] = idx
	}
	if !rec.StartTime.IsZero() {
		m := rec.StartTime.Unix() / 60
		s.byMinute[m] = append(s.byMinute[m], idx)
	}
}

func (s *Store) AttachTrackID(user, filename, trackID string, detectedAt time.Time, timedOut bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.byKey[user+"|"+filename]
	if !ok {
		return
	}
	r := s.records[idx]
	if r == nil { // already flushed
		return
	}
	if timedOut {
		r.TrackID = "TRACKID_TIMEOUT"
		if r.ErrorCode == "" {
			r.ErrorCode = "TRACKID_TIMEOUT"
		}
	} else {
		r.TrackID = trackID
	}
	r.TrackIDAt = detectedAt
	if !r.EndTime.IsZero() {
		r.TrackIDWait = detectedAt.Sub(r.EndTime)
	}
}

// AttachDownloadByFilenameID attaches a DownloadResult to whichever
// upload record was tagged with the given marker token. Used by the
// filename-mode download worker: it scans an arbitrary outbox name for
// the embedded marker and looks the record up here. Returns false when
// no upload in this run owned this marker (orphan) or the record has
// already been flushed to disk.
func (s *Store) AttachDownloadByFilenameID(marker string, d DownloadResult) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.byFilenameID[marker]
	if !ok {
		return false
	}
	r := s.records[idx]
	if r == nil {
		return false
	}
	r.DownloadUser = d.DownloadUser
	r.DownloadAvailableAt = d.AvailableAt
	r.DownloadStartTime = d.StartTime
	r.DownloadEndTime = d.EndTime
	r.DownloadSizeBytes = d.SizeBytes
	r.DownloadSpeedMBps = d.SpeedMBps
	r.DownloadError = d.Error
	// v0.18.0 — hash verification. d.SHA256 is empty when the run
	// disabled VerifyHashes; in that case both sides stay empty
	// strings and HashMatch stays false (which CSV renders as
	// "false" for those rows — readable for verify-off runs).
	if d.SHA256 != "" {
		r.DownloadSHA256 = d.SHA256
		if r.UploadSHA256 != "" {
			r.HashMatch = r.UploadSHA256 == r.DownloadSHA256
			if !r.HashMatch && r.DownloadError == "" {
				// Override an otherwise-clean download with a
				// HASH_MISMATCH so the row's error column tells
				// the operator something failed end-to-end even
				// when the bytes themselves arrived.
				r.DownloadError = "HASH_MISMATCH"
			}
		}
	}
	if !d.AvailableAt.IsZero() && !d.StartTime.IsZero() {
		r.DownloadWait = d.StartTime.Sub(d.AvailableAt)
	}
	return true
}

// HasUploadByFilenameID reports whether THIS run uploaded a file
// carrying the supplied filename-mode marker. Used by the download
// poller to refuse files left in the outbox by a previous run (or by
// any tool that happens to use the same _slt_ marker pattern) — those
// files would otherwise be redownloaded every poll tick because the
// server isn't moving them out of the folder. Survives FlushFinalized:
// the byFilenameID index is preserved even after the live record is
// released to disk, so a long-running test still reliably says "yes,
// that one's mine" for early uploads.
func (s *Store) HasUploadByFilenameID(marker string) bool {
	if marker == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.byFilenameID[marker]
	return ok
}

// HasUploadByBasename reports whether THIS run uploaded a file with
// the supplied basename (no track-id suffix). Used by the download
// poller in track-id mode to refuse files the server already had on
// disk before the run started — typical case is a stale outbox the
// operator forgot to drain between tests. Same flush-survives
// semantics as HasUploadByFilenameID.
func (s *Store) HasUploadByBasename(basename string) bool {
	if basename == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.byBasename[basename]
	return ok
}

// AttachDownloadByBasename attaches a DownloadResult to whichever upload
// record has this basename. Returns false if we never uploaded a file with
// that basename in this run (caller can count orphans) or if the record has
// already been flushed to disk.
func (s *Store) AttachDownloadByBasename(basename string, d DownloadResult) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.byBasename[basename]
	if !ok {
		return false
	}
	r := s.records[idx]
	if r == nil {
		return false
	}
	r.DownloadUser = d.DownloadUser
	r.DownloadAvailableAt = d.AvailableAt
	r.DownloadStartTime = d.StartTime
	r.DownloadEndTime = d.EndTime
	r.DownloadSizeBytes = d.SizeBytes
	r.DownloadSpeedMBps = d.SpeedMBps
	r.DownloadError = d.Error
	// v0.18.0 — hash verification. d.SHA256 is empty when the run
	// disabled VerifyHashes; in that case both sides stay empty
	// strings and HashMatch stays false (which CSV renders as
	// "false" for those rows — readable for verify-off runs).
	if d.SHA256 != "" {
		r.DownloadSHA256 = d.SHA256
		if r.UploadSHA256 != "" {
			r.HashMatch = r.UploadSHA256 == r.DownloadSHA256
			if !r.HashMatch && r.DownloadError == "" {
				// Override an otherwise-clean download with a
				// HASH_MISMATCH so the row's error column tells
				// the operator something failed end-to-end even
				// when the bytes themselves arrived.
				r.DownloadError = "HASH_MISMATCH"
			}
		}
	}
	if !d.AvailableAt.IsZero() && !d.StartTime.IsZero() {
		r.DownloadWait = d.StartTime.Sub(d.AvailableAt)
	}
	return true
}

func (s *Store) AttachDownload(user, filename string, d DownloadResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.byKey[user+"|"+filename]
	if !ok {
		return
	}
	r := s.records[idx]
	if r == nil {
		return
	}
	r.DownloadUser = d.DownloadUser
	r.DownloadAvailableAt = d.AvailableAt
	r.DownloadStartTime = d.StartTime
	r.DownloadEndTime = d.EndTime
	r.DownloadSizeBytes = d.SizeBytes
	r.DownloadSpeedMBps = d.SpeedMBps
	r.DownloadError = d.Error
	// v0.18.0 — hash verification. d.SHA256 is empty when the run
	// disabled VerifyHashes; in that case both sides stay empty
	// strings and HashMatch stays false (which CSV renders as
	// "false" for those rows — readable for verify-off runs).
	if d.SHA256 != "" {
		r.DownloadSHA256 = d.SHA256
		if r.UploadSHA256 != "" {
			r.HashMatch = r.UploadSHA256 == r.DownloadSHA256
			if !r.HashMatch && r.DownloadError == "" {
				// Override an otherwise-clean download with a
				// HASH_MISMATCH so the row's error column tells
				// the operator something failed end-to-end even
				// when the bytes themselves arrived.
				r.DownloadError = "HASH_MISMATCH"
			}
		}
	}
	if !d.AvailableAt.IsZero() && !d.StartTime.IsZero() {
		r.DownloadWait = d.StartTime.Sub(d.AvailableAt)
	}
}

// Snapshot returns the live (not-yet-flushed) records. For very long runs,
// most records will have been streamed to disk and nil'd — use WriteRemainingCSV
// to serialize them, or read the stream file directly.
func (s *Store) Snapshot() []FileRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]FileRecord, 0, len(s.records))
	for _, r := range s.records {
		if r != nil {
			out = append(out, *r)
		}
	}
	return out
}

// SnapshotTail returns the most recent n records by StartTime. Combines
// live (still-mutable) records and the recent-flushed ring buffer so the
// UI keeps showing rows even though streaming has released most of them
// from the live slice. Cost: O(live + recentTail) — both are bounded.
func (s *Store) SnapshotTail(n int) []FileRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n <= 0 {
		return nil
	}
	out := make([]FileRecord, 0, len(s.recentTail)+n)
	out = append(out, s.recentTail...)
	for _, r := range s.records {
		if r != nil {
			out = append(out, *r)
		}
	}
	if len(out) == 0 {
		return nil
	}
	// Most-recent first by StartTime.
	sort.Slice(out, func(i, j int) bool { return out[i].StartTime.Before(out[j].StartTime) })
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

// Len returns the number of slots ever allocated (flushed + live). Useful
// for progress displays; for live-in-memory count use LiveCount.
func (s *Store) Len() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.records) }

// LiveCount returns how many records are still in memory (not yet flushed).
func (s *Store) LiveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.records {
		if r != nil {
			n++
		}
	}
	return n
}

// FlushedCount returns the cumulative number of records moved from memory
// to the stream file during this run.
func (s *Store) FlushedCount() int64 { s.mu.Lock(); defer s.mu.Unlock(); return s.flushed }

// RecordsInMinute returns records whose StartTime falls in the given
// unix-minute, skipping any that have already been flushed.
func (s *Store) RecordsInMinute(minute int64) []FileRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	idxs := s.byMinute[minute]
	if len(idxs) == 0 {
		return nil
	}
	out := make([]FileRecord, 0, len(idxs))
	for _, idx := range idxs {
		if s.records[idx] == nil {
			continue
		}
		out = append(out, *s.records[idx])
	}
	return out
}

// FlushFinalized walks the live records and, for any where isFinal(r)
// returns true, writes the row through the stream and releases the in-memory
// slot (records[idx] = nil). Returns how many rows were flushed.
//
// isFinal is supplied by the caller (typically the runner) because "final"
// depends on whether the run has a download phase, whether the trackid
// watcher has drained, etc. Passing a nil stream is a no-op.
func (s *Store) FlushFinalized(isFinal func(*FileRecord) bool, slowdownMins map[int64]bool, eff EffectiveSpeedFn) (int, error) {
	if s == nil {
		return 0, nil
	}
	s.mu.Lock()
	if s.stream == nil {
		s.mu.Unlock()
		return 0, nil
	}
	// v0.19.0 — collect finalized rows under the Store mutex, then
	// release the mutex BEFORE doing disk I/O so AddUpload /
	// AttachDownload* aren't blocked behind a slow disk. The batch
	// path on the writer (WriteRowsBatch) flushes once instead of
	// once-per-row — pre-v0.19 a 250-row flush issued 250 csv.Writer
	// flushes; now it issues one.
	batch := make([]FileRecord, 0, 64)
	indices := make([]int, 0, 64)
	for i, r := range s.records {
		if r == nil || !isFinal(r) {
			continue
		}
		batch = append(batch, *r)
		indices = append(indices, i)
	}
	if len(batch) == 0 {
		s.mu.Unlock()
		return 0, nil
	}
	stream := s.stream
	s.mu.Unlock()

	// Disk write happens lock-free — only the writer's own mutex is
	// held during the actual fwrite syscalls.
	if err := stream.WriteRowsBatch(batch, slowdownMins, eff); err != nil {
		return 0, err
	}

	// Re-acquire to commit the bookkeeping (recentTail update + clear
	// the in-memory slots). This window is tight — typically <50 µs
	// even for a 1k-row batch — and adders see the ring buffer
	// updated atomically with respect to the slot clears.
	//
	// v0.19.3 — also prune the byKey / byBasename / byFilenameID entries
	// for records whose round-trip is settled (download attached OR
	// upload failed cleanly with no download possible). pprof on an 8 h
	// run showed AddUpload retaining ~600 B per record across these
	// indices, ~91 MB at 152 k records. Pruning here cuts that to ~0
	// for the dominant case (downloads succeeded). Late-arrival round
	// trips for records that timed out without a download keep their
	// indices through run-end (their entries are released only when
	// records[idx] = nil and we never repopulate them either).
	s.mu.Lock()
	for _, i := range indices {
		if s.records[i] == nil {
			continue // defensive: skip if a concurrent caller cleared this slot
		}
		rec := s.records[i]
		s.recentTail = append(s.recentTail, *rec)
		if len(s.recentTail) > s.recentTailCap {
			s.recentTail = s.recentTail[len(s.recentTail)-s.recentTailCap:]
		}
		// Prune index entries when no further mutation is possible.
		// Three cases qualify, derived from the same "final" predicate
		// the caller used to select this row:
		//   1. Upload failed (ErrorCode != "" && TrackID == "") — no
		//      download path was ever opened, indices are dead weight.
		//   2. Download attached (DownloadEndTime != zero) — round-trip
		//      complete, no late arrival possible.
		//   3. Download error recorded (DownloadError != "") — explicit
		//      failure, no further attach expected.
		// Cases 1+2+3 cover the dominant heap-growth path on long runs.
		// Records that don't qualify (track-id resolved but download
		// timed out within grace) keep their indices — late arrivals
		// can still try to attach, although by definition the attach
		// will fail because s.records[idx] is now nil. That's fine —
		// AttachDownloadByFilenameID returns false in that case and
		// the caller counts it as orphan.
		canPrune := false
		switch {
		case rec.ErrorCode != "" && rec.TrackID == "":
			canPrune = true
		case !rec.DownloadEndTime.IsZero():
			canPrune = true
		case rec.DownloadError != "":
			canPrune = true
		}
		if canPrune {
			delete(s.byKey, rec.User+"|"+rec.Filename)
			delete(s.byBasename, rec.Filename)
			if rec.FilenameID != "" {
				delete(s.byFilenameID, rec.FilenameID)
			}
		}
		s.records[i] = nil
	}
	flushed := len(batch)
	s.flushed += int64(flushed)
	s.mu.Unlock()
	return flushed, nil
}

// StampPendingDownloads marks every still-live record that completed an
// upload (TrackID set) but never received a matching download with the
// given error code. Returns the number of rows stamped. Used by the seal
// path so a CSV row with empty download_user is never silently emitted —
// readers can grep download_error for the timeout code instead.
func (s *Store) StampPendingDownloads(errCode string) int {
	if s == nil || errCode == "" {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stamped := 0
	for _, r := range s.records {
		if r == nil {
			continue
		}
		if r.TrackID == "" {
			continue
		}
		if !r.DownloadEndTime.IsZero() {
			continue
		}
		if r.DownloadError != "" {
			continue
		}
		r.DownloadError = errCode
		stamped++
	}
	return stamped
}

// WriteRemainingCSV serializes every still-live record through a one-shot
// csv.Writer. Used by the teardown path when the streaming file needs a
// final batch appended, and by the live-download path to tack the in-memory
// tail onto the stream file's content.
func (s *Store) WriteRemainingCSV(cw *csv.Writer, slowdownMins map[int64]bool, eff EffectiveSpeedFn) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.records {
		if r == nil {
			continue
		}
		if err := cw.Write(buildRow(*r, slowdownMins, eff)); err != nil {
			return err
		}
	}
	return nil
}

// EffectiveSpeedFn lets the caller compute a truthful MB/s value for rows where
// the raw per-file timing is unreliable (small files, short transfers).
type EffectiveSpeedFn func(bytes int64, startTime time.Time, dur time.Duration) float64

// CSVHeader is the canonical column list, exported so both the bulk and
// streaming writers agree.
//
// History note: pre-v0.13.29, `download_available_at` was written with
// `r.TrackIDAt` — the same value `track_id_detected_at` already carried.
// Two columns, identical data. v0.13.29 added a real
// `FileRecord.DownloadAvailableAt`, populated from
// `DownloadResult.AvailableAt`, so the column now carries useful data
// (when the download worker first saw the file in the destination
// outbox vs when the track-id was detected via polling). They can
// differ when the download worker has its own queue depth / pacing.
var CSVHeader = []string{
	"user", "kind", "filename", "filename_id", "start_time", "end_time", "duration_sec",
	"size_bytes", "expected_bytes", "incomplete",
	"upload_mbps", "upload_mbps_source",
	"track_id", "track_id_detected_at", "track_id_wait_sec", "processing_time_min",
	"in_slowdown_minute", "error", "error_code",
	"download_user", "download_available_at", "download_start", "download_end",
	"download_wait_sec", "download_duration_sec", "download_size_bytes",
	"download_mbps", "download_mbps_source", "download_error",
	// v0.18.0 — hash-verification trio. Empty when VerifyHashes was
	// off for the run; non-empty rows always carry both hashes (or
	// the upload hash plus a HASH_MISMATCH download_error when the
	// download produced something different).
	"upload_sha256", "download_sha256", "hash_match",
}

// buildRow is the single source of truth for a CSV line. Used by both
// WriteCSV (bulk) and the streaming writer (row-at-a-time).
func buildRow(r FileRecord, slowdownMins map[int64]bool, eff EffectiveSpeedFn) []string {
	upDur := r.EndTime.Sub(r.StartTime)
	dlDur := r.DownloadEndTime.Sub(r.DownloadStartTime)
	inSlow := r.InSlowdown
	if !r.StartTime.IsZero() && slowdownMins != nil && slowdownMins[r.StartTime.Unix()/60] {
		inSlow = true
	}
	upSpeed, upSource := r.SpeedMBps, "per_file"
	if !IsReliablePerFileSpeed(r.SizeBytes, upDur) && eff != nil {
		upSpeed = eff(r.SizeBytes, r.StartTime, upDur)
		upSource = "window_rate"
	}
	dlSpeed, dlSource := r.DownloadSpeedMBps, "per_file"
	if !IsReliablePerFileSpeed(r.DownloadSizeBytes, dlDur) && eff != nil {
		dlSpeed = eff(r.DownloadSizeBytes, r.DownloadStartTime, dlDur)
		dlSource = "window_rate"
	}
	return []string{
		r.User,
		r.Kind,
		r.Filename,
		r.FilenameID,
		r.StartTime.Format(time.RFC3339Nano),
		r.EndTime.Format(time.RFC3339Nano),
		strconv.FormatFloat(upDur.Seconds(), 'f', 3, 64),
		strconv.FormatInt(r.SizeBytes, 10),
		strconv.FormatInt(r.ExpectedSize, 10),
		strconv.FormatBool(r.Incomplete),
		strconv.FormatFloat(upSpeed, 'f', 3, 64),
		upSource,
		r.TrackID,
		fmtTime(r.TrackIDAt),
		strconv.FormatFloat(r.TrackIDWait.Seconds(), 'f', 3, 64),
		strconv.FormatFloat(r.TrackIDWait.Minutes(), 'f', 3, 64),
		strconv.FormatBool(inSlow),
		r.Error,
		r.ErrorCode,
		r.DownloadUser,
		fmtTime(r.DownloadAvailableAt),
		fmtTime(r.DownloadStartTime),
		fmtTime(r.DownloadEndTime),
		strconv.FormatFloat(r.DownloadWait.Seconds(), 'f', 3, 64),
		strconv.FormatFloat(dlDur.Seconds(), 'f', 3, 64),
		strconv.FormatInt(r.DownloadSizeBytes, 10),
		strconv.FormatFloat(dlSpeed, 'f', 3, 64),
		dlSource,
		r.DownloadError,
		// v0.18.0 — hash trio. hash_match defaults to "false" and
		// only flips to "true" when both sides hashed AND the values
		// agree, which keeps the column readable for verify-off runs
		// (everything stays empty + false).
		r.UploadSHA256,
		r.DownloadSHA256,
		strconv.FormatBool(r.HashMatch),
	}
}

// WriteCSV is the bulk writer: header + all rows at once. Kept for tests
// and for historical (post-teardown) CSV downloads that read back the full
// in-memory snapshot.
func WriteCSV(w io.Writer, records []FileRecord, slowdownMinutes map[int64]bool, effective EffectiveSpeedFn) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()
	if err := cw.Write(CSVHeader); err != nil {
		return err
	}
	for _, r := range records {
		if err := cw.Write(buildRow(r, slowdownMinutes, effective)); err != nil {
			return fmt.Errorf("write row: %w", err)
		}
	}
	return nil
}

// ReleaseHeavyState drops the in-memory bookkeeping that's only useful
// during an active run — once sealed, the only remaining consumer is
// the /api/runs history pane reading recentTail for the "recent rows"
// preview. Everything else (records slice, byKey/byBasename/
// byFilenameID/byMinute indexes) is freed so the Server's run-history
// retention doesn't keep ~150 KB / Run alive after the JSON metadata
// is on disk. v0.19.5 — closes the "true idle" gap surfaced by the
// 30-min hash-verify test (heap residue was ~+1 MB after 6 runs).
//
// recentTail is kept (capped at 256 × ~500 B = ~128 KB worst case)
// because the UI uses it to render the last few rows of a finished
// run without re-reading the CSV from disk. That's a deliberate
// tradeoff: a tiny constant cost per retained Run vs unbounded
// structures that grew with file count.
//
// Safe to call multiple times (idempotent). After release, AddUpload
// would silently no-op (records is nil) — callers must not write to
// a Store after ReleaseHeavyState. The runner only calls it from
// teardown's seal path, after dispatchers + watcher have stopped.
func (s *Store) ReleaseHeavyState() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Drop the unbounded indexes; their only consumer (the runner's
	// dispatch + ownership filter + flusher) is already shut down.
	s.byKey = nil
	s.byBasename = nil
	s.byFilenameID = nil
	s.byMinute = nil
	// recentTail kept — bounded at recentTailCap, used by /api/runs UI.
	// records[] kept — entries are mostly nil after flush; Snapshot()
	// callers (tests + /api/report.csv on a sealed run) skip nils
	// and pay only the 8 B/slot cost of the slice itself.
}

// CSVStreamWriter appends finalized rows to an open CSV file. Thread-safe;
// the runner flusher + the teardown path may call WriteRow concurrently
// with the live-CSV-download handler acquiring its own lock.
type CSVStreamWriter struct {
	mu          sync.Mutex
	file        *os.File
	cw          *csv.Writer
	wroteHeader bool
}

// NewCSVStreamWriter opens (or creates) the CSV file in append mode. The
// header is written lazily on the first row so an empty run leaves an empty
// file (nothing to serve yet).
func NewCSVStreamWriter(path string) (*CSVStreamWriter, error) {
	// 0o600: CSV may contain credentials in error strings (e.g. an SSH error
	// echoed in Error/DownloadError). Owner-only on shared hosts.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	return &CSVStreamWriter{file: f, cw: csv.NewWriter(f)}, nil
}

func (s *CSVStreamWriter) WriteRow(r FileRecord, slowdownMins map[int64]bool, eff EffectiveSpeedFn) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writeRowLocked(r, slowdownMins, eff); err != nil {
		return err
	}
	s.cw.Flush()
	return s.cw.Error()
}

// writeRowLocked is the lockless inner of WriteRow / WriteRowsBatch.
// Caller must hold s.mu. Does NOT call Flush — the caller batches.
func (s *CSVStreamWriter) writeRowLocked(r FileRecord, slowdownMins map[int64]bool, eff EffectiveSpeedFn) error {
	if !s.wroteHeader {
		if err := s.cw.Write(CSVHeader); err != nil {
			return err
		}
		s.wroteHeader = true
	}
	return s.cw.Write(buildRow(r, slowdownMins, eff))
}

// WriteRowsBatch (v0.19.0) writes N rows under one mutex acquire and
// one cw.Flush. Pre-v0.19.0 every row called WriteRow which flushed
// per-row — at 5k fpm that was 80+ syscalls/sec inside a 5-second
// flush window. The batch path defers the flush so the underlying
// csv.Writer's own buffering can amortise the syscall cost. Caller is
// responsible for collecting the rows beforehand (e.g. snapshot under
// the Store mutex) so this method holds CSVStreamWriter.mu only for
// the duration of the actual disk writes.
func (s *CSVStreamWriter) WriteRowsBatch(records []FileRecord, slowdownMins map[int64]bool, eff EffectiveSpeedFn) error {
	if len(records) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range records {
		if err := s.writeRowLocked(records[i], slowdownMins, eff); err != nil {
			s.cw.Flush()
			return err
		}
	}
	s.cw.Flush()
	return s.cw.Error()
}

// Close flushes any buffered rows and closes the file. Idempotent for
// practical purposes — after Close, further WriteRow calls will error.
func (s *CSVStreamWriter) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cw != nil {
		s.cw.Flush()
	}
	if s.file != nil {
		err := s.file.Close()
		s.file = nil
		return err
	}
	return nil
}

// Path returns the underlying file path (for live CSV download concatenation).
func (s *CSVStreamWriter) Path() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return ""
	}
	return s.file.Name()
}

// LockForConcat serializes the stream writer against concurrent WriteRow
// calls so the live-CSV handler can read the on-disk file + append the
// in-memory tail atomically. Returns an Unlock function.
func (s *CSVStreamWriter) LockForConcat() func() {
	s.mu.Lock()
	return s.mu.Unlock
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339Nano)
}
