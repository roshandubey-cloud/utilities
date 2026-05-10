// Package rundoctor analyses one finished run against a chosen set
// of historical baselines and produces an LLM-generated diagnostic
// narrative. The package owns four concerns:
//
//   1. Comparability — which past runs are apples-to-apples for a
//      given focal run? The answer is "anything sealed against the
//      same (host, port, protocol) tuple" and nothing else. Comparing
//      throughput numbers across different destinations is
//      meaningless, so the filter is strict; missing host info
//      (legacy meta files written before v0.20.4) is treated as
//      not comparable to anything.
//
//   2. Redaction — every operator-typed string that could be
//      sensitive (target hostname, usernames in disabled-user rows,
//      stop-detail messages that quote operator config) is replaced
//      with stable opaque tokens before the prompt leaves the
//      process. Redaction is on by default; the UI exposes a toggle
//      so a privacy-relaxed local user can see un-redacted text in
//      the analysis (but the prompt actually sent is always the
//      redacted one — un-redaction is post-response).
//
//   3. Prompt assembly — given a focal run + N baselines, build a
//      compact, deterministic, model-friendly prompt with sections
//      the LLM can rely on (TARGET, FOCAL_RUN, BASELINES, DIFF) so
//      the resulting narrative cites real numbers from the metas.
//
//   4. Provider call — speak HTTP to the configured AI provider
//      (currently Anthropic Messages API) using a key the operator
//      stored in their encrypted vault under the well-known refs
//      "ai/provider" and "ai/api_key". The package does NOT touch
//      the vault directly; the caller (web handler) resolves the
//      key and hands it in. This keeps the package free of vault
//      dependencies and easy to test.
//
// Public surface (everything else is unexported):
//
//   ComparablePeers(focal, all []persist.RunMeta) []persist.RunMeta
//   Redact(s string, redactions map[string]string) (string, map[string]string)
//   BuildPrompt(focal persist.RunMeta, baselines []persist.RunMeta, redact bool) PromptResult
//   CallAnthropic(ctx, key, model, prompt) (string, error)
//
// PromptResult exposes both the redacted prompt that is actually
// sent and the redaction map (token → real value) so the UI can
// reverse the substitution on the response if the operator opts in.
package rundoctor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/persist"
)

// PromptResult is what BuildPrompt returns. SystemPrompt holds the
// expert framing the model needs to interpret tool-specific
// terminology. UserPrompt is the per-call body. PriorTurns carries
// any follow-up conversation history (assistant + user pairs) so
// the AI provider sees the same diagnostic thread the operator is
// reading. Redactions maps each opaque token (e.g. "<host_001>")
// back to its real value; callers may use it to un-redact the
// model's response when rendering to a privacy-relaxed UI.
type PromptResult struct {
	SystemPrompt string            `json:"system_prompt"`
	UserPrompt   string            `json:"user_prompt"`
	PriorTurns   []Turn            `json:"prior_turns,omitempty"`
	Redactions   map[string]string `json:"redactions,omitempty"`
}

// Turn is one prior message in a Run Doctor follow-up conversation.
// Mirrors the Anthropic Messages API's role/content shape so callers
// can hand it straight to the provider.
type Turn struct {
	Role    string `json:"role"`    // "user" | "assistant"
	Content string `json:"content"`
}

// Known model identifiers + a lightweight per-token cost hint
// (USD per million tokens, rough order-of-magnitude — kept here
// so the UI cost estimate stays accurate across model swaps).
type ModelInfo struct {
	ID            string  `json:"id"`
	Label         string  `json:"label"`
	USDPer1MIn    float64 `json:"usd_per_million_input"`
	USDPer1MOut   float64 `json:"usd_per_million_output"`
	Description   string  `json:"description"`
}

// KnownModels lists the Anthropic models the UI offers. Order =
// recommendation order (Haiku first because it's the fast/cheap
// default; operators escalate to Sonnet / Opus when a complex run
// needs deeper reasoning).
var KnownModels = []ModelInfo{
	{
		ID: "claude-haiku-4-5-20251001", Label: "Haiku 4.5 (fast, cheap — default)",
		USDPer1MIn: 1.00, USDPer1MOut: 5.00,
		Description: "Best for quick triage of routine runs.",
	},
	{
		ID: "claude-sonnet-4-6", Label: "Sonnet 4.6 (balanced)",
		USDPer1MIn: 3.00, USDPer1MOut: 15.00,
		Description: "Better at multi-baseline trend detection.",
	},
	{
		ID: "claude-opus-4-7", Label: "Opus 4.7 (deepest reasoning)",
		USDPer1MIn: 15.00, USDPer1MOut: 75.00,
		Description: "Only for hard cases — slow and expensive.",
	},
}

// EstimateCostUSD returns a rough cost estimate for a single call
// given the prompt char count and an expected response size. Char
// counts are converted to tokens at the standard ~4 chars/token
// approximation. Returns 0 when the model id is not in KnownModels.
func EstimateCostUSD(modelID string, promptChars, responseChars int) float64 {
	for _, m := range KnownModels {
		if m.ID == modelID {
			inTokens := float64(promptChars) / 4.0
			outTokens := float64(responseChars) / 4.0
			return (inTokens*m.USDPer1MIn + outTokens*m.USDPer1MOut) / 1_000_000.0
		}
	}
	return 0
}

// ComparablePeers returns the subset of `all` that targets the same
// (host, port, protocol) tuple as `focal`, sorted newest-first.
// Excludes focal itself. Runs missing target host info (legacy meta
// from before v0.20.4) are dropped — comparing them to anything
// would not be apples-to-apples.
func ComparablePeers(focal persist.RunMeta, all []persist.RunMeta) []persist.RunMeta {
	if focal.TargetHost == "" {
		return nil
	}
	out := make([]persist.RunMeta, 0, len(all))
	for _, m := range all {
		if m.ID == focal.ID {
			continue
		}
		if m.TargetHost == "" {
			continue
		}
		if !sameTarget(focal, m) {
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].StartedAt.After(out[j].StartedAt)
	})
	return out
}

// sameTarget compares the focal-vs-candidate tuple. Protocol is
// compared case-insensitively because operators sometimes type
// "SFTP" and sometimes "sftp" — both should match.
func sameTarget(a, b persist.RunMeta) bool {
	return a.TargetHost == b.TargetHost &&
		a.TargetPort == b.TargetPort &&
		strings.EqualFold(a.TargetProtocol, b.TargetProtocol)
}

// Redact substitutes a known set of sensitive tokens with stable
// opaque placeholders. The substitution is deterministic — the same
// real value always produces the same token within one Redact call,
// so a hostname appearing five times in the prompt yields five
// identical "<host_xx>" tokens. Returned redactions map carries
// token → real-value pairs the caller can later use to un-redact
// the LLM's response.
//
// Note: callers usually pass nil for `seed` and let Redact build
// a fresh map. Tests pass a pre-seeded map to assert specific
// token assignments.
func Redact(s string, seed map[string]string) (string, map[string]string) {
	if seed == nil {
		seed = map[string]string{}
	}
	// Reverse map: real → token. Build from seed for stability.
	rev := make(map[string]string, len(seed))
	for tok, real := range seed {
		rev[real] = tok
	}
	// Helper to allocate or reuse a token for a given real value
	// under a category prefix.
	alloc := func(category, real string) string {
		if real == "" {
			return ""
		}
		if tok, ok := rev[real]; ok {
			return tok
		}
		// Stable token derived from the category + a SHA-256 prefix
		// of the real value. Keeps tokens deterministic across calls
		// (so two prompts about the same host yield the same token)
		// without leaking ordering information.
		sum := sha256.Sum256([]byte(category + ":" + real))
		tok := fmt.Sprintf("<%s_%s>", category, hex.EncodeToString(sum[:3]))
		seed[tok] = real
		rev[real] = tok
		return tok
	}
	// We don't know which substrings of `s` are sensitive without
	// context, so callers should redact-then-format. This top-level
	// helper redacts known patterns:
	//   * Anything in seed (already-known real values).
	out := s
	for real, tok := range rev {
		if real == "" {
			continue
		}
		out = strings.ReplaceAll(out, real, tok)
	}
	_ = alloc // silence unused — exposed via redactMeta helper below.
	return out, seed
}

// redactMeta walks a RunMeta and applies redaction to every field
// the caller would later embed in a prompt. Returns a new meta with
// host / disabled-user / stop-detail fields rewritten, plus the
// redaction map so the caller can un-redact later if desired.
func redactMeta(m persist.RunMeta, seed map[string]string) (persist.RunMeta, map[string]string) {
	if seed == nil {
		seed = map[string]string{}
	}
	rev := make(map[string]string, len(seed))
	for tok, real := range seed {
		rev[real] = tok
	}
	tokenize := func(category, real string) string {
		if real == "" {
			return ""
		}
		if tok, ok := rev[real]; ok {
			return tok
		}
		sum := sha256.Sum256([]byte(category + ":" + real))
		tok := fmt.Sprintf("<%s_%s>", category, hex.EncodeToString(sum[:3]))
		seed[tok] = real
		rev[real] = tok
		return tok
	}
	out := m
	out.TargetHost = tokenize("host", m.TargetHost)
	dis := make([]persist.DisabledUser, len(m.Disabled))
	for i, d := range m.Disabled {
		dd := d
		dd.User = tokenize("user", d.User)
		dd.LastFile = tokenize("path", d.LastFile)
		dis[i] = dd
	}
	out.Disabled = dis
	if m.StopDetail != "" {
		// Stop-detail strings sometimes embed a username/host that
		// the redaction map already covers — apply the global
		// replace pass to catch those.
		s := m.StopDetail
		for real, tok := range rev {
			if real == "" {
				continue
			}
			s = strings.ReplaceAll(s, real, tok)
		}
		out.StopDetail = s
	}
	return out, seed
}

// BuildFollowupPrompt produces the prompt for a follow-up question
// in an existing diagnostic conversation. Convention:
//
//   * The FIRST user turn is always the structured prompt (focal +
//     baselines) so the model sees the run data even on the 5th
//     follow-up.  We synthesise it via BuildPrompt and stash it in
//     PriorTurns[0] as a user role.
//   * PriorTurns then alternates assistant / user across the
//     conversation history (oldest first, excluding the new
//     question).
//   * UserPrompt is the operator's NEW question — what the model
//     needs to answer in this call.
//
// `history` contains the assistant answers given so far AND the
// operator's prior follow-up questions, paired oldest-first.
// `newQuestion` is the operator's currently-typed question.
func BuildFollowupPrompt(focal persist.RunMeta, baselines []persist.RunMeta, history []Turn, newQuestion string, redact bool) PromptResult {
	base := BuildPrompt(focal, baselines, redact)
	turns := make([]Turn, 0, 1+len(history))
	turns = append(turns, Turn{Role: "user", Content: base.UserPrompt})
	turns = append(turns, history...)
	out := base
	out.PriorTurns = turns
	out.UserPrompt = newQuestion
	return out
}

// BuildPrompt assembles the system + user prompt for an analysis
// call. The user prompt is structured so the model can rely on
// section headers (## TARGET, ## FOCAL_RUN, ## BASELINES, ## DIFF)
// rather than parsing free-form prose. When `redact` is false the
// real values flow through; when true, redactMeta replaces sensitive
// fields with stable opaque tokens and the redaction map is returned
// so callers can reverse the substitution on the model response.
func BuildPrompt(focal persist.RunMeta, baselines []persist.RunMeta, redact bool) PromptResult {
	red := map[string]string{}
	if redact {
		focal, red = redactMeta(focal, red)
		out := make([]persist.RunMeta, len(baselines))
		for i, b := range baselines {
			out[i], red = redactMeta(b, red)
		}
		baselines = out
	}

	sys := strings.TrimSpace(`
You are Run Doctor, an SFTP / FTPS / FTP load-test diagnostic assistant
embedded in the sftp-loadtest tool. Your job is to read one finished
run's structured metadata plus an optional set of historical baseline
runs that hit the same destination (host, port, protocol) and produce
a plain-English diagnosis the operator can act on within minutes.

Always organise your response under these exact headings, in this order:

  ## What happened
  ## Why it slowed down or failed
  ## Compared to baseline(s)
  ## What to try next

Rules:
  * Cite real numbers from the metadata (success rate, p95, errors_by_code,
    peak_cpu_percent, dispatch_skips). Never invent values.
  * If a baseline section is empty say "no comparable historical runs"
    and skip the diff — do not fabricate.
  * Be concise: the operator is reading this between coffee sips.
    No more than ~250 words total.
  * Prefer concrete server-side suggestions ("raise MaxStartups", "check
    SFTP subsystem ulimit") over vague advice. Do not recommend changes
    that contradict the data.
  * Tokens like <host_a1b2> in the input represent redacted real values;
    pass them through unchanged when you need to refer to them.
`)

	var b bytes.Buffer
	b.WriteString("## TARGET\n")
	if focal.TargetHost == "" {
		b.WriteString("destination unknown (legacy meta, pre-v0.20.4)\n\n")
	} else {
		fmt.Fprintf(&b, "host=%s port=%d protocol=%s\n\n", focal.TargetHost, focal.TargetPort, focal.TargetProtocol)
	}
	b.WriteString("## FOCAL_RUN\n")
	b.WriteString(metaSummary(focal))
	b.WriteString("\n")

	if len(baselines) == 0 {
		b.WriteString("## BASELINES\n(no comparable historical runs targeting the same destination)\n\n")
		b.WriteString("## DIFF\n(no diff — no baselines)\n")
	} else {
		fmt.Fprintf(&b, "## BASELINES (%d run%s)\n", len(baselines), pluralS(len(baselines)))
		for i, base := range baselines {
			fmt.Fprintf(&b, "### baseline_%d  id=%s started=%s\n", i+1, base.ID, base.StartedAt.UTC().Format(time.RFC3339))
			b.WriteString(metaSummary(base))
			b.WriteString("\n")
		}
		b.WriteString("## DIFF\n")
		b.WriteString(diffSummary(focal, baselines))
	}

	user := strings.TrimSpace(b.String())
	out := PromptResult{
		SystemPrompt: sys,
		UserPrompt:   user,
	}
	if redact {
		out.Redactions = red
	}
	return out
}

// metaSummary emits a stable per-run block: id, time, workload shape,
// success/failure counts, error breakdown, latency p95/p99, infra
// peaks. One field per line, key=value, so the LLM can scan it
// without parsing punctuation.
func metaSummary(m persist.RunMeta) string {
	var b bytes.Buffer
	dur := m.StoppedAt.Sub(m.StartedAt)
	if dur < 0 {
		dur = 0
	}
	successRate := 0.0
	total := m.SucceededFiles + m.FailedFiles
	if total > 0 {
		successRate = float64(m.SucceededFiles) / float64(total) * 100
	}
	fmt.Fprintf(&b, "id=%s\n", m.ID)
	fmt.Fprintf(&b, "duration_s=%d\n", int(dur.Seconds()))
	fmt.Fprintf(&b, "files_attempted=%d files_succeeded=%d files_failed=%d success_rate_pct=%.1f\n",
		total, m.SucceededFiles, m.FailedFiles, successRate)
	fmt.Fprintf(&b, "total_bytes=%d overall_mbps=%.2f\n", m.TotalBytes, m.OverallMBps)
	fmt.Fprintf(&b, "workload upload_users=%d parallel_streams=%d files_per_minute=%d\n",
		m.UploadUsers, m.ParallelStreams, m.FilesPerMinute)
	fmt.Fprintf(&b, "download enabled=%t users=%d streams=%d match=%s\n",
		m.DownloadEnabled, m.DownloadUsers, m.DownloadParallelStreams, m.DownloadMatchMode)
	if m.DispatchSkips > 0 {
		fmt.Fprintf(&b, "dispatch_skips=%d (capacity ceiling hit)\n", m.DispatchSkips)
	} else {
		b.WriteString("dispatch_skips=0\n")
	}
	if len(m.ErrorsByCode) > 0 {
		// Stable key order so the LLM sees the same prompt for the
		// same input across runs (helps caching + reasoning).
		keys := make([]string, 0, len(m.ErrorsByCode))
		for k := range m.ErrorsByCode {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("errors_by_code:\n")
		for _, k := range keys {
			fmt.Fprintf(&b, "  %s=%d\n", k, m.ErrorsByCode[k])
		}
	}
	if m.HashVerified > 0 || m.HashMismatch > 0 {
		fmt.Fprintf(&b, "hash_verified=%d hash_mismatch=%d\n", m.HashVerified, m.HashMismatch)
	}
	if m.Latency != nil {
		if m.Latency.Upload != nil {
			fmt.Fprintf(&b, "upload_latency p50=%dms p95=%dms p99=%dms p999=%dms\n",
				m.Latency.Upload.P50/1e6, m.Latency.Upload.P95/1e6,
				m.Latency.Upload.P99/1e6, m.Latency.Upload.P999/1e6)
		}
		if m.Latency.UploadCOR != nil {
			fmt.Fprintf(&b, "upload_cor_latency p95=%dms (queue-wait corrected)\n",
				m.Latency.UploadCOR.P95/1e6)
		}
		if m.Latency.Dial != nil {
			fmt.Fprintf(&b, "dial_latency p95=%dms\n", m.Latency.Dial.P95/1e6)
		}
		if m.Latency.Download != nil && m.Latency.Download.Count > 0 {
			fmt.Fprintf(&b, "download_latency p95=%dms\n", m.Latency.Download.P95/1e6)
		}
	}
	fmt.Fprintf(&b, "host_peaks cpu=%.0f%% avg_cpu=%.0f%% peak_fd=%d peak_heap_mb=%.0f cores=%d\n",
		m.PeakCPUPercent, m.AvgCPUPercent, m.PeakFDInUse, m.PeakHeapMB, m.NumCPU)
	if m.StopReason != "" {
		fmt.Fprintf(&b, "stop_reason=%s\n", m.StopReason)
	}
	if m.StopDetail != "" {
		fmt.Fprintf(&b, "stop_detail=%q\n", m.StopDetail)
	}
	return b.String()
}

// diffSummary computes a few headline deltas between the focal run
// and the median baseline so the LLM has cheap, deterministic
// comparison numbers to anchor its narrative.
func diffSummary(focal persist.RunMeta, baselines []persist.RunMeta) string {
	if len(baselines) == 0 {
		return "(no baselines)\n"
	}
	median := func(vals []float64) float64 {
		if len(vals) == 0 {
			return 0
		}
		sort.Float64s(vals)
		mid := len(vals) / 2
		if len(vals)%2 == 0 {
			return (vals[mid-1] + vals[mid]) / 2
		}
		return vals[mid]
	}
	mbps := make([]float64, 0, len(baselines))
	successPct := make([]float64, 0, len(baselines))
	p95 := make([]float64, 0, len(baselines))
	for _, b := range baselines {
		mbps = append(mbps, b.OverallMBps)
		total := b.SucceededFiles + b.FailedFiles
		if total > 0 {
			successPct = append(successPct, float64(b.SucceededFiles)/float64(total)*100)
		}
		if b.Latency != nil && b.Latency.Upload != nil {
			p95 = append(p95, float64(b.Latency.Upload.P95)/1e6)
		}
	}
	mMBps := median(mbps)
	mSuccess := median(successPct)
	mP95 := median(p95)

	focalSuccess := 0.0
	total := focal.SucceededFiles + focal.FailedFiles
	if total > 0 {
		focalSuccess = float64(focal.SucceededFiles) / float64(total) * 100
	}
	focalP95 := 0.0
	if focal.Latency != nil && focal.Latency.Upload != nil {
		focalP95 = float64(focal.Latency.Upload.P95) / 1e6
	}
	var b strings.Builder
	fmt.Fprintf(&b, "vs_baseline_median (n=%d):\n", len(baselines))
	fmt.Fprintf(&b, "  overall_mbps focal=%.2f median=%.2f delta=%+.2f\n", focal.OverallMBps, mMBps, focal.OverallMBps-mMBps)
	fmt.Fprintf(&b, "  success_pct  focal=%.1f median=%.1f delta=%+.1f\n", focalSuccess, mSuccess, focalSuccess-mSuccess)
	fmt.Fprintf(&b, "  upload_p95_ms focal=%.0f median=%.0f delta=%+.0f\n", focalP95, mP95, focalP95-mP95)
	return b.String()
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// AnthropicRequest / AnthropicResponse mirror the Messages API
// fields we care about. We intentionally don't map the entire
// schema — only what's needed for a single-turn diagnostic call.
type anthropicRequest struct {
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
	System    string `json:"system,omitempty"`
	Messages  []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// CallAnthropic sends one prompt to the Anthropic Messages API and
// returns the assistant's text response. Caller supplies the API
// key (from the encrypted vault), the model name, and a context for
// cancellation. The function does not retry — the UI surfaces any
// error to the operator so they can decide whether to re-try.
func CallAnthropic(ctx context.Context, apiKey, model string, p PromptResult) (string, error) {
	if apiKey == "" {
		return "", fmt.Errorf("no API key configured")
	}
	if model == "" {
		model = "claude-haiku-4-5-20251001"
	}
	body := anthropicRequest{
		Model:     model,
		MaxTokens: 1024,
		System:    p.SystemPrompt,
	}
	// First-turn analysis: PriorTurns is empty, UserPrompt is the
	// structured prompt. Follow-up: PriorTurns is the full
	// conversation so far (oldest first, starting with the
	// structured prompt as a user message), and UserPrompt is the
	// operator's new question. Either way we emit PriorTurns +
	// UserPrompt in order.
	for _, t := range p.PriorTurns {
		body.Messages = append(body.Messages, struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{Role: t.Role, Content: t.Content})
	}
	body.Messages = append(body.Messages, struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}{Role: "user", Content: p.UserPrompt})

	buf, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.anthropic.com/v1/messages", bytes.NewReader(buf))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("anthropic request: %w", err)
	}
	defer resp.Body.Close()

	var out anthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode anthropic response: %w", err)
	}
	if out.Error != nil {
		return "", fmt.Errorf("anthropic %s: %s", out.Error.Type, out.Error.Message)
	}
	if len(out.Content) == 0 {
		return "", fmt.Errorf("anthropic returned no content")
	}
	var text strings.Builder
	for _, blk := range out.Content {
		if blk.Type == "text" {
			text.WriteString(blk.Text)
		}
	}
	return strings.TrimSpace(text.String()), nil
}
