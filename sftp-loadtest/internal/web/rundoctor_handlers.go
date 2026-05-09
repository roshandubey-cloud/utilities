// rundoctor_handlers.go — HTTP surface for the Run Doctor AI
// diagnostic feature. Three endpoints:
//
//   GET  /api/run-doctor/config
//        → { configured, provider, model } — does the vault hold an
//          AI key and which provider is selected? UI surfaces the
//          "set up your AI provider" call-to-action when not.
//
//   POST /api/run-doctor/config
//        body: { provider, api_key, model? }
//        → stores the key in the encrypted vault under refs
//          "ai/api_key" and "ai/provider"; key never leaves the
//          server in plaintext after this. Idempotent.
//
//   GET  /api/run-doctor/peers?id=<runID>
//        → { focal_run, peers: [...] } — every historical run
//          targeting the same (host, port, protocol) tuple as the
//          focal run, newest first. UI populates the "compare
//          against" picker from this. Honours the apples-to-apples
//          rule: legacy runs without target host are excluded.
//
//   POST /api/run-doctor/analyze
//        body: { run_id, compare_ids?, redact?, dry_run? }
//        → If dry_run=true, returns the prompt that would be sent
//          (so the operator can preview before paying tokens).
//          Otherwise calls the configured AI provider and returns
//          the narrative. compare_ids may be empty (server picks
//          the 5 most-recent same-host peers automatically) or a
//          specific list (operator picked a particular date from
//          the picker).
package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/persist"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/rundoctor"
)

// Vault refs where the AI provider config lives. Keeping them as
// constants here so any future provider rotation only changes one
// file.
const (
	vaultRefAIProvider = "ai/provider"
	vaultRefAIKey      = "ai/api_key"
	vaultRefAIModel    = "ai/model"
)

// handleRunDoctorConfig returns whether the operator has stored an
// AI key + which provider is selected. Never returns the key
// itself — the UI just needs to know "is this set up?" so it can
// show the right call-to-action. Status endpoint, GET only.
func (s *Server) handleRunDoctorConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		v := s.vaultBinder.get()
		if v == nil {
			writeJSON(w, map[string]any{"configured": false, "vault_unlocked": false})
			return
		}
		_, hasKey := v.Get(vaultRefAIKey)
		provider, _ := v.Get(vaultRefAIProvider)
		model, _ := v.Get(vaultRefAIModel)
		if provider == "" {
			provider = "anthropic"
		}
		writeJSON(w, map[string]any{
			"configured":     hasKey,
			"vault_unlocked": true,
			"provider":       provider,
			"model":          model,
		})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	v := s.vaultBinder.get()
	if v == nil {
		http.Error(w, "vault is locked — unlock from Trust → Vault before saving an AI key", http.StatusForbidden)
		return
	}
	var body struct {
		Provider string `json:"provider"`
		APIKey   string `json:"api_key"`
		Model    string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	provider := strings.TrimSpace(strings.ToLower(body.Provider))
	if provider == "" {
		provider = "anthropic"
	}
	if provider != "anthropic" {
		http.Error(w, "unsupported provider: only 'anthropic' currently", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.APIKey) == "" {
		http.Error(w, "api_key is required", http.StatusBadRequest)
		return
	}
	if err := v.Set(vaultRefAIProvider, provider); err != nil {
		http.Error(w, "vault set provider: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := v.Set(vaultRefAIKey, strings.TrimSpace(body.APIKey)); err != nil {
		http.Error(w, "vault set key: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if body.Model != "" {
		if err := v.Set(vaultRefAIModel, strings.TrimSpace(body.Model)); err != nil {
			http.Error(w, "vault set model: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if err := v.Save(); err != nil {
		http.Error(w, "vault save: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleRunDoctorPeers returns same-host peers for a given run ID.
// Apples-to-apples: identical (target_host, target_port,
// target_protocol). Excludes the focal run. Drops anything missing
// host info (legacy runs sealed before v0.20.4).
func (s *Server) handleRunDoctorPeers(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	if s.reportsDir == "" {
		http.Error(w, "no reports directory configured", http.StatusServiceUnavailable)
		return
	}
	all, err := persist.ListMeta(s.reportsDir)
	if err != nil {
		http.Error(w, "list meta: "+err.Error(), http.StatusInternalServerError)
		return
	}
	var focal persist.RunMeta
	found := false
	for _, m := range all {
		if m.ID == id {
			focal = m
			found = true
			break
		}
	}
	if !found {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	peers := rundoctor.ComparablePeers(focal, all)
	writeJSON(w, map[string]any{
		"focal_run": focalSummary(focal),
		"peers":     peerSummaries(peers),
	})
}

// handleRunDoctorAnalyze runs the focal-vs-baselines diagnostic.
// Body: run_id (required), compare_ids (optional — empty = server
// picks 5 newest same-host peers), redact (default true), dry_run
// (default false — when true, no AI call, returns the prompt only).
func (s *Server) handleRunDoctorAnalyze(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		RunID      string   `json:"run_id"`
		CompareIDs []string `json:"compare_ids"`
		Redact     *bool    `json:"redact"`
		DryRun     bool     `json:"dry_run"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.RunID == "" {
		http.Error(w, "run_id is required", http.StatusBadRequest)
		return
	}
	if s.reportsDir == "" {
		http.Error(w, "no reports directory configured", http.StatusServiceUnavailable)
		return
	}
	all, err := persist.ListMeta(s.reportsDir)
	if err != nil {
		http.Error(w, "list meta: "+err.Error(), http.StatusInternalServerError)
		return
	}
	var focal persist.RunMeta
	found := false
	byID := make(map[string]persist.RunMeta, len(all))
	for _, m := range all {
		byID[m.ID] = m
		if m.ID == body.RunID {
			focal = m
			found = true
		}
	}
	if !found {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}

	// Resolve comparison set. Empty ⇒ default to all comparable peers,
	// truncated to the 5 most recent (so the prompt doesn't blow past
	// the model's context budget on a long-running operator's history).
	const defaultMaxPeers = 5
	var baselines []persist.RunMeta
	if len(body.CompareIDs) == 0 {
		all := rundoctor.ComparablePeers(focal, all)
		if len(all) > defaultMaxPeers {
			all = all[:defaultMaxPeers]
		}
		baselines = all
	} else {
		seen := make(map[string]bool, len(body.CompareIDs))
		for _, id := range body.CompareIDs {
			if seen[id] {
				continue
			}
			seen[id] = true
			m, ok := byID[id]
			if !ok {
				continue
			}
			// Operator-picked peers must still be apples-to-apples; we
			// silently drop any that don't match the focal target so a
			// stale picker selection can't poison the analysis.
			if m.TargetHost == "" || m.TargetHost != focal.TargetHost ||
				m.TargetPort != focal.TargetPort ||
				!strings.EqualFold(m.TargetProtocol, focal.TargetProtocol) {
				continue
			}
			baselines = append(baselines, m)
		}
	}

	redact := true
	if body.Redact != nil {
		redact = *body.Redact
	}
	prompt := rundoctor.BuildPrompt(focal, baselines, redact)

	if body.DryRun {
		writeJSON(w, map[string]any{
			"focal_run":     focalSummary(focal),
			"baseline_runs": peerSummaries(baselines),
			"prompt":        prompt,
			"dry_run":       true,
		})
		return
	}

	// Real call — pull the AI key from the encrypted vault.
	v := s.vaultBinder.get()
	if v == nil {
		http.Error(w, "vault is locked — unlock from Trust → Vault, then re-run analysis", http.StatusForbidden)
		return
	}
	apiKey, ok := v.Get(vaultRefAIKey)
	if !ok || apiKey == "" {
		http.Error(w, "no AI key configured — set one in Trust → Vault → AI provider", http.StatusPreconditionFailed)
		return
	}
	provider, _ := v.Get(vaultRefAIProvider)
	if provider == "" {
		provider = "anthropic"
	}
	if !strings.EqualFold(provider, "anthropic") {
		http.Error(w, "unsupported provider stored in vault: "+provider, http.StatusInternalServerError)
		return
	}
	model, _ := v.Get(vaultRefAIModel)

	ctx, cancel := context.WithTimeout(r.Context(), 70*time.Second)
	defer cancel()
	narrative, err := rundoctor.CallAnthropic(ctx, apiKey, model, prompt)
	if err != nil {
		http.Error(w, "AI call failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{
		"focal_run":     focalSummary(focal),
		"baseline_runs": peerSummaries(baselines),
		"prompt":        prompt,
		"narrative":     narrative,
		"model":         model,
		"provider":      provider,
		"redacted":      redact,
		"generated_at":  time.Now().UTC(),
	})
}

// focalSummary / peerSummaries emit the compact per-run object the
// UI binds to (run id, time, target host display string, headline
// numbers). Keeping the keys short and stable so the UI doesn't
// have to re-derive them.
func focalSummary(m persist.RunMeta) map[string]any {
	return map[string]any{
		"id":              m.ID,
		"started_at":      m.StartedAt,
		"stopped_at":      m.StoppedAt,
		"target_host":     m.TargetHost,
		"target_port":     m.TargetPort,
		"target_protocol": m.TargetProtocol,
		"target_display":  targetDisplay(m),
		"overall_mbps":    m.OverallMBps,
		"failed_files":    m.FailedFiles,
		"succeeded_files": m.SucceededFiles,
	}
}

func peerSummaries(ms []persist.RunMeta) []map[string]any {
	out := make([]map[string]any, len(ms))
	for i, m := range ms {
		out[i] = focalSummary(m)
	}
	return out
}

func targetDisplay(m persist.RunMeta) string {
	if m.TargetHost == "" {
		return "(host unknown — legacy run)"
	}
	proto := m.TargetProtocol
	if proto == "" {
		proto = "sftp"
	}
	if m.TargetPort == 0 {
		return fmt.Sprintf("%s://%s", proto, m.TargetHost)
	}
	return fmt.Sprintf("%s://%s:%d", proto, m.TargetHost, m.TargetPort)
}
