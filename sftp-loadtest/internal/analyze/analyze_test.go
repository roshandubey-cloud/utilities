package analyze

import (
	"strings"
	"testing"

	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/persist"
)

// findSuggestion returns the first suggestion whose title contains needle,
// or nil. Lets each test focus on the rule it is exercising rather than
// pinning slice indexes that shift as new rules are added.
func findSuggestion(out []persist.Suggestion, needle string) *persist.Suggestion {
	for i := range out {
		if strings.Contains(strings.ToLower(out[i].Title), strings.ToLower(needle)) {
			return &out[i]
		}
	}
	return nil
}

func TestSuggest_NoIssues_EmptyOnTinyRun(t *testing.T) {
	// Tiny run with no skips, no failures, no infra peaks → no findings.
	out := Suggest(persist.RunMeta{TotalFiles: 5, FailedFiles: 0})
	if len(out) != 0 {
		t.Fatalf("want 0 suggestions on a clean tiny run, got %d: %+v", len(out), out)
	}
}

func TestSuggest_CapacityCeiling_HostHasHeadroom(t *testing.T) {
	m := persist.RunMeta{
		TotalFiles:      800,
		DispatchSkips:   200,
		ParallelStreams: 2,
		FilesPerMinute:  600,
		PeakCPUPercent:  35, // headroom
		NumCPU:          8,
	}
	out := Suggest(m)
	s := findSuggestion(out, "Capacity ceiling")
	if s == nil {
		t.Fatalf("missing capacity-ceiling finding: %+v", out)
	}
	if s.Severity != SeverityWarn {
		t.Errorf("severity = %s, want warn (skip pct ~20)", s.Severity)
	}
	if !strings.Contains(strings.ToLower(s.Action), "raise parallel_streams") {
		t.Errorf("expected action to recommend raising parallel_streams when CPU has headroom, got: %s", s.Action)
	}
}

func TestSuggest_CapacityCeiling_HostCPUPinned(t *testing.T) {
	m := persist.RunMeta{
		TotalFiles:      800,
		DispatchSkips:   200,
		ParallelStreams: 4,
		FilesPerMinute:  600,
		PeakCPUPercent:  92, // pinned
		NumCPU:          4,
	}
	out := Suggest(m)
	s := findSuggestion(out, "Capacity ceiling")
	if s == nil {
		t.Fatal("missing capacity-ceiling finding")
	}
	if !strings.Contains(strings.ToLower(s.Action), "reduce files_per_minute") {
		t.Errorf("when CPU is pinned the analyzer must steer to lower fpm, got: %s", s.Action)
	}
}

func TestSuggest_CapacityCeiling_Critical(t *testing.T) {
	// >25% skips → critical.
	m := persist.RunMeta{
		TotalFiles:      600,
		DispatchSkips:   400, // 40% of attempted
		ParallelStreams: 1,
		PeakCPUPercent:  20,
		NumCPU:          8,
		FilesPerMinute:  900,
	}
	out := Suggest(m)
	s := findSuggestion(out, "Capacity ceiling")
	if s == nil || s.Severity != SeverityCritical {
		t.Fatalf("want critical capacity-ceiling, got %+v", s)
	}
}

func TestSuggest_HighFailureRate(t *testing.T) {
	m := persist.RunMeta{TotalFiles: 100, FailedFiles: 30}
	out := Suggest(m)
	s := findSuggestion(out, "failure rate")
	if s == nil {
		t.Fatal("missing failure-rate finding")
	}
	if s.Severity != SeverityCritical {
		t.Errorf("30%% failure should be critical, got %s", s.Severity)
	}
}

func TestSuggest_NetworkLimited(t *testing.T) {
	// 100 files of 10 MiB at 600 fpm = 100 MB/s expected. Peak is 30 MB/s,
	// no skips → network limit.
	m := persist.RunMeta{
		TotalFiles:     100,
		TotalBytes:     int64(100) * 10 * 1024 * 1024,
		FilesPerMinute: 600,
		OverallMBps:    30,
		PeakWindowMBps: 30,
	}
	out := Suggest(m)
	s := findSuggestion(out, "Network throughput")
	if s == nil {
		t.Fatalf("missing network-limited finding: %+v", out)
	}
}

func TestSuggest_NetworkLimited_NotFiredIfSkipsPresent(t *testing.T) {
	// Same numbers but with skips: capacity is the diagnosis, not network.
	m := persist.RunMeta{
		TotalFiles:     100,
		TotalBytes:     int64(100) * 10 * 1024 * 1024,
		FilesPerMinute: 600,
		OverallMBps:    30,
		PeakWindowMBps: 30,
		DispatchSkips:  20,
	}
	out := Suggest(m)
	if findSuggestion(out, "Network throughput") != nil {
		t.Error("network-limited rule must defer to capacity-ceiling when skips > 0")
	}
}

func TestSuggest_DownloadsStalled(t *testing.T) {
	m := persist.RunMeta{
		TotalFiles:              200,
		DownloadEnabled:         true, // gate added in v0.13.29 — without this, the suggestion is intentionally skipped
		DownloadStalled:         60,
		DownloadParallelStreams: 1,
		DownloadUsers:           1,
	}
	out := Suggest(m)
	s := findSuggestion(out, "Downloads stalled")
	if s == nil {
		t.Fatal("missing downloads-stalled finding")
	}
	if s.Severity != SeverityCritical {
		t.Errorf("30%% stalled is critical, got %s", s.Severity)
	}
}

// TestSuggest_DownloadsStalled_DisabledIsSkipped pins the v0.13.29 gate:
// even with non-zero DownloadStalled, the suggestion must NOT fire when
// DownloadEnabled is false (the run never had downloads — a stalled
// counter would be a stale artifact, not a real signal).
func TestSuggest_DownloadsStalled_DisabledIsSkipped(t *testing.T) {
	m := persist.RunMeta{
		TotalFiles:      200,
		DownloadEnabled: false,
		DownloadStalled: 60,
	}
	out := Suggest(m)
	if findSuggestion(out, "Downloads stalled") != nil {
		t.Error("downloads-stalled fired despite DownloadEnabled=false")
	}
}

func TestSuggest_FDPressure(t *testing.T) {
	m := persist.RunMeta{TotalFiles: 10, PeakFDInUse: 5000}
	out := Suggest(m)
	s := findSuggestion(out, "file-descriptor")
	if s == nil || s.Severity != SeverityCritical {
		t.Fatalf("FD ≥ 4000 should be critical, got %+v", s)
	}
}

func TestSuggest_HeadroomPositive(t *testing.T) {
	m := persist.RunMeta{
		TotalFiles:      500,
		FailedFiles:     0,
		DispatchSkips:   0,
		FilesPerMinute:  120,
		ParallelStreams: 2,
		PeakCPUPercent:  20,
	}
	out := Suggest(m)
	s := findSuggestion(out, "headroom")
	if s == nil {
		t.Fatal("clean small run should produce a positive headroom hint")
	}
	if s.Severity != SeverityInfo {
		t.Errorf("headroom hint must be info, got %s", s.Severity)
	}
}

func TestSuggestParallel_ScalesByObservedSkipRatio(t *testing.T) {
	m := persist.RunMeta{TotalFiles: 800, DispatchSkips: 200, ParallelStreams: 2}
	if got := suggestParallel(m); got <= 2 {
		t.Errorf("with 20%% skip and current=2 streams the suggestion must be > 2, got %d", got)
	}
}

func TestSuggestFpmFromCPU_BackCalculatesAt70Pct(t *testing.T) {
	// At PeakCPU=90 we should suggest fpm * (70/90) = 466 (rounded).
	m := persist.RunMeta{FilesPerMinute: 600, PeakCPUPercent: 90}
	got := suggestFpmFromCPU(m)
	if got < 450 || got > 480 {
		t.Errorf("expected ~466, got %d", got)
	}
}
