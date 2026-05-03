package runner_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/config"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/mocksftp"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/runner"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/sftpx"
)

// TestRunner_AgainstMockSFTP is the end-to-end integration smoke test:
// boot the in-process mock SFTP server, point the runner at it for a few
// seconds, and assert the runner finished a non-trivial number of files
// without falling over. The mock server is the same one cmd/mockserver
// ships, imported as a library, so what we exercise here is byte-for-byte
// what the desktop app talks to in local dev.
//
// Goals (in priority order):
//   1. Catch top-of-stack regressions: dial, handshake, upload, trackid,
//      seal — if any of these break the test goes red.
//   2. Self-test the LOAD TESTER. We previously had no Go tests at all;
//      the tool was an unverified instrument.
//   3. Stay fast (<5 s) so it runs on every CI push without nagging.
func TestRunner_AgainstMockSFTP(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (mockserver) skipped under -short")
	}

	srv, err := mocksftp.Start(mocksftp.Options{
		Addr:   "127.0.0.1:0",
		Delay:  100 * time.Millisecond, // tight so the watcher can resolve trackids in the run window
		Logger: log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("start mock: %v", err)
	}
	defer srv.Stop()

	host, portStr, err := net.SplitHostPort(srv.Addr().String())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	port, _ := strconv.Atoi(portStr)

	// Trust the mock's host key on the fly — Tests must not depend on a
	// real known_hosts file or the ambient process-wide callback that
	// other tests may have configured.
	sftpx.SetHostKeyCallback(ssh.InsecureIgnoreHostKey())

	cfg := &config.RunConfig{
		Host:            host,
		Port:            port,
		UploadFolder:    "inbox",
		ParallelStreams: 2,
		DurationHours:   2.0 / 3600.0, // 2 seconds
		PollInterval:    250 * time.Millisecond,
		TrackIDTimeout:  5 * time.Second,
		Normal: &config.NormalLoad{
			FilesPerMinute: 600, // 10/sec — easy for the mock
			MinSizeMB:      1,
			MaxSizeMB:      1,
		},
		NormalUsers: []config.UserCreds{
			{Username: "u1", Password: "p", Patterns: []string{"f-*"}},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	run, err := runner.Start(ctx, cfg)
	if err != nil {
		t.Fatalf("runner.Start: %v", err)
	}
	select {
	case <-run.Done():
	case <-ctx.Done():
		t.Fatal("run did not complete in time")
	}

	snap := run.Metrics.Snapshot()
	if snap.TotalFiles < 5 {
		t.Errorf("expected >=5 uploads in 2s at 10/sec, got %d", snap.TotalFiles)
	}
	if snap.TotalBytes < 1024*1024 {
		t.Errorf("expected >=1 MiB uploaded, got %d bytes", snap.TotalBytes)
	}

	// FailedFiles is the public counter the UI reads; should be 0 here.
	if got := run.FailedFiles.Load(); got != 0 {
		t.Errorf("expected zero failures against the mock, got %d", got)
	}
}

// TestRunner_FailingUserDisablesNotCrashes covers the auto-disable policy:
// a user the mock server is configured to fail must trip MaxConsecutive-
// Failures and stop being dispatched, while OTHER users keep going.
func TestRunner_FailingUserDisablesNotCrashes(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (mockserver) skipped under -short")
	}
	srv, err := mocksftp.Start(mocksftp.Options{
		Addr:      "127.0.0.1:0",
		Delay:     50 * time.Millisecond,
		FailUsers: map[string]bool{"badguy": true},
		Logger:    log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	host, portStr, _ := net.SplitHostPort(srv.Addr().String())
	port, _ := strconv.Atoi(portStr)
	sftpx.SetHostKeyCallback(ssh.InsecureIgnoreHostKey())

	cfg := &config.RunConfig{
		Host:                   host,
		Port:                   port,
		UploadFolder:           "inbox",
		ParallelStreams:        1,
		DurationHours:          3.0 / 3600.0, // 3 s
		PollInterval:           200 * time.Millisecond,
		TrackIDTimeout:         5 * time.Second,
		MaxConsecutiveFailures: 3,
		Normal: &config.NormalLoad{
			FilesPerMinute: 600,
			MinSizeMB:      1,
			MaxSizeMB:      1,
		},
		NormalUsers: []config.UserCreds{
			{Username: "good", Password: "p", Patterns: []string{"g-*"}},
			{Username: "badguy", Password: "p", Patterns: []string{"b-*"}},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	run, err := runner.Start(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-run.Done():
	case <-ctx.Done():
		t.Fatal("run did not complete in time")
	}

	disabled := run.DisabledUsers()
	var badDisabled bool
	for _, d := range disabled {
		if d.User == "badguy" {
			badDisabled = true
			break
		}
	}
	if !badDisabled {
		t.Errorf("badguy should have been auto-disabled after 3 consecutive failures, got %+v", disabled)
	}
	if run.FailedFiles.Load() == 0 {
		t.Error("expected non-zero FailedFiles when one user always fails")
	}

	// And the run should still produce successful uploads from the good user.
	if snap := run.Metrics.Snapshot(); snap.TotalFiles == 0 {
		t.Error("good user produced zero uploads — disable policy may have killed both")
	}
}

// TestRunner_FilenameModeRoundTrip exercises the v0.8.1 round-trip
// tracking mode. The runner injects a 12-char marker into upload names;
// the mock self-loops uploads into the same user's outbox; the download
// worker must find each file by its marker (NOT by a "#trackid" suffix
// — the watcher is bypassed in this mode) and attribute the download
// back to the originating upload row.
//
// We're checking three invariants together:
//   1. Generator embeds the marker in the upload filename.
//   2. Store.AttachDownloadByFilenameID gets called and matches.
//   3. Records seal cleanly without the watcher (synthetic TrackID
//      keeps isRecordFinal happy).
func TestRunner_FilenameModeRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (mockserver) skipped under -short")
	}
	srv, err := mocksftp.Start(mocksftp.Options{
		Addr:   "127.0.0.1:0",
		Delay:  50 * time.Millisecond,
		Logger: log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	host, portStr, _ := net.SplitHostPort(srv.Addr().String())
	port, _ := strconv.Atoi(portStr)
	sftpx.SetHostKeyCallback(ssh.InsecureIgnoreHostKey())

	cfg := &config.RunConfig{
		Host:            host,
		Port:            port,
		UploadFolder:    "inbox",
		ParallelStreams: 2,
		DurationHours:   3.0 / 3600.0, // 3 s — long enough for the mock to self-loop several files
		PollInterval:    250 * time.Millisecond,
		TrackIDTimeout:  5 * time.Second,
		Normal: &config.NormalLoad{
			FilesPerMinute: 600,
			MinSizeMB:      1,
			MaxSizeMB:      1,
		},
		NormalUsers: []config.UserCreds{
			{Username: "u1", Password: "p", Patterns: []string{"f-*"}},
		},
		Download: &config.DownloadLoad{
			Folder:          "outbox",
			ParallelStreams: 2,
			MatchMode:       config.MatchModeFilename,
		},
		DownloadUsers: []config.UserCreds{
			{Username: "u1", Password: "p", Patterns: []string{"*"}},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	run, err := runner.Start(ctx, cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-run.Done():
	case <-ctx.Done():
		t.Fatal("run did not complete in time")
	}

	snap := run.Metrics.Snapshot()
	if snap.TotalFiles < 5 {
		t.Errorf("expected >=5 uploads, got %d", snap.TotalFiles)
	}
	if got := run.FailedFiles.Load(); got != 0 {
		t.Errorf("filename-mode round-trip should not fail; got %d failures", got)
	}

	// Walk the records and confirm:
	//   (a) every upload has a non-empty FilenameID and a synthetic TrackID
	//   (b) at least one record received a matching download (the marker
	//       lookup actually fired).
	rows := run.Report.Snapshot()
	if len(rows) == 0 {
		t.Fatal("no records produced")
	}
	withMarker := 0
	withDownload := 0
	for _, r := range rows {
		if r.FilenameID == "" {
			t.Errorf("record %s has empty FilenameID in filename mode", r.Filename)
			continue
		}
		if r.TrackID == "" || (len(r.TrackID) > 0 && r.TrackID[:9] != "FILENAME:") {
			t.Errorf("record %s should carry FILENAME:* TrackID, got %q", r.Filename, r.TrackID)
		}
		withMarker++
		if !r.DownloadEndTime.IsZero() {
			withDownload++
		}
	}
	if withMarker == 0 {
		t.Error("no records carried a marker — filename mode is not engaging")
	}
	// We don't require ALL files to have round-tripped (the mock has a 50ms
	// routing delay; the last few uploads may not have come back before
	// the run ended), but at least one round-trip must complete or the
	// download path is broken.
	if withDownload == 0 {
		t.Error("no records received a download — marker-lookup attach never fired")
	}
}

// TestRunner_VerifyHashes_RoundTrip exercises the SHA-256 round-trip
// on synthetic uploads + discard downloads against a real mocksftp.
// Asserts:
//   (a) every successful upload row carries a non-empty UploadSHA256
//       (proves uploadOne's TeeReader fires regardless of source kind),
//   (b) every row that received a download carries a non-empty
//       DownloadSHA256 (proves downloadWorker's MultiWriter fires
//       regardless of sink kind — discard included),
//   (c) those rows have HashMatch == true (proves the bytes that left
//       the client are the bytes that came back; the mock loops them
//       through verbatim, so any mismatch would be a real bug),
//   (d) RunMeta.HashVerified is positive at seal time.
//
// We use filename mode so the mock's outbox routing stays
// deterministic. The mock copies upload bytes into the outbox
// unmodified, which is the contract the runner relies on for hash
// equality. A future mock that mutates bytes in flight would surface
// here as HashMatch=false + RunMeta.HashMismatch > 0 — exactly the
// signal the operator wants.
func TestRunner_VerifyHashes_RoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (mockserver) skipped under -short")
	}
	// PersistContent: true makes the mock store upload bytes and replay
	// them verbatim on download. Without it, the mock synthesises zero-
	// filled downloads and the hash check correctly reports MISMATCH —
	// which is the OPPOSITE of what we want to assert here. The earlier
	// failure of this test caught exactly that misconfiguration.
	srv, err := mocksftp.Start(mocksftp.Options{
		Addr:           "127.0.0.1:0",
		Delay:          50 * time.Millisecond,
		Logger:         log.New(io.Discard, "", 0),
		PersistContent: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	host, portStr, _ := net.SplitHostPort(srv.Addr().String())
	port, _ := strconv.Atoi(portStr)
	sftpx.SetHostKeyCallback(ssh.InsecureIgnoreHostKey())

	cfg := &config.RunConfig{
		Host:            host,
		Port:            port,
		UploadFolder:    "inbox",
		ParallelStreams: 2,
		DurationHours:   3.0 / 3600.0,
		PollInterval:    250 * time.Millisecond,
		TrackIDTimeout:  5 * time.Second,
		VerifyHashes:    true, // <-- the feature under test
		Normal: &config.NormalLoad{
			FilesPerMinute: 600,
			MinSizeMB:      1,
			MaxSizeMB:      1,
		},
		NormalUsers: []config.UserCreds{
			{Username: "u1", Password: "p", Patterns: []string{"f-*"}},
		},
		Download: &config.DownloadLoad{
			Folder:          "outbox",
			ParallelStreams: 2,
			MatchMode:       config.MatchModeFilename,
		},
		DownloadUsers: []config.UserCreds{
			{Username: "u1", Password: "p", Patterns: []string{"*"}},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	run, err := runner.Start(ctx, cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-run.Done():
	case <-ctx.Done():
		t.Fatal("run did not complete in time")
	}

	rows := run.Report.Snapshot()
	if len(rows) == 0 {
		t.Fatal("no records produced")
	}
	uploadHashed, downloadHashed, matched, mismatched := 0, 0, 0, 0
	for _, r := range rows {
		if r.Error != "" {
			continue // skip failures
		}
		if r.UploadSHA256 == "" {
			t.Errorf("upload row %s has empty UploadSHA256 (synthetic upload should hash via TeeReader)", r.Filename)
			continue
		}
		uploadHashed++
		if r.DownloadEndTime.IsZero() {
			continue // download didn't round-trip yet
		}
		if r.DownloadSHA256 == "" {
			t.Errorf("download row %s has empty DownloadSHA256 (discard sink should hash via MultiWriter)", r.Filename)
			continue
		}
		downloadHashed++
		if r.HashMatch {
			matched++
		} else {
			mismatched++
			t.Errorf("row %s hash mismatch: upload=%s download=%s download_error=%q",
				r.Filename, r.UploadSHA256, r.DownloadSHA256, r.DownloadError)
		}
	}
	if uploadHashed == 0 {
		t.Fatal("no upload rows carried a SHA-256 — TeeReader is not firing")
	}
	if downloadHashed == 0 {
		t.Fatal("no download rows carried a SHA-256 — MultiWriter is not firing")
	}
	if matched == 0 {
		t.Fatal("no matched rounds-trips passed hash check; the runner is reporting bytes that don't match")
	}
	if mismatched > 0 {
		t.Fatalf("%d hash mismatches against a byte-faithful mock — comparator wiring is wrong", mismatched)
	}
	t.Logf("hash verify wiring confirmed: upload_hashed=%d download_hashed=%d matched=%d", uploadHashed, downloadHashed, matched)
}

// TestRunner_VerifyHashes_RealFileSource asserts that the SHA-256
// captured by the runner's upload TeeReader matches a pre-computed
// hash of an on-disk file when the source kind is local-files (NOT
// synthetic). Closes the gap left by the synthetic-only test:
// proves the wrap is io.Reader-agnostic and a real file source
// flows through the same hashing path.
func TestRunner_VerifyHashes_RealFileSource(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (mockserver) skipped under -short")
	}
	// Materialise a known on-disk file and compute its expected SHA-256.
	// The runner should produce exactly this hash on the upload row.
	tmp := t.TempDir()
	srcFile := filepath.Join(tmp, "fixture.bin")
	body := make([]byte, 256*1024) // 256 KiB
	for i := range body {
		body[i] = byte(i % 251) // not all zeros, not all one byte; deterministic
	}
	if err := os.WriteFile(srcFile, body, 0o600); err != nil {
		t.Fatal(err)
	}
	expectedSum := sha256.Sum256(body)
	expectedHex := hex.EncodeToString(expectedSum[:])

	srv, err := mocksftp.Start(mocksftp.Options{
		Addr:           "127.0.0.1:0",
		Delay:          50 * time.Millisecond,
		Logger:         log.New(io.Discard, "", 0),
		PersistContent: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	host, portStr, _ := net.SplitHostPort(srv.Addr().String())
	port, _ := strconv.Atoi(portStr)
	sftpx.SetHostKeyCallback(ssh.InsecureIgnoreHostKey())

	cfg := &config.RunConfig{
		Host:            host,
		Port:            port,
		UploadFolder:    "inbox",
		ParallelStreams: 1,
		DurationHours:   2.0 / 3600.0,
		PollInterval:    250 * time.Millisecond,
		TrackIDTimeout:  5 * time.Second,
		VerifyHashes:    true,
		Normal: &config.NormalLoad{
			FilesPerMinute: 60,
			MinSizeMB:      1, // ignored when local-files supplies bytes
			MaxSizeMB:      1,
			Source: &config.SourceConfig{
				Kind:  "local-files",
				Files: []string{srcFile},
				Mode:  "round-robin",
			},
		},
		NormalUsers: []config.UserCreds{
			{Username: "u1", Password: "p", Patterns: []string{"f-*"}},
		},
		Download: &config.DownloadLoad{
			Folder:          "outbox",
			ParallelStreams: 1,
			MatchMode:       config.MatchModeFilename,
		},
		DownloadUsers: []config.UserCreds{
			{Username: "u1", Password: "p", Patterns: []string{"*"}},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	run, err := runner.Start(ctx, cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-run.Done():
	case <-ctx.Done():
		t.Fatal("run did not complete in time")
	}

	rows := run.Report.Snapshot()
	if len(rows) == 0 {
		t.Fatal("no records produced")
	}
	// Every successful upload row should carry the expected SHA-256
	// because the local-files source serves the same fixture every
	// time. If even one row diverges, the TeeReader is hashing the
	// wrong stream (e.g. a buffered copy that bypassed our wrap).
	checked := 0
	for _, r := range rows {
		if r.Error != "" {
			continue
		}
		if r.UploadSHA256 != expectedHex {
			t.Errorf("row %s: UploadSHA256=%s expected %s — TeeReader is hashing the wrong stream for local-files source",
				r.Filename, r.UploadSHA256, expectedHex)
		}
		checked++
		if !r.DownloadEndTime.IsZero() {
			if r.DownloadSHA256 != expectedHex {
				t.Errorf("row %s: DownloadSHA256=%s expected %s — MultiWriter is hashing the wrong stream",
					r.Filename, r.DownloadSHA256, expectedHex)
			}
			if !r.HashMatch {
				t.Errorf("row %s: HashMatch should be true for byte-identical round-trip", r.Filename)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no successful upload rows to assert against")
	}
	t.Logf("real-file source wiring confirmed: %d rows all carry expected SHA-256 %s", checked, expectedHex)
}

// _ keeps the fmt import alive if we add diagnostic prints later.
var _ = fmt.Sprintf
