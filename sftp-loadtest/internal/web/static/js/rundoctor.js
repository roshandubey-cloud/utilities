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

  // 1. Pull peers + AI-config status in parallel — both block the
  //    "Analyze" CTA's enable state.
  const [peersData, configData] = await Promise.all([
    fetchPeers(meta.id),
    fetchAIConfig(),
  ]);

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
      const body = currentRequestBody(meta, compareSelect, peerPicker, redactToggle);
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

  // 5. Analyze. Fires the real AI call. Streams a "thinking…"
  //    placeholder; replaces it with the narrative on response.
  analyzeBtn.addEventListener('click', async () => {
    if (!configData.configured) return;
    analyzeBtn.disabled = true;
    resultBlock.hidden = false;
    const out = resultBlock.querySelector('[data-role="result-content"]');
    out.innerHTML = '<div class="run-doctor-thinking">Run Doctor is analysing… this typically takes 5–15 s.</div>';
    try {
      const body = currentRequestBody(meta, compareSelect, peerPicker, redactToggle);
      const r = await apiFetch('/api/run-doctor/analyze', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
      if (!r.ok) throw new Error(await r.text());
      const j = await r.json();
      out.innerHTML = renderResult(j);
      wireResultActions(out, j);
    } catch (e) {
      out.innerHTML = `<div class="run-doctor-error">Analysis failed: ${escapeHTML(e.message || String(e))}</div>`;
    } finally {
      analyzeBtn.disabled = false;
    }
  });

  panel.scrollIntoView({ behavior: 'smooth', block: 'start' });
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

function currentRequestBody(meta, compareSelect, peerPicker, redactToggle) {
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
  return body;
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
  `;
}

function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}
function escapeAttr(s) { return escapeHTML(s); }
