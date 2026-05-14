package config

import (
	"strings"
	"testing"
)

// TestValidate_DownloadOnly_Accepted locks in the v0.20.9 fix:
// a run config with ONLY download enabled (no normal, no large-file)
// must pass validation. Operators legitimately want to measure
// download throughput against pre-staged files; pre-v0.20.9 the
// validator rejected this with "enable at least one of normal-load
// or large-file-load", which contradicted what third-party SFTP
// tools allow and frustrated MoveIT-server users.
func TestValidate_DownloadOnly_Accepted(t *testing.T) {
	cfg := &RunConfig{
		Host:          "h",
		Port:          22,
		UploadFolder:  "/in",
		DurationHours: 1,
		Download: &DownloadLoad{
			ParallelStreams: 1,
			Folder:          "/out",
			MatchMode:       "filename",
			Sink:            &SinkConfig{Kind: "discard"},
		},
		DownloadUsers: []UserCreds{{Username: "u", Password: "p"}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("download-only run config should validate; got %v", err)
	}
}

// TestValidate_NoLoadAtAll_Rejected keeps the universe of valid
// configs bounded: a config with NONE of normal / large / download
// is still rejected, with the error message pointing at the
// missing-load problem so operators know which knob to flip.
func TestValidate_NoLoadAtAll_Rejected(t *testing.T) {
	cfg := &RunConfig{
		Host:          "h",
		Port:          22,
		UploadFolder:  "/in",
		DurationHours: 1,
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatalf("config with no load enabled must error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "load") {
		t.Fatalf("error must mention the missing load; got %q", err)
	}
}
