// rundoctor_handlers.go — HTTP surface for the Run Doctor AI
// diagnostic feature. v0.20.6 endpoints:
//
//   GET  /api/run-doctor/config        → AI provider config + selected model
//   POST /api/run-doctor/config        → save AI key + provider + model in vault
//   GET  /api/run-doctor/models        → list available models with cost hints
//   GET  /api/run-doctor/peers         → comparable same-host peers for a run
//   GET  /api/run-doctor/history       → all saved diagnoses for a run (threaded)
//   POST /api/run-doctor/analyze       → run a diagnosis (initial or follow-up);
//                                         saves the result to disk for history.
//
// Follow-up flow (v0.20.6): when the analyze body carries a
// non-empty `parent_diagnosis_id` and `question`, the handler walks
// the parent chain to assemble the conversation history, threads it
// into the prompt via rundoctor.BuildFollowupPrompt, calls the AI,
// and persists the new diagnosis with the parent id linked. The UI
// can then render the conversation as a thread.
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
	apiKey := strings.TrimSpace(body.APIKey)
	model := strings.TrimSpace(body.Model)
	// v0.20.6 — accept partial updates. The first save needs a key;
	// subsequent saves (e.g. operator picks a different model from
	// the dropdown) can omit it. We still require AT LEAST one of
	// api_key / model so a no-op POST is rejected.
	_, hasExisting := v.Get(vaultRefAIKey)
	if apiKey == "" && model == "" {
		http.Error(w, "api_key or model is required", http.StatusBadRequest)
		return
	}
	if apiKey == "" && !hasExisting {
		http.Error(w, "api_key is required on first setup", http.StatusBadRequest)
		return
	}
	if err := v.Set(vaultRefAIProvider, provider); err != nil {
		http.Error(w, "vault set provider: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if apiKey != "" {
		if err := v.Set(vaultRefAIKey, apiKey); err != nil {
			http.Error(w, "vault set key: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if model != "" {
		if err := v.Set(vaultRefAIModel, model); err != nil {
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

// handleRunDoctorAnalyze runs the focal-vs-baselines diagnostic OR
// a follow-up question on a prior diagnosis.
//
// Body: run_id (required), compare_ids (optional — empty = server
// picks 5 newest same-host peers), redact (default true), dry_run
// (default false), model (optional — overrides vault-stored model
// for this call only), parent_diagnosis_id + question (follow-up).
// When parent_diagnosis_id is set, the handler walks the parent
// chain to thread the prior assistant + user turns into the
// prompt. Each non-dry-run call is appended to the per-run
// diagnoses log so the UI can restore the conversation later.
func (s *Server) handleRunDoctorAnalyze(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		RunID             string   `json:"run_id"`
		CompareIDs        []string `json:"compare_ids"`
		Redact            *bool    `json:"redact"`
		DryRun            bool     `json:"dry_run"`
		Model             string   `json:"model"`              // override; empty ⇒ use vault-stored
		ParentDiagnosisID string   `json:"parent_diagnosis_id"`// for follow-ups
		Question          string   `json:"question"`           // for follow-ups
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.RunID == "" {
		http.Error(w, "run_id is required", http.StatusBadRequest)
		return
	}
	if body.ParentDiagnosisID != "" && strings.TrimSpace(body.Question) == "" {
		http.Error(w, "question is required when parent_diagnosis_id is set", http.StatusBadRequest)
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

	// Build the prompt. If this is a follow-up, walk the parent
	// chain and assemble the conversation history; otherwise it's
	// a first-turn analysis.
	var prompt rundoctor.PromptResult
	var parentChain []persist.Diagnosis
	isFollowup := body.ParentDiagnosisID != ""
	if isFollowup {
		// Walk parent chain oldest-first so the model sees turns in
		// the right order. Cap the depth at 12 to keep prompt size
		// sane on a runaway thread; older turns drop off.
		const maxDepth = 12
		var chain []persist.Diagnosis
		cur := body.ParentDiagnosisID
		for i := 0; i < maxDepth && cur != ""; i++ {
			d, ok, err := persist.FindDiagnosis(s.reportsDir, body.RunID, cur)
			if err != nil {
				http.Error(w, "lookup parent diagnosis: "+err.Error(), http.StatusInternalServerError)
				return
			}
			if !ok {
				break
			}
			chain = append([]persist.Diagnosis{d}, chain...) // prepend → oldest-first
			cur = d.ParentID
		}
		parentChain = chain

		// Convert chain into rundoctor.Turn pairs the prompt builder
		// understands. Each saved diagnosis represents one assistant
		// turn (and, for follow-ups, the user question that preceded
		// it). The structured-prompt user turn is synthesised inside
		// BuildFollowupPrompt; we only emit the assistant + later
		// user-question entries here.
		var history []rundoctor.Turn
		for _, d := range chain {
			if d.Question != "" {
				history = append(history, rundoctor.Turn{Role: "user", Content: d.Question})
			}
			history = append(history, rundoctor.Turn{Role: "assistant", Content: d.Narrative})
		}
		prompt = rundoctor.BuildFollowupPrompt(focal, baselines, history, body.Question, redact)
	} else {
		prompt = rundoctor.BuildPrompt(focal, baselines, redact)
	}

	if body.DryRun {
		writeJSON(w, map[string]any{
			"focal_run":     focalSummary(focal),
			"baseline_runs": peerSummaries(baselines),
			"prompt":        prompt,
			"dry_run":       true,
			"parent_chain":  diagnosisSummaries(parentChain),
			"is_followup":   isFollowup,
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
	// Model resolution: per-call body override > vault-stored default >
	// rundoctor default (Haiku 4.5). Validating against KnownModels
	// would be overzealous — Anthropic ships new model ids over time
	// and we should let an operator pick one we don't know about.
	model := strings.TrimSpace(body.Model)
	if model == "" {
		model, _ = v.Get(vaultRefAIModel)
	}

	ctx, cancel := context.WithTimeout(r.Context(), 70*time.Second)
	defer cancel()
	tStart := time.Now()
	narrative, err := rundoctor.CallAnthropic(ctx, apiKey, model, prompt)
	if err != nil {
		http.Error(w, "AI call failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	elapsed := time.Since(tStart)
	promptChars := len(prompt.SystemPrompt) + len(prompt.UserPrompt)
	for _, t := range prompt.PriorTurns {
		promptChars += len(t.Content)
	}
	estUSD := rundoctor.EstimateCostUSD(model, promptChars, len(narrative))

	// Persist the diagnosis so the operator can scroll back through
	// prior diagnoses + follow-up threads when they reopen the panel.
	// Failure to persist is logged but does not fail the call — the
	// operator already paid for the AI response and should see it.
	mode := "auto"
	if len(body.CompareIDs) > 0 {
		mode = "pick"
	}
	baselineIDs := make([]string, 0, len(baselines))
	for _, b := range baselines {
		baselineIDs = append(baselineIDs, b.ID)
	}
	saved := persist.Diagnosis{
		RunID:         body.RunID,
		Provider:      provider,
		Model:         model,
		Redacted:      redact,
		BaselineIDs:   baselineIDs,
		Mode:          mode,
		Question:      body.Question,
		ParentID:      body.ParentDiagnosisID,
		Narrative:     narrative,
		PromptChars:   promptChars,
		ResponseChars: len(narrative),
		ElapsedMs:     elapsed.Milliseconds(),
		EstUSD:        estUSD,
	}
	if err := persist.AppendDiagnosis(s.reportsDir, saved); err != nil {
		// Soft-fail: tag the response so the UI can warn.
		writeJSON(w, map[string]any{
			"focal_run":     focalSummary(focal),
			"baseline_runs": peerSummaries(baselines),
			"prompt":        prompt,
			"narrative":     narrative,
			"model":         model,
			"provider":      provider,
			"redacted":      redact,
			"generated_at":  saved.GeneratedAt,
			"prompt_chars":  promptChars,
			"response_chars": len(narrative),
			"elapsed_ms":    elapsed.Milliseconds(),
			"est_usd":       estUSD,
			"persist_warning": err.Error(),
		})
		return
	}
	// Re-fetch to get the assigned ID + canonical timestamp.
	allDiags, _ := persist.ListDiagnoses(s.reportsDir, body.RunID)
	var savedID string
	var savedAt time.Time
	if len(allDiags) > 0 {
		last := allDiags[len(allDiags)-1]
		savedID = last.ID
		savedAt = last.GeneratedAt
	}
	writeJSON(w, map[string]any{
		"diagnosis_id":   savedID,
		"focal_run":      focalSummary(focal),
		"baseline_runs":  peerSummaries(baselines),
		"prompt":         prompt,
		"narrative":      narrative,
		"model":          model,
		"provider":       provider,
		"redacted":       redact,
		"generated_at":   savedAt,
		"prompt_chars":   promptChars,
		"response_chars": len(narrative),
		"elapsed_ms":     elapsed.Milliseconds(),
		"est_usd":        estUSD,
		"is_followup":    isFollowup,
		"parent_id":      body.ParentDiagnosisID,
	})
}

// handleRunDoctorHistory returns every saved diagnosis for a run in
// chronological order (oldest first). Each entry includes ParentID
// so the UI can render the conversation as a tree of follow-ups.
func (s *Server) handleRunDoctorHistory(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("run_id")
	if id == "" {
		http.Error(w, "run_id is required", http.StatusBadRequest)
		return
	}
	if s.reportsDir == "" {
		http.Error(w, "no reports directory configured", http.StatusServiceUnavailable)
		return
	}
	diags, err := persist.ListDiagnoses(s.reportsDir, id)
	if err != nil {
		http.Error(w, "list diagnoses: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"run_id":      id,
		"diagnoses":   diags,
	})
}

// handleRunDoctorModels returns the list of supported AI models
// with rough cost hints. Pure metadata — no vault required.
func (s *Server) handleRunDoctorModels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"models": rundoctor.KnownModels})
}

// diagnosisSummaries — compact array form for the UI's parent-chain
// breadcrumb under a follow-up dry-run preview.
func diagnosisSummaries(ds []persist.Diagnosis) []map[string]any {
	out := make([]map[string]any, len(ds))
	for i, d := range ds {
		out[i] = map[string]any{
			"id":           d.ID,
			"generated_at": d.GeneratedAt,
			"model":        d.Model,
			"question":     d.Question,
			"parent_id":    d.ParentID,
		}
	}
	return out
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
