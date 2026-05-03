// Package alerts dispatches run-completion / threshold-breach
// notifications to Slack incoming webhooks, generic JSON webhooks,
// and SMTP email recipients.
//
// v0.15.0 — first cut. Configuration is global (one set of channels
// per server install) and stored as JSON under the reports directory.
// A future iteration may make alerts per-run; for now the global
// model is enough for "wake me up when prod regresses."
//
// The dispatcher is best-effort: a failing webhook never blocks the
// run from finishing or the next alert from firing. All errors are
// logged and the alert payload is dropped.
package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/smtp"
	"sync"
	"time"
)

// Config is the persisted alert configuration. Empty / missing fields
// disable the corresponding channel.
type Config struct {
	// SlackWebhookURL is an incoming-webhook URL of the form
	// https://hooks.slack.com/services/.../...  When set, alerts post
	// a Slack-formatted JSON payload to this URL.
	SlackWebhookURL string `json:"slack_webhook_url,omitempty"`

	// GenericWebhookURL is any HTTP endpoint that accepts a JSON POST
	// of the alert payload. Use this for PagerDuty Events API,
	// Opsgenie, Datadog Events, or your own bridge.
	GenericWebhookURL string `json:"generic_webhook_url,omitempty"`

	// Email — SMTP host:port + auth + recipient list. Skipped when
	// SMTPHost is empty.
	SMTPHost     string   `json:"smtp_host,omitempty"`
	SMTPPort     int      `json:"smtp_port,omitempty"`
	SMTPUser     string   `json:"smtp_user,omitempty"`
	SMTPPassword string   `json:"smtp_password,omitempty"`
	EmailFrom    string   `json:"email_from,omitempty"`
	EmailTo      []string `json:"email_to,omitempty"`

	// Triggers control which kinds of alert are dispatched.
	// AlertOnFailure: any run that ends with FailedFiles > 0.
	// AlertOnDispatchSkips: dispatch_skips > 0 (capacity exceeded).
	// AlertOnP99MS: p99 upload latency > this many ms (0 = disabled).
	// AlertOnErrorRatePct: failed/total > this % (0 = disabled).
	AlertOnFailure       bool    `json:"alert_on_failure"`
	AlertOnDispatchSkips bool    `json:"alert_on_dispatch_skips"`
	AlertOnP99MS         int     `json:"alert_on_p99_ms,omitempty"`
	AlertOnErrorRatePct  float64 `json:"alert_on_error_rate_pct,omitempty"`
}

// Anything tells whether at least one channel is configured. Used by
// the dispatcher to short-circuit when alerts are disabled run-wide.
func (c Config) Anything() bool {
	return c.SlackWebhookURL != "" || c.GenericWebhookURL != "" || (c.SMTPHost != "" && len(c.EmailTo) > 0)
}

// Event is the structured payload sent to webhooks (and rendered as
// the email body). Stable shape — customers' downstream pipelines
// can rely on it.
type Event struct {
	Kind         string    `json:"kind"`           // "run_complete", "threshold_breach"
	RunID        string    `json:"run_id"`
	StartedAt    time.Time `json:"started_at"`
	EndedAt      time.Time `json:"ended_at"`
	Host         string    `json:"host"`
	Protocol     string    `json:"protocol"`
	TotalFiles   int64     `json:"total_files"`
	FailedFiles  int64     `json:"failed_files"`
	TotalBytes   int64     `json:"total_bytes"`
	OverallMbps  float64   `json:"overall_mbps"`
	P99LatencyMS float64   `json:"p99_latency_ms"`
	DispatchSkips int64    `json:"dispatch_skips"`
	ErrorRate    float64   `json:"error_rate_pct"`
	// v0.18.0 — populated when the run was started with stop-reason
	// or hash verification active. StopReason is one of "duration",
	// "user", "speed-floor", "max-failures". HashMismatch is the
	// number of rows whose download SHA-256 differed from upload's.
	StopReason   string  `json:"stop_reason,omitempty"`
	StopDetail   string  `json:"stop_detail,omitempty"`
	HashMismatch int64   `json:"hash_mismatch,omitempty"`
	Reasons      []string `json:"reasons"` // human-readable list of triggers fired
}

// ShouldFire decides whether the given event matches any of the
// configured triggers. Returns the list of triggered reasons; empty
// slice means "no alert."
func (c Config) ShouldFire(ev Event) []string {
	var reasons []string
	if c.AlertOnFailure && ev.FailedFiles > 0 {
		reasons = append(reasons, fmt.Sprintf("failed_files=%d", ev.FailedFiles))
	}
	if c.AlertOnDispatchSkips && ev.DispatchSkips > 0 {
		reasons = append(reasons, fmt.Sprintf("dispatch_skips=%d", ev.DispatchSkips))
	}
	if c.AlertOnP99MS > 0 && ev.P99LatencyMS > float64(c.AlertOnP99MS) {
		reasons = append(reasons, fmt.Sprintf("p99=%.0fms (>%dms)", ev.P99LatencyMS, c.AlertOnP99MS))
	}
	if c.AlertOnErrorRatePct > 0 && ev.ErrorRate > c.AlertOnErrorRatePct {
		reasons = append(reasons, fmt.Sprintf("error_rate=%.2f%% (>%.2f%%)", ev.ErrorRate, c.AlertOnErrorRatePct))
	}
	// v0.18.0 — speed-floor and hash-mismatch are always-on triggers
	// when the operator opts into the underlying feature: the floor is
	// configured per-run, and verify_hashes implies "I want to know
	// when bytes don't match end-to-end". Both are urgent enough that
	// gating them behind another checkbox just adds friction.
	if ev.StopReason == "speed-floor" {
		if ev.StopDetail != "" {
			reasons = append(reasons, "speed-floor: "+ev.StopDetail)
		} else {
			reasons = append(reasons, "speed-floor stop")
		}
	}
	if ev.HashMismatch > 0 {
		reasons = append(reasons, fmt.Sprintf("hash_mismatch=%d", ev.HashMismatch))
	}
	return reasons
}

// Dispatcher fans out an Event to every configured channel
// concurrently. All errors are logged; the run never blocks.
type Dispatcher struct {
	cfg    Config
	client *http.Client
	mu     sync.Mutex
}

func NewDispatcher(cfg Config) *Dispatcher {
	return &Dispatcher{
		cfg:    cfg,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// Fire dispatches `ev` to all configured channels. The reasons list
// is decorative — the caller computed it via ShouldFire and now wants
// the alert sent. Returns immediately; channel work happens in the
// caller's goroutine sequentially (fast HTTP calls, slow SMTP) so the
// caller can choose to invoke this in a goroutine if it wants
// non-blocking delivery.
func (d *Dispatcher) Fire(ctx context.Context, ev Event) {
	d.mu.Lock()
	cfg := d.cfg
	d.mu.Unlock()
	ev.Reasons = append([]string{}, ev.Reasons...) // defensive copy
	if cfg.SlackWebhookURL != "" {
		if err := d.postSlack(ctx, cfg.SlackWebhookURL, ev); err != nil {
			log.Printf("alert: slack webhook failed: %v", err)
		}
	}
	if cfg.GenericWebhookURL != "" {
		if err := d.postJSON(ctx, cfg.GenericWebhookURL, ev); err != nil {
			log.Printf("alert: generic webhook failed: %v", err)
		}
	}
	if cfg.SMTPHost != "" && len(cfg.EmailTo) > 0 {
		if err := d.sendEmail(cfg, ev); err != nil {
			log.Printf("alert: email failed: %v", err)
		}
	}
}

// SlackPayload is the minimal Slack incoming-webhook shape. We use
// blocks for a richer card format; falls back to "text" if Slack
// rejects blocks for any reason.
type slackPayload struct {
	Text   string       `json:"text"`
	Blocks []slackBlock `json:"blocks,omitempty"`
}

type slackBlock struct {
	Type   string             `json:"type"`
	Text   *slackBlockText    `json:"text,omitempty"`
	Fields []slackBlockField  `json:"fields,omitempty"`
}

type slackBlockText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type slackBlockField struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (d *Dispatcher) postSlack(ctx context.Context, url string, ev Event) error {
	headline := fmt.Sprintf(":rotating_light: sftp-loadtest run %s — %s", ev.RunID, joinReasons(ev.Reasons))
	payload := slackPayload{
		Text: headline,
		Blocks: []slackBlock{
			{Type: "section", Text: &slackBlockText{Type: "mrkdwn", Text: "*" + headline + "*"}},
			{Type: "section", Fields: []slackBlockField{
				{Type: "mrkdwn", Text: fmt.Sprintf("*Host:*\n%s (%s)", ev.Host, ev.Protocol)},
				{Type: "mrkdwn", Text: fmt.Sprintf("*Files:*\n%d total / %d failed", ev.TotalFiles, ev.FailedFiles)},
				{Type: "mrkdwn", Text: fmt.Sprintf("*Throughput:*\n%.2f Mbps", ev.OverallMbps)},
				{Type: "mrkdwn", Text: fmt.Sprintf("*p99 latency:*\n%.0f ms", ev.P99LatencyMS)},
				{Type: "mrkdwn", Text: fmt.Sprintf("*Error rate:*\n%.2f%%", ev.ErrorRate)},
				{Type: "mrkdwn", Text: fmt.Sprintf("*Skips:*\n%d", ev.DispatchSkips)},
			}},
		},
	}
	return d.postJSONBody(ctx, url, payload)
}

func (d *Dispatcher) postJSON(ctx context.Context, url string, ev Event) error {
	return d.postJSONBody(ctx, url, ev)
}

func (d *Dispatcher) postJSONBody(ctx context.Context, url string, body any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "sftp-loadtest-alerts/0.15.0")
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	return nil
}

func (d *Dispatcher) sendEmail(cfg Config, ev Event) error {
	port := cfg.SMTPPort
	if port == 0 {
		port = 587
	}
	addr := fmt.Sprintf("%s:%d", cfg.SMTPHost, port)
	from := cfg.EmailFrom
	if from == "" {
		from = cfg.SMTPUser
	}
	subject := fmt.Sprintf("[sftp-loadtest] run %s — %s", ev.RunID, joinReasons(ev.Reasons))
	body := fmt.Sprintf(`Run %s on %s (%s) ended with the following triggers:

%s

Totals: %d files (%d failed), %.2f%% error rate
Throughput: %.2f Mbps
p99 latency: %.0f ms
Dispatch skips: %d
Started: %s
Ended:   %s
`, ev.RunID, ev.Host, ev.Protocol,
		joinReasons(ev.Reasons),
		ev.TotalFiles, ev.FailedFiles, ev.ErrorRate,
		ev.OverallMbps, ev.P99LatencyMS, ev.DispatchSkips,
		ev.StartedAt.Format(time.RFC3339), ev.EndedAt.Format(time.RFC3339))

	msg := []byte(fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\n%s",
		from, joinTo(cfg.EmailTo), subject, body))

	var auth smtp.Auth
	if cfg.SMTPUser != "" {
		auth = smtp.PlainAuth("", cfg.SMTPUser, cfg.SMTPPassword, cfg.SMTPHost)
	}
	return smtp.SendMail(addr, auth, from, cfg.EmailTo, msg)
}

func joinReasons(rs []string) string {
	if len(rs) == 0 {
		return "completed"
	}
	out := ""
	for i, r := range rs {
		if i > 0 {
			out += ", "
		}
		out += r
	}
	return out
}

func joinTo(to []string) string {
	out := ""
	for i, t := range to {
		if i > 0 {
			out += ", "
		}
		out += t
	}
	return out
}
