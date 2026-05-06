package runner_test

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/config"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/mocksftp"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/persist"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/runner"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/sftpx"
)

// TestRunMeta_DownloadFieldsPopulated is the v0.19.12 pin: when a run
// completes with downloads enabled, the sealed RunMeta JSON MUST carry
// download_completed, latency.download (with non-zero count), and an
// errors_by_code map (when any errors were logged). Without this pin the
// 8 h hash-verify report goes out missing the very fields the operator
// reads first.
func TestRunMeta_DownloadFieldsPopulated(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (mockserver) skipped under -short")
	}
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

	reportsDir := t.TempDir()

	cfg := &config.RunConfig{
		Host:            host,
		Port:            port,
		UploadFolder:    "inbox",
		ParallelStreams: 2,
		DurationHours:   3.0 / 3600.0,
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
	run, err := runner.StartWithPersist(ctx, cfg, reportsDir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-run.Done():
	case <-ctx.Done():
		t.Fatal("run did not complete in time")
	}

	metaPath := persist.MetaPath(reportsDir, run.ID)
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	var meta persist.RunMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("unmarshal meta: %v", err)
	}

	if meta.DownloadCompleted == 0 {
		t.Fatalf("download_completed is zero — counter not persisted (raw: %s)", string(raw))
	}
	if meta.Latency == nil || meta.Latency.Download == nil {
		t.Fatalf("latency.download missing from meta (raw: %s)", string(raw))
	}
	if meta.Latency.Download.Count == 0 {
		t.Fatalf("latency.download.count is zero — histogram not observed (raw: %s)", string(raw))
	}
	if meta.Latency.Download.Mean <= 0 {
		t.Fatalf("latency.download.mean_ns must be positive: %d", meta.Latency.Download.Mean)
	}
	// errors_by_code is optional — a clean run can have none. Just assert
	// the field is at least serialised when non-empty so the contract is
	// stable. We can't force errors deterministically in this test, so
	// only validate when present.
	if len(meta.ErrorsByCode) > 0 {
		for code, n := range meta.ErrorsByCode {
			if n <= 0 {
				t.Fatalf("errors_by_code[%s]=%d should be positive when present", code, n)
			}
		}
	}
}
