package runner_test

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
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

// _ keeps the fmt import alive if we add diagnostic prints later.
var _ = fmt.Sprintf
