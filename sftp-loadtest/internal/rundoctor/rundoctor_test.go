package rundoctor

import (
	"strings"
	"testing"
	"time"

	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/persist"
)

func mkMeta(id, host string, port int, proto string, mbps float64, success, failed int64, started time.Time) persist.RunMeta {
	return persist.RunMeta{
		ID:             id,
		StartedAt:      started,
		StoppedAt:      started.Add(60 * time.Second),
		TargetHost:     host,
		TargetPort:     port,
		TargetProtocol: proto,
		OverallMBps:    mbps,
		TotalFiles:     success + failed,
		SucceededFiles: success,
		FailedFiles:    failed,
	}
}

func TestComparablePeers_FiltersByHostPortProtocol(t *testing.T) {
	now := time.Now()
	focal := mkMeta("focal", "edge.acme.com", 22, "sftp", 90, 1000, 0, now)
	all := []persist.RunMeta{
		focal,                                                                      // self — excluded
		mkMeta("p1", "edge.acme.com", 22, "sftp", 95, 980, 20, now.Add(-1*time.Hour)),
		mkMeta("p2", "edge.acme.com", 22, "sftp", 110, 990, 10, now.Add(-2*time.Hour)),
		mkMeta("p3", "other.acme.com", 22, "sftp", 50, 1000, 0, now.Add(-30*time.Minute)), // wrong host
		mkMeta("p4", "edge.acme.com", 990, "ftps", 30, 1000, 0, now.Add(-15*time.Minute)), // wrong protocol+port
		mkMeta("p5", "", 0, "", 100, 1000, 0, now.Add(-10*time.Minute)),                  // legacy — no host
	}
	got := ComparablePeers(focal, all)
	if len(got) != 2 {
		t.Fatalf("want 2 peers, got %d: %#v", len(got), ids(got))
	}
	if got[0].ID != "p1" || got[1].ID != "p2" {
		t.Fatalf("peers should be sorted newest-first: got %v", ids(got))
	}
}

func TestComparablePeers_ProtocolCaseInsensitive(t *testing.T) {
	now := time.Now()
	focal := mkMeta("focal", "h", 22, "SFTP", 1, 1, 0, now)
	all := []persist.RunMeta{mkMeta("p", "h", 22, "sftp", 1, 1, 0, now.Add(-time.Minute))}
	if got := ComparablePeers(focal, all); len(got) != 1 {
		t.Fatalf("case-insensitive protocol match should yield 1 peer, got %d", len(got))
	}
}

func TestComparablePeers_FocalMissingHost_NoPeers(t *testing.T) {
	now := time.Now()
	focal := mkMeta("focal", "", 0, "", 1, 1, 0, now)
	all := []persist.RunMeta{mkMeta("p", "h", 22, "sftp", 1, 1, 0, now.Add(-time.Minute))}
	if got := ComparablePeers(focal, all); len(got) != 0 {
		t.Fatalf("focal without host should never compare to anything; got %d peers", len(got))
	}
}

func TestRedactMeta_HostUserPath_TokensStableAcrossCalls(t *testing.T) {
	now := time.Now()
	m1 := persist.RunMeta{
		ID:             "r1",
		TargetHost:     "edge.acme.com",
		TargetPort:     22,
		TargetProtocol: "sftp",
		StartedAt:      now,
		StoppedAt:      now.Add(time.Minute),
		Disabled: []persist.DisabledUser{{User: "loadtest-1", LastFile: "/incoming/abc.csv"}},
	}
	r1, m1Map := redactMeta(m1, nil)
	if r1.TargetHost == "edge.acme.com" {
		t.Fatalf("host should be tokenized; got %q", r1.TargetHost)
	}
	if !strings.HasPrefix(r1.TargetHost, "<host_") {
		t.Fatalf("host token format wrong: %q", r1.TargetHost)
	}
	if r1.Disabled[0].User == "loadtest-1" {
		t.Fatalf("user should be tokenized; got %q", r1.Disabled[0].User)
	}
	if r1.Disabled[0].LastFile == "/incoming/abc.csv" {
		t.Fatalf("path should be tokenized; got %q", r1.Disabled[0].LastFile)
	}

	// Second call with a meta carrying the SAME values must reuse
	// the same tokens, so the LLM sees stable identifiers across
	// the focal + baseline blocks.
	m2 := m1
	m2.ID = "r2"
	r2, _ := redactMeta(m2, m1Map)
	if r2.TargetHost != r1.TargetHost {
		t.Fatalf("redact should be deterministic across calls: %q vs %q", r1.TargetHost, r2.TargetHost)
	}
	if r2.Disabled[0].User != r1.Disabled[0].User {
		t.Fatalf("user redaction not stable: %q vs %q", r1.Disabled[0].User, r2.Disabled[0].User)
	}
}

func TestBuildPrompt_ContainsRequiredSections(t *testing.T) {
	now := time.Now()
	focal := mkMeta("focal", "edge.acme.com", 22, "sftp", 92.5, 980, 20, now)
	bases := []persist.RunMeta{
		mkMeta("b1", "edge.acme.com", 22, "sftp", 110.0, 1000, 0, now.Add(-1*time.Hour)),
	}
	pr := BuildPrompt(focal, bases, false)
	for _, want := range []string{"## TARGET", "## FOCAL_RUN", "## BASELINES", "## DIFF", "edge.acme.com"} {
		if !strings.Contains(pr.UserPrompt, want) {
			t.Fatalf("prompt missing %q\n---\n%s", want, pr.UserPrompt)
		}
	}
	if pr.Redactions != nil {
		t.Fatalf("redactions should be nil when redact=false")
	}
}

func TestBuildPrompt_RedactedHidesHost(t *testing.T) {
	now := time.Now()
	focal := mkMeta("focal", "edge.acme.com", 22, "sftp", 92.5, 980, 20, now)
	pr := BuildPrompt(focal, nil, true)
	if strings.Contains(pr.UserPrompt, "edge.acme.com") {
		t.Fatalf("redacted prompt must not contain raw hostname: %s", pr.UserPrompt)
	}
	if pr.Redactions == nil || len(pr.Redactions) == 0 {
		t.Fatalf("redaction map should be populated")
	}
	// The map must contain the original host as a value.
	found := false
	for _, real := range pr.Redactions {
		if real == "edge.acme.com" {
			found = true
		}
	}
	if !found {
		t.Fatalf("redaction map missing original host; got %#v", pr.Redactions)
	}
}

func TestBuildPrompt_NoBaselinesEmitsExplicitMessage(t *testing.T) {
	now := time.Now()
	focal := mkMeta("focal", "h", 22, "sftp", 1, 1, 0, now)
	pr := BuildPrompt(focal, nil, false)
	if !strings.Contains(pr.UserPrompt, "no comparable historical runs") {
		t.Fatalf("zero-baseline prompt must say so explicitly: %s", pr.UserPrompt)
	}
}

func ids(ms []persist.RunMeta) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}

func TestBuildFollowupPrompt_SeedsStructuredPromptAsFirstUserTurn(t *testing.T) {
	now := time.Now()
	focal := mkMeta("focal", "h", 22, "sftp", 90, 1000, 0, now)
	history := []Turn{
		{Role: "assistant", Content: "first answer"},
		{Role: "user", Content: "first follow-up"},
		{Role: "assistant", Content: "second answer"},
	}
	pr := BuildFollowupPrompt(focal, nil, history, "third question", false)

	if pr.UserPrompt != "third question" {
		t.Fatalf("UserPrompt should be the new question; got %q", pr.UserPrompt)
	}
	if len(pr.PriorTurns) != 1+len(history) {
		t.Fatalf("PriorTurns should have %d entries (structured prompt + history); got %d", 1+len(history), len(pr.PriorTurns))
	}
	if pr.PriorTurns[0].Role != "user" {
		t.Fatalf("first PriorTurn must be the structured prompt as user role; got %q", pr.PriorTurns[0].Role)
	}
	if !strings.Contains(pr.PriorTurns[0].Content, "## TARGET") {
		t.Fatalf("first PriorTurn must carry the structured prompt; got %q", pr.PriorTurns[0].Content[:80])
	}
	// Subsequent turns preserve order.
	for i, want := range history {
		got := pr.PriorTurns[i+1]
		if got.Role != want.Role || got.Content != want.Content {
			t.Fatalf("PriorTurns[%d] mismatch: got %+v want %+v", i+1, got, want)
		}
	}
}

func TestEstimateCostUSD_KnownAndUnknownModels(t *testing.T) {
	cost := EstimateCostUSD("claude-haiku-4-5-20251001", 4000, 800)
	if cost <= 0 {
		t.Fatalf("known model should produce a positive estimate; got %f", cost)
	}
	if EstimateCostUSD("not-a-real-model", 4000, 800) != 0 {
		t.Fatalf("unknown model should produce zero estimate")
	}
	// Sonnet should cost more than Haiku for the same payload.
	cHaiku := EstimateCostUSD("claude-haiku-4-5-20251001", 10000, 2000)
	cSonnet := EstimateCostUSD("claude-sonnet-4-6", 10000, 2000)
	if cSonnet <= cHaiku {
		t.Fatalf("Sonnet should be more expensive than Haiku; got Sonnet=%f Haiku=%f", cSonnet, cHaiku)
	}
}
