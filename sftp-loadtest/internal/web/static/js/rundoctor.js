// rundoctor.js — Run Doctor UI module. Lazy-loaded by run-detail.js
// when the operator clicks "Run Doctor" on a finished run.
//
// Mounted into the run-detail page below the analysis section. The
// panel is structured around the customer's mental model:
//
//   1. "Which run am I diagnosing?"  — the focal run's destination
//      badge sits at the top, identical to the one in the run-detail
//      header. Apples-to-apples comparison only happens against runs
//      sharing this exact host:port:protocol.
//
//   2. "What am I comparing it against?"  — a "Compare against"
//      picker with three modes:
//        a. Auto — server picks the 5 most-recent same-host runs.
//        b. All same-host runs — every comparable peer.
//        c. Pick by date — operator selects specific runs from a
//           dropdown grouped by day. Multi-select.
//
//   3. "Is this private?"  — redaction toggle (on by default) +
//      a "Preview prompt" disclosure so the operator can see exactly
//      what would be sent to the AI provider before paying tokens.
//
//   4. "What did the AI say?"  — narrative output with copy + retry.
//
// Public surface:
//   mountRunDoctor(panelEl, meta) — fills the panel; idempotent on
//                                   subsequent clicks (re-renders
//                                   only when the focal run id changed).

import { apiFetch, apiJSON } from './api.js';
import { confirm as confirmModal, prompt as promptModal } from './modal.js';
import { pushToast } from './toast.js';

const MOUNT_FLAG = 'run-doctor-mounted-id';

export async function mountRunDoctor(panel, meta) {
  // Idempotent — clicking the button twice on the same run reuses
  // the already-rendered panel. New run id ⇒ rebuild from scratch.
  if (panel.dataset[MOUNT_FLAG] === meta.id) {
    panel.scrollIntoView({ behavior: 'smooth', block: 'start' });
    return;
  }
  panel.dataset[MOUNT_FLAG] = meta.id;
  panel.innerHTML = renderShell(meta);

  const peersList = panel.querySelector('[data-role="peers-list"]');
  const peersCount = panel.querySelector('[data-role="peers-count"]');
  const compareSelect = panel.querySelector('[data-role="compare-mode"]');
  const peerPicker = panel.querySelector('[data-role="peer-picker"]');
  const redactToggle = panel.querySelector('[data-role="redact-toggle"]');
  const previewBtn = panel.querySelector('[data-role="preview-prompt"]');
  const analyzeBtn = panel.querySelector('[data-role="analyze"]');
  const setupBlock = panel.querySelector('[data-role="setup-block"]');
  const setupBtn = panel.querySelector('[data-role="open-vault"]');
  const resultBlock = panel.querySelector('[data-role="result-block"]');
  const previewBlock = panel.querySelector('[data-role="preview-block"]');

  // 1. Pull peers + AI config + model list + saved-diagnosis history
  //    in parallel. All four are independent and small.
  const [peersData, configData, modelsData, historyData] = await Promise.all([
    fetchPeers(meta.id),
    fetchAIConfig(),
    fetchModels(),
    fetchHistory(meta.id),
  ]);

  // Populate the model picker. Configured model from vault is the
  // default selection; falls back to the first known model.
  const modelSelect = panel.querySelector('[data-role="model-select"]');
  const models = modelsData.models || [];
  if (models.length === 0) {
    // Backend didn't return any — keep the picker hidden.
    modelSelect.innerHTML = '<option value="">(default)</option>';
  } else {
    modelSelect.innerHTML = models.map((m) => `
      <option value="${escapeAttr(m.id)}" title="${escapeAttr(m.description || '')}">
        ${escapeHTML(m.label)}
      </option>`).join('');
    // Select the operator's saved choice if any.
    if (configData.model) {
      const opt = [...modelSelect.options].find((o) => o.value === configData.model);
      if (opt) modelSelect.value = configData.model;
    }
  }
  // Save-on-change so the next session opens to the same picked model.
  modelSelect.addEventListener('change', async () => {
    if (!configData.configured) return; // can't save model without a key in place
    try {
      await apiFetch('/api/run-doctor/config', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ provider: 'anthropic', model: modelSelect.value }),
      });
      configData.model = modelSelect.value;
    } catch { /* model picker still works in-session even if save fails */ }
  });

  // Render saved-diagnosis history at the top so the operator
  // immediately sees prior conversations on this run. The thread
  // builder turns the flat list into a parent → children tree.
  renderHistory(panel, meta, historyData.diagnoses || [], modelSelect, redactToggleRef());
  function redactToggleRef() {
    return panel.querySelector('[data-role="redact-toggle"]');
  }

  // 2. AI config gate. If no key is configured, show the setup
  //    block and disable the analyze button. The setup block links
  //    to Trust → Vault, where the operator can store their key.
  const configured = !!configData.configured;
  const vaultUnlocked = !!configData.vault_unlocked;
  if (!configured) {
    setupBlock.hidden = false;
    analyzeBtn.disabled = true;
    setupBtn.addEventListener('click', async () => {
      // The vault may be locked (no key visible) OR unlocked but
      // empty (vault present, AI key not yet stored). Either way,
      // open Trust → Vault and offer to set the key inline.
      const ok = await promptForAIKey(vaultUnlocked);
      if (ok) {
        // Re-fetch and refresh.
        const fresh = await fetchAIConfig();
        if (fresh.configured) {
          setupBlock.hidden = true;
          analyzeBtn.disabled = false;
          pushToast('AI provider key saved — ready to diagnose.', 'success');
        }
      }
    });
  }

  // 3. Peers picker. Populate the dropdown with comparable runs and
  //    add a date-grouped checkbox list when "pick by date" is on.
  const peers = peersData.peers || [];
  peersCount.textContent = peers.length === 0
    ? 'no comparable history yet'
    : `${peers.length} same-host run${peers.length === 1 ? '' : 's'} available`;
  if (peers.length === 0) {
    // No peers: hide the compare picker, the analysis still runs
    // (server emits an explicit "no comparable historical runs"
    // line in the prompt so the LLM doesn't fabricate a baseline).
    compareSelect.disabled = true;
  } else {
    renderPeerCheckboxes(peerPicker, peers);
  }
  compareSelect.addEventListener('change', () => {
    peerPicker.hidden = compareSelect.value !== 'pick';
    renderSelectedPeers(peersList, compareSelect, peerPicker, peers);
  });
  peerPicker.addEventListener('change', () => {
    renderSelectedPeers(peersList, compareSelect, peerPicker, peers);
  });
  renderSelectedPeers(peersList, compareSelect, peerPicker, peers);

  // 4. Preview-prompt button. Calls the analyze endpoint with
  //    dry_run=true so no tokens are spent and the operator sees
  //    exactly what would be sent. Costs nothing.
  previewBtn.addEventListener('click', async () => {
    previewBtn.disabled = true;
    previewBlock.hidden = false;
    previewBlock.querySelector('[data-role="preview-content"]').textContent = 'Building prompt…';
    try {
      const body = currentRequestBody(meta, compareSelect, peerPicker, redactToggle, modelSelect);
      body.dry_run = true;
      const r = await apiFetch('/api/run-doctor/analyze', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
      if (!r.ok) throw new Error(await r.text());
      const j = await r.json();
      const sys = j.prompt?.system_prompt || '';
      const usr = j.prompt?.user_prompt || '';
      previewBlock.querySelector('[data-role="preview-content"]').innerHTML = `
        <details open>
          <summary><strong>System prompt</strong> — framing for the AI</summary>
          <pre class="run-doctor-pre">${escapeHTML(sys)}</pre>
        </details>
        <details open>
          <summary><strong>User prompt</strong> — the run data being sent</summary>
          <pre class="run-doctor-pre">${escapeHTML(usr)}</pre>
        </details>`;
    } catch (e) {
      previewBlock.querySelector('[data-role="preview-content"]').textContent = 'Preview failed: ' + (e.message || e);
    } finally {
      previewBtn.disabled = false;
    }
  });

  // v0.20.6 — keep the cost-hint in sync with the model dropdown.
  // Estimates assume a typical prompt ~4 KB / response ~1 KB so the
  // operator sees an order-of-magnitude figure without us having to
  // build a real prompt up front.
  async function refreshCostHint() {
    const hint = panel.querySelector('[data-role="cost-hint"]');
    if (!hint) return;
    const m = await modelCost(modelSelect.value);
    if (!m) { hint.textContent = ''; return; }
    const est = estimateUSD(m, 4000, 1000);
    hint.textContent = `~$${est.toFixed(4)} per analysis · ${m.description || ''}`;
  }
  modelSelect.addEventListener('change', refreshCostHint);
  refreshCostHint();

  // 5. Analyze. Drives a real, observable, step-by-step progression
  //    so the operator can see exactly what is happening at each
  //    stage — no opaque spinner. Each step writes its concrete
  //    result into the DOM (baseline count, prompt size, redaction
  //    count, model name, elapsed time) before moving on.
  //
  //    v0.20.6 — also drives follow-up Q&A: when extraBody carries
  //    parent_diagnosis_id + question, the same staged pipeline
  //    runs against the follow-up endpoint, with stage labels
  //    adjusted to "Threading prior turns…", and the rendered
  //    result is inserted as a follow-up reply card under the
  //    history thread instead of replacing the main result block.
  async function runAnalysis(extraBody = {}) {
    if (!configData.configured) return;
    const isFollowup = !!extraBody.parent_diagnosis_id;
    analyzeBtn.disabled = true;
    resultBlock.hidden = false;
    const out = resultBlock.querySelector('[data-role="result-content"]');
    const stages = isFollowup ? [
      { id: 'select',  label: 'Threading prior turns into context' },
      { id: 'build',   label: 'Preparing follow-up prompt' },
      { id: 'send',    label: 'Sending to your AI provider' },
      { id: 'render',  label: 'Rendering reply' },
    ] : [
      { id: 'select',  label: 'Selecting baselines to compare against' },
      { id: 'build',   label: 'Preparing comparison summary (with redaction)' },
      { id: 'send',    label: 'Sending to your AI provider' },
      { id: 'render',  label: 'Rendering diagnosis' },
    ];
    out.innerHTML = `
      <div class="run-doctor-stages-wrap">
        <div class="run-doctor-stages-head">Run Doctor steps — live</div>
        <ol class="run-doctor-stages">
          ${stages.map((s) => `
            <li class="run-doctor-stage" data-stage="${s.id}" data-state="pending">
              <span class="run-doctor-stage-icon" aria-hidden="true"></span>
              <span class="run-doctor-stage-body">
                <span class="run-doctor-stage-label">${escapeHTML(s.label)}</span>
                <span class="run-doctor-stage-detail"></span>
              </span>
            </li>`).join('')}
        </ol>
        <div class="run-doctor-stages-foot" data-role="stages-foot"></div>
      </div>`;

    const setStage = (id, state, detail) => {
      const el = out.querySelector(`[data-stage="${id}"]`);
      if (!el) return;
      el.dataset.state = state;
      if (detail !== undefined) {
        el.querySelector('.run-doctor-stage-detail').textContent = detail;
      }
    };
    const fail = (id, msg) => setStage(id, 'error', msg);

    const t0 = performance.now();
    try {
      // Stage 1 — select baselines (purely client-side; the operator
      // already chose; we just enumerate what the request will carry).
      setStage('select', 'active');
      const body = { ...currentRequestBody(meta, compareSelect, peerPicker, redactToggle, modelSelect), ...extraBody };
      const baseCount = body.compare_ids.length;
      const mode = compareSelect.value;
      const modeLabel = mode === 'auto' ? 'Auto (server picks 5 newest same-host)'
        : mode === 'all' ? 'All same-host runs'
        : 'Pick by date';
      const detail1 = isFollowup
        ? `Threading parent diagnosis ${escapeHTML(extraBody.parent_diagnosis_id || '').slice(0, 18)}…`
        : (baseCount === 0
          ? `${modeLabel} — server will fall back to "no comparable historical runs" if none exist`
          : `${modeLabel} — ${baseCount} run${baseCount === 1 ? '' : 's'} selected`);
      setStage('select', 'done', detail1);

      // Stage 2 — build prompt via the same endpoint with dry_run=true
      // so we can show prompt size + redaction count + estimated cost
      // BEFORE paying tokens.
      setStage('build', 'active');
      const dryR = await apiFetch('/api/run-doctor/analyze', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ ...body, dry_run: true }),
      });
      if (!dryR.ok) throw new Error(`build failed: ${await dryR.text()}`);
      const dryJ = await dryR.json();
      const sysLen = (dryJ.prompt?.system_prompt || '').length;
      const usrLen = (dryJ.prompt?.user_prompt || '').length;
      const priorLen = (dryJ.prompt?.prior_turns || []).reduce((s, t) => s + (t.content || '').length, 0);
      const totalChars = sysLen + usrLen + priorLen;
      const redCount = Object.keys(dryJ.prompt?.redactions || {}).length;
      const redLabel = body.redact
        ? `${redCount} value${redCount === 1 ? '' : 's'} redacted`
        : 'redaction OFF';
      // Show cost estimate inline so the operator can bail before send.
      const m = await modelCost(modelSelect.value);
      const cost = m ? estimateUSD(m, totalChars, 1000) : 0;
      const costLabel = cost > 0 ? ` · est. $${cost.toFixed(4)}` : '';
      setStage('build', 'done', `${totalChars.toLocaleString()} chars · ${dryJ.baseline_runs?.length || 0} baseline${(dryJ.baseline_runs?.length || 0) === 1 ? '' : 's'} · ${redLabel}${costLabel}`);

      // Stage 3 — real AI call.
      setStage('send', 'active', 'Anthropic Messages API · awaiting response…');
      const tSend = performance.now();
      const r = await apiFetch('/api/run-doctor/analyze', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
      if (!r.ok) throw new Error(await r.text());
      const j = await r.json();
      const elapsedSec = ((performance.now() - tSend) / 1000).toFixed(1);
      const respChars = (j.narrative || '').length;
      const finalCost = (j.est_usd || 0).toFixed(4);
      setStage('send', 'done', `${respChars.toLocaleString()} chars in ${elapsedSec} s · model ${j.model || 'default'}${j.est_usd ? ' · $' + finalCost : ''}`);

      // Stage 4 — render.
      setStage('render', 'active');
      const totalSec = ((performance.now() - t0) / 1000).toFixed(1);
      const foot = out.querySelector('[data-role="stages-foot"]');
      if (foot) foot.textContent = `Total time: ${totalSec} s · diagnosis saved as ${j.diagnosis_id || '(unsaved)'}`;
      setStage('render', 'done');

      // Append the comparison strip + diagnosis below the stages.
      const result = document.createElement('div');
      result.className = 'run-doctor-result-after-stages';
      const cmpHTML = isFollowup
        ? '' // follow-ups don't repeat the focal-vs-baseline visual
        : renderComparisonStrip(j.focal_run || meta, j.baseline_runs || []);
      result.innerHTML = cmpHTML + renderResult(j) + renderResultActionsRow(j, isFollowup);
      out.appendChild(result);
      wireResultActions(result, j);

      // Wire the Re-analyze button (replays the same parameters on
      // the same focal run; produces a fresh saved diagnosis).
      const retry = result.querySelector('[data-role="re-analyze"]');
      if (retry) retry.addEventListener('click', () => runAnalysis(extraBody.parent_diagnosis_id ? extraBody : {}));

      // Reveal the follow-up textbox under the diagnosis. The send
      // button binds to the latest diagnosis id so the next question
      // threads under THIS turn unless the operator picked a
      // different parent from the history thread.
      const fuBlock = panel.querySelector('[data-role="followup-block"]');
      if (fuBlock) {
        fuBlock.hidden = false;
        if (j.diagnosis_id) stagedFollowupParent = j.diagnosis_id;
        const tag = panel.querySelector('[data-role="followup-parent-tag"]');
        if (tag) tag.textContent = `↳ replying to ${stagedFollowupParent || 'latest'}`;
      }

      // Refresh the history thread so the new diagnosis joins it.
      const fresh = await fetchHistory(meta.id);
      renderHistory(panel, meta, fresh.diagnoses || [], modelSelect, redactToggle);
    } catch (e) {
      const active = out.querySelector('[data-stage][data-state="active"]');
      if (active) fail(active.dataset.stage, e.message || String(e));
      else {
        const errTile = document.createElement('div');
        errTile.className = 'run-doctor-error';
        errTile.textContent = 'Analysis failed: ' + (e.message || String(e));
        out.appendChild(errTile);
      }
    } finally {
      analyzeBtn.disabled = false;
    }
  }

  // Initial Analyze click → first-turn analysis (no follow-up extras).
  analyzeBtn.addEventListener('click', () => runAnalysis({}));

  // Follow-up Send button → analysis with parent_diagnosis_id +
  // question. Keeps the same model / compare-set / redaction
  // settings the operator picked above so threading is consistent.
  const fuInput = panel.querySelector('[data-role="followup-input"]');
  const fuSend  = panel.querySelector('[data-role="followup-send"]');
  const fuClear = panel.querySelector('[data-role="followup-clear"]');
  if (fuSend) {
    fuSend.addEventListener('click', () => {
      const q = (fuInput.value || '').trim();
      if (!q) { pushToast('Type a question first.', 'warn'); return; }
      const parent = stagedFollowupParent;
      if (!parent) { pushToast('No parent diagnosis to follow up on yet.', 'warn'); return; }
      fuInput.value = '';
      runAnalysis({ parent_diagnosis_id: parent, question: q });
    });
  }
  if (fuClear) {
    fuClear.addEventListener('click', () => {
      stagedFollowupParent = '';
      const tag = panel.querySelector('[data-role="followup-parent-tag"]');
      if (tag) tag.textContent = '';
    });
  }

  panel.scrollIntoView({ behavior: 'smooth', block: 'start' });
}

// renderResultActionsRow — buttons that sit below a freshly-rendered
// diagnosis: Copy (already in renderResult), Re-analyze (re-runs
// with the same parameters), and a "View saved" link to the history.
function renderResultActionsRow(j, isFollowup) {
  return `
    <div class="run-doctor-result-actions">
      <button type="button" class="btn btn-ghost btn-sm" data-role="re-analyze"
              title="Run the analysis again with the current settings — produces a fresh saved diagnosis">
        ↻ Re-analyze
      </button>
      ${j.diagnosis_id
        ? `<span class="run-doctor-saved-tag">saved as <span class="mono">${escapeHTML(j.diagnosis_id)}</span></span>`
        : '<span class="run-doctor-saved-tag run-doctor-saved-tag-warn">not saved (persist warning)</span>'}
    </div>`;
}

// ---------------------------------------------------------------- helpers

async function fetchPeers(id) {
  try { return await apiJSON(`/api/run-doctor/peers?id=${encodeURIComponent(id)}`); }
  catch { return { peers: [] }; }
}
async function fetchAIConfig() {
  try { return await apiJSON('/api/run-doctor/config'); }
  catch { return { configured: false, vault_unlocked: false }; }
}
async function fetchModels() {
  try { return await apiJSON('/api/run-doctor/models'); }
  catch { return { models: [] }; }
}
async function fetchHistory(runID) {
  try { return await apiJSON(`/api/run-doctor/history?run_id=${encodeURIComponent(runID)}`); }
  catch { return { diagnoses: [] }; }
}

// Model cost lookup: fetched once per panel, cached on the module
// scope so renderResult / step-2 detail can show "~$0.0001 est."
// without re-fetching. Populated from /api/run-doctor/models.
let _modelCost = null;
async function modelCost(modelID) {
  if (!_modelCost) {
    const j = await fetchModels();
    _modelCost = new Map((j.models || []).map((m) => [m.id, m]));
  }
  return _modelCost.get(modelID) || null;
}
function estimateUSD(model, promptChars, responseChars) {
  if (!model || promptChars <= 0) return 0;
  const inT = promptChars / 4;
  const outT = responseChars / 4;
  return (inT * (model.usd_per_million_input || 0) + outT * (model.usd_per_million_output || 0)) / 1_000_000;
}

function currentRequestBody(meta, compareSelect, peerPicker, redactToggle, modelSelect) {
  const mode = compareSelect.value;
  const body = { run_id: meta.id, redact: !!redactToggle.checked };
  if (mode === 'pick') {
    body.compare_ids = [...peerPicker.querySelectorAll('input[type="checkbox"]:checked')].map((b) => b.value);
  } else if (mode === 'all') {
    // server's empty default = newest 5; for "all" we send every peer id.
    const all = [...peerPicker.querySelectorAll('input[type="checkbox"]')].map((b) => b.value);
    body.compare_ids = all;
  } else {
    // mode === 'auto' — empty array, server picks 5 most recent.
    body.compare_ids = [];
  }
  if (modelSelect && modelSelect.value) body.model = modelSelect.value;
  return body;
}

// renderHistory paints a thread of saved diagnoses at the top of
// the panel. Each top-level diagnosis (no parent) becomes a card;
// follow-ups are nested beneath their parent. Clicking a header
// expands the narrative inline. The newest top-level conversation
// is expanded by default; older ones are collapsed.
function renderHistory(panel, meta, diagnoses, modelSelect, redactToggle) {
  const host = panel.querySelector('[data-role="history-block"]');
  if (!host) return;
  if (!diagnoses || diagnoses.length === 0) {
    host.hidden = true;
    return;
  }
  host.hidden = false;
  // Group by thread (parent chain root). For each diagnosis with no
  // parent, that's a thread root; descendants are anything that
  // chains to it via parent_id.
  const byID = new Map(diagnoses.map((d) => [d.id, d]));
  const childrenOf = new Map();
  for (const d of diagnoses) {
    if (!d.parent_id) continue;
    if (!childrenOf.has(d.parent_id)) childrenOf.set(d.parent_id, []);
    childrenOf.get(d.parent_id).push(d);
  }
  const roots = diagnoses.filter((d) => !d.parent_id);
  // Newest root first.
  roots.sort((a, b) => (b.generated_at || '').localeCompare(a.generated_at || ''));

  const renderTurn = (d, depth) => {
    const kids = (childrenOf.get(d.id) || []).sort((a, b) => (a.generated_at || '').localeCompare(b.generated_at || ''));
    const ts = (d.generated_at || '').replace('T', ' ').replace(/\..*$/, '').replace('Z', ' UTC');
    const promptedBy = d.question
      ? `<div class="run-doctor-thread-question"><span class="lbl">Q:</span> ${escapeHTML(d.question)}</div>`
      : '';
    return `
      <details class="run-doctor-thread-turn" data-depth="${depth}" ${depth === 0 && roots[0].id === d.id ? 'open' : ''}>
        <summary>
          <span class="run-doctor-thread-time mono">${escapeHTML(ts)}</span>
          <span class="run-doctor-thread-model mono">${escapeHTML(d.model || '—')}</span>
          ${d.question
            ? `<span class="run-doctor-thread-tag run-doctor-thread-tag-followup">follow-up</span>`
            : `<span class="run-doctor-thread-tag run-doctor-thread-tag-initial">initial</span>`}
          <span class="run-doctor-thread-snippet">${escapeHTML((d.narrative || '').split('\n').find((l) => l.trim()) || '').slice(0, 90)}…</span>
        </summary>
        ${promptedBy}
        <div class="run-doctor-thread-narrative">${narrativeToHTML(d.narrative || '')}</div>
        <div class="run-doctor-thread-foot">
          <button type="button" class="btn btn-sm btn-ghost" data-role="thread-followup" data-parent="${escapeAttr(d.id)}">Ask follow-up</button>
        </div>
        ${kids.map((k) => renderTurn(k, depth + 1)).join('')}
      </details>`;
  };

  host.innerHTML = `
    <div class="run-doctor-history-head">
      <span>Past Run Doctor diagnoses for this run · ${diagnoses.length}</span>
      <span class="run-doctor-history-spacer"></span>
      <button type="button" class="btn btn-sm btn-ghost" data-role="history-collapse-all">Collapse all</button>
    </div>
    ${roots.map((r) => renderTurn(r, 0)).join('')}`;

  // Collapse-all helper.
  host.querySelector('[data-role="history-collapse-all"]').addEventListener('click', () => {
    host.querySelectorAll('details').forEach((d) => { d.open = false; });
  });
  // Per-turn "Ask follow-up" button — hands the parent diagnosis id
  // to the follow-up textbox below.
  host.querySelectorAll('[data-role="thread-followup"]').forEach((btn) => {
    btn.addEventListener('click', () => {
      stagedFollowupParent = btn.dataset.parent;
      const ta = panel.querySelector('[data-role="followup-input"]');
      const wrap = panel.querySelector('[data-role="followup-block"]');
      if (wrap) wrap.hidden = false;
      if (ta) { ta.focus(); }
      const tag = panel.querySelector('[data-role="followup-parent-tag"]');
      if (tag) tag.textContent = `↳ replying to ${btn.dataset.parent}`;
    });
  });
}

// Module-scope: when the operator clicks "Ask follow-up" on a
// specific past diagnosis, the parent id is stashed here and the
// next analyze call inherits it. Cleared after each successful send.
let stagedFollowupParent = '';

// narrativeToHTML — light markdown-ish formatter used for both
// freshly-rendered diagnoses and history-thread bodies. Identical
// to renderResult's inline conversion but factored out so history
// rendering reuses it without the meta-line wrapper.
function narrativeToHTML(text) {
  return `<p>${String(text)
    .replace(/^## (.+)$/gm, '</p><h4 class="run-doctor-section">$1</h4><p>')
    .replace(/\n\n/g, '</p><p>')}</p>`;
}

// renderComparisonStrip — three stacked horizontal bars showing the
// focal run vs the median of the baselines on the three KPIs an
// operator scans first: throughput, success%, p95 upload latency.
// Pure CSS bars, no chart library. Returns an HTML string.
function renderComparisonStrip(focal, baselines) {
  if (!baselines || baselines.length === 0) {
    return `<div class="run-doctor-cmp run-doctor-cmp-empty">No baseline runs to compare against.</div>`;
  }
  const median = (vals) => {
    const v = vals.filter((x) => Number.isFinite(x)).sort((a, b) => a - b);
    if (v.length === 0) return 0;
    const mid = Math.floor(v.length / 2);
    return v.length % 2 ? v[mid] : (v[mid - 1] + v[mid]) / 2;
  };
  const fMbps = Number(focal.overall_mbps || 0);
  const bMbps = median(baselines.map((b) => Number(b.overall_mbps || 0)));
  const totalF = Number(focal.succeeded_files || 0) + Number(focal.failed_files || 0);
  const fSuc = totalF > 0 ? (Number(focal.succeeded_files || 0) / totalF) * 100 : 0;
  const bSuc = median(baselines.map((b) => {
    const t = Number(b.succeeded_files || 0) + Number(b.failed_files || 0);
    return t > 0 ? (Number(b.succeeded_files || 0) / t) * 100 : 0;
  }));
  // p95 latency comes from the focal_run on the wire only when the
  // analyze response carried it; otherwise we render N/A. The
  // server's focal_run summary doesn't include latency today, so
  // these come back as 0 — that's OK, we'll surface "—".
  const renderBar = (label, focalV, baseV, units, betterIs) => {
    const max = Math.max(focalV, baseV, 0.001);
    const fPct = (focalV / max) * 100;
    const bPct = (baseV / max) * 100;
    const delta = focalV - baseV;
    const pos = (betterIs === 'higher' && delta >= 0) || (betterIs === 'lower' && delta <= 0);
    const tone = Math.abs(delta) < 0.01 ? 'flat' : (pos ? 'good' : 'bad');
    const sign = delta > 0 ? '+' : '';
    return `
      <div class="run-doctor-cmp-row" data-tone="${tone}">
        <span class="run-doctor-cmp-label">${escapeHTML(label)}</span>
        <span class="run-doctor-cmp-bar">
          <span class="run-doctor-cmp-fill run-doctor-cmp-fill-focal" style="width:${fPct.toFixed(1)}%"></span>
          <span class="run-doctor-cmp-fill run-doctor-cmp-fill-base"  style="width:${bPct.toFixed(1)}%"></span>
        </span>
        <span class="run-doctor-cmp-vals mono">
          <span title="focal run">${focalV.toFixed(1)}${escapeHTML(units)}</span>
          <span class="run-doctor-cmp-sep">vs</span>
          <span title="baseline median">${baseV.toFixed(1)}${escapeHTML(units)}</span>
          <span class="run-doctor-cmp-delta">${sign}${delta.toFixed(1)}</span>
        </span>
      </div>`;
  };
  return `
    <div class="run-doctor-cmp">
      <div class="run-doctor-cmp-head">Focal vs baseline median (${baselines.length} run${baselines.length === 1 ? '' : 's'})</div>
      ${renderBar('Throughput',    fMbps, bMbps, ' MB/s', 'higher')}
      ${renderBar('Success rate',  fSuc,  bSuc,  '%',     'higher')}
    </div>`;
}

function renderPeerCheckboxes(host, peers) {
  // Group by yyyy-mm-dd (UTC) for readable scanning.
  const byDay = new Map();
  for (const p of peers) {
    const day = (p.started_at || '').slice(0, 10) || 'unknown';
    if (!byDay.has(day)) byDay.set(day, []);
    byDay.get(day).push(p);
  }
  const days = [...byDay.keys()].sort().reverse();
  host.innerHTML = days.map((day) => `
    <div class="run-doctor-peer-day">
      <div class="run-doctor-peer-day-head">${escapeHTML(day)}</div>
      ${byDay.get(day).map((p) => `
        <label class="run-doctor-peer-row">
          <input type="checkbox" value="${escapeAttr(p.id)}">
          <span class="run-doctor-peer-time mono">${escapeHTML((p.started_at || '').slice(11, 19))}</span>
          <span class="run-doctor-peer-id mono">${escapeHTML(p.id)}</span>
          <span class="run-doctor-peer-mbps mono">${Number(p.overall_mbps || 0).toFixed(1)} MB/s</span>
          <span class="run-doctor-peer-fail mono">${Number(p.failed_files || 0)} failed</span>
        </label>`).join('')}
    </div>`).join('');
}

function renderSelectedPeers(target, compareSelect, peerPicker, peers) {
  const mode = compareSelect.value;
  if (mode === 'auto') {
    target.textContent = `auto · server picks the ${Math.min(5, peers.length)} most recent same-host run${peers.length === 1 ? '' : 's'}`;
    return;
  }
  if (mode === 'all') {
    target.textContent = `all · ${peers.length} same-host run${peers.length === 1 ? '' : 's'}`;
    return;
  }
  const checked = peerPicker.querySelectorAll('input[type="checkbox"]:checked').length;
  target.textContent = checked === 0
    ? 'pick at least one run from the list below'
    : `${checked} run${checked === 1 ? '' : 's'} selected`;
}

function renderResult(j) {
  // Convert the model's markdown-ish ## headings into <h4> sections,
  // preserving paragraph breaks. Light formatting only — full
  // markdown would invite injection vectors via redacted token names.
  const text = String(j.narrative || '');
  const html = text
    .replace(/^## (.+)$/gm, '<h4 class="run-doctor-section">$1</h4>')
    .replace(/\n\n/g, '</p><p>')
    .replace(/^([^<].*)$/gm, '$1');
  const meta = `
    <div class="run-doctor-meta-line">
      <span>${j.redacted ? '🔒 sent redacted' : '⚠ sent un-redacted'}</span>
      <span>·</span>
      <span>model: <span class="mono">${escapeHTML(j.model || 'default')}</span></span>
      <span>·</span>
      <span>${(j.baseline_runs || []).length} baseline${(j.baseline_runs || []).length === 1 ? '' : 's'}</span>
      <span class="run-doctor-meta-spacer"></span>
      <button type="button" class="btn btn-sm btn-ghost" data-role="copy">Copy</button>
    </div>`;
  return `${meta}<div class="run-doctor-narrative"><p>${html}</p></div>`;
}

function wireResultActions(host, j) {
  const copy = host.querySelector('[data-role="copy"]');
  if (copy) copy.addEventListener('click', async () => {
    try {
      await navigator.clipboard.writeText(j.narrative || '');
      pushToast('Copied diagnosis to clipboard.', 'success');
    } catch {
      pushToast('Copy failed.', 'error');
    }
  });
}

async function promptForAIKey(vaultUnlocked) {
  const intro = vaultUnlocked
    ? 'Paste your Anthropic API key — it will be saved encrypted in your vault under "ai/api_key" and never leaves the server in plaintext.'
    : 'Your vault is locked. Open Trust → Vault to unlock, then come back and paste your Anthropic API key.';
  if (!vaultUnlocked) {
    await confirmModal({ title: 'Unlock vault first', message: intro, okLabel: 'Open Trust → Vault', cancelLabel: 'Cancel' })
      .then((go) => { if (go && window.__sftpl_openVault) window.__sftpl_openVault({ unlock: true }); });
    return false;
  }
  const key = await promptModal({
    title: 'Anthropic API key',
    label: intro,
    placeholder: 'sk-ant-…',
  });
  if (!key) return false;
  const r = await apiFetch('/api/run-doctor/config', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ provider: 'anthropic', api_key: key }),
  });
  if (!r.ok) {
    pushToast('Save failed: ' + await r.text(), 'error');
    return false;
  }
  return true;
}

// renderShell builds the static skeleton — every dynamic surface
// inside has a data-role the wiring code in mountRunDoctor binds to.
function renderShell(meta) {
  const targetHost = meta.target_host || '';
  const targetPort = Number(meta.target_port || 0);
  const targetProto = (meta.target_protocol || '').toLowerCase() || 'sftp';
  const targetLine = targetHost
    ? `${targetProto}://${targetHost}${targetPort ? ':' + targetPort : ''}`
    : '(host unknown — legacy run, comparison disabled)';
  return `
    <header class="run-doctor-head">
      <div class="run-doctor-head-icon" aria-hidden="true">
        <svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor"
             stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round">
          <path d="M5 2h6"/><path d="M8 2v3"/>
          <path d="M5 5a3 3 0 003 3 3 3 0 003-3"/>
          <path d="M8 8v3"/>
          <circle cx="8" cy="13" r="1.5"/>
        </svg>
      </div>
      <div class="run-doctor-head-text">
        <h3>Run Doctor</h3>
        <p class="run-doctor-head-sub">
          AI diagnosis of this run, compared apples-to-apples against
          historical runs that hit the same destination.
          <span class="run-doctor-target mono">${escapeHTML(targetLine)}</span>
        </p>
      </div>
    </header>

    <!-- "How Run Doctor works" — plain-English transparency disclosure.
         Open by default so a first-time operator immediately understands
         what is sent, what isn't, and what each step does. Once they
         trust the feature they can collapse it; the <details> state is
         not persisted on purpose — the disclosure is cheap and the
         clarity matters. -->
    <details class="run-doctor-explainer" open>
      <summary>
        <svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor"
             stroke-width="1.6" stroke-linecap="round" aria-hidden="true">
          <circle cx="8" cy="8" r="6"/>
          <path d="M8 11v-4"/><circle cx="8" cy="5" r="0.6" fill="currentColor"/>
        </svg>
        How Run Doctor works (and what it does NOT send)
      </summary>
      <ol class="run-doctor-explainer-steps">
        <li><strong>Reads</strong> this run's structured metrics — file counts, latencies, error codes, throughput, host peaks — from your local reports directory.</li>
        <li><strong>Finds</strong> past runs that hit the <em>same destination</em> (host, port, protocol). Other servers' runs are <em>never</em> compared — apples-to-apples only.</li>
        <li><strong>Builds</strong> a comparison summary. With redaction on (default), hostnames / usernames / file paths are replaced with stable opaque tokens (<span class="mono">&lt;host_a1b2&gt;</span>) before anything leaves this machine.</li>
        <li><strong>Sends</strong> the summary to your configured AI provider (Anthropic Claude). The API key lives encrypted in your vault; it never sits in plaintext on disk.</li>
        <li><strong>Renders</strong> the diagnosis below — copy or re-run any time.</li>
      </ol>
      <div class="run-doctor-explainer-policy">
        <div class="run-doctor-explainer-row">
          <span class="run-doctor-explainer-tag run-doctor-tag-ok">Sent</span>
          <span>numeric run summary (counts, latencies, errors), comparison deltas, baseline ids</span>
        </div>
        <div class="run-doctor-explainer-row">
          <span class="run-doctor-explainer-tag run-doctor-tag-no">Never sent</span>
          <span>passwords, private keys, vault secrets, raw CSV rows, file contents, the AI key itself</span>
        </div>
      </div>
      <p class="run-doctor-explainer-foot">
        Want to verify? Click <strong>Preview what will be sent</strong> below — it shows the exact prompt without spending any tokens.
      </p>
    </details>

    <!-- Setup block — shown only when the operator hasn't stored
         an AI key yet. Links to Trust → Vault so the canonical
         secret-store flow handles the key. -->
    <div class="run-doctor-setup" data-role="setup-block" hidden>
      <div class="run-doctor-setup-icon">🔑</div>
      <div class="run-doctor-setup-text">
        <strong>Set up your AI provider</strong>
        <p>Run Doctor needs an Anthropic API key. Paste yours here — it lives encrypted in your vault and never leaves the server in plaintext.</p>
      </div>
      <button type="button" class="btn btn-primary" data-role="open-vault">Set API key</button>
    </div>

    <!-- v0.20.6 — saved diagnosis history thread. Hidden when the
         server reports zero saved diagnoses for this run. The thread
         renders parents and follow-ups as nested <details>; clicking
         "Ask follow-up" on any turn primes the textbox below. -->
    <div class="run-doctor-history" data-role="history-block" hidden></div>

    <div class="run-doctor-controls">
      <div class="run-doctor-control-row">
        <label class="run-doctor-control-label" for="run-doctor-compare">Compare against</label>
        <select id="run-doctor-compare" class="run-doctor-select" data-role="compare-mode">
          <option value="auto">Auto — last 5 same-host runs</option>
          <option value="all">All same-host runs</option>
          <option value="pick">Pick specific date(s) below</option>
        </select>
        <span class="run-doctor-peers-count" data-role="peers-count">loading…</span>
      </div>

      <!-- v0.20.6 — model picker. Cost hint updates as the operator
           changes models so they see the trade-off (Haiku cheap,
           Opus deep) before paying. -->
      <div class="run-doctor-control-row">
        <label class="run-doctor-control-label" for="run-doctor-model">Model</label>
        <select id="run-doctor-model" class="run-doctor-select" data-role="model-select">
          <option value="">(default)</option>
        </select>
        <span class="run-doctor-cost-hint" data-role="cost-hint"></span>
      </div>

      <div class="run-doctor-control-row">
        <span class="run-doctor-selected" data-role="peers-list"></span>
      </div>

      <div class="run-doctor-peer-picker" data-role="peer-picker" hidden></div>

      <div class="run-doctor-control-row run-doctor-control-row-foot">
        <label class="run-doctor-redact">
          <input type="checkbox" data-role="redact-toggle" checked>
          <span>Redact hostname / users / paths before sending</span>
        </label>
        <span class="run-doctor-control-spacer"></span>
        <button type="button" class="btn btn-ghost" data-role="preview-prompt">
          Preview what will be sent
        </button>
        <button type="button" class="btn btn-primary" data-role="analyze">
          <svg viewBox="0 0 16 16" width="13" height="13" fill="currentColor" aria-hidden="true">
            <path d="M3 8l4 4 6-8" stroke="currentColor" stroke-width="1.6" fill="none" stroke-linecap="round" stroke-linejoin="round"/>
          </svg>
          <span>Analyze with AI</span>
        </button>
      </div>
    </div>

    <div class="run-doctor-preview" data-role="preview-block" hidden>
      <div class="run-doctor-preview-head">Preview — exactly what will be sent</div>
      <div class="run-doctor-preview-body" data-role="preview-content"></div>
    </div>

    <div class="run-doctor-result" data-role="result-block" hidden>
      <div class="run-doctor-result-head">Diagnosis</div>
      <div class="run-doctor-result-body" data-role="result-content"></div>
    </div>

    <!-- v0.20.6 — follow-up Q&A. Auto-revealed after the first
         diagnosis renders, OR when "Ask follow-up" is clicked on a
         past turn in the history thread. The parent-tag span shows
         which prior diagnosis the question will be threaded under. -->
    <div class="run-doctor-followup" data-role="followup-block" hidden>
      <div class="run-doctor-followup-head">
        Ask Run Doctor a follow-up about this run
        <span class="run-doctor-followup-parent-tag" data-role="followup-parent-tag"></span>
      </div>
      <textarea class="run-doctor-followup-input" data-role="followup-input"
                placeholder="e.g. Why specifically did POOL_EMPTY happen, and what should I tell my SFTP admin?"
                rows="3"></textarea>
      <div class="run-doctor-followup-actions">
        <button type="button" class="btn btn-ghost btn-sm" data-role="followup-clear">Clear parent</button>
        <span class="run-doctor-control-spacer"></span>
        <button type="button" class="btn btn-primary" data-role="followup-send">Ask follow-up</button>
      </div>
    </div>
  `;
}

function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}
function escapeAttr(s) { return escapeHTML(s); }
