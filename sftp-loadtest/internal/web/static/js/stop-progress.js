// stop-progress.js — v0.19.19 visible drain progress when the operator
// hits Stop.
//
// Pre-fix: clicking Stop fired POST /api/stop and went silent until the
// status badge eventually flipped to "idle" — but the runner has a
// multi-step teardown (drain pending track-IDs, flush CSV, seal meta,
// close pools) and on a long run the gap between click and idle could
// be 30+ seconds with nothing on screen explaining the wait. v0.19.19
// surfaces a drain dialog that polls /api/status and shows what the
// runner is still working on, plus a Force close affordance that
// dismisses the dialog without aborting the seal (the runner keeps
// draining server-side).
//
// Wired by intercepting clicks on every Stop button in the DOM (legacy
// #stopBtn and the topbar [data-role="topbar-stop"]) — both fire
// before the original handler, run the modal, and let the original
// handler also fire so existing semantics (CSRF, error toast) stay.

import { apiFetch } from './api.js';

const POLL_MS = 1000;

let active = false;

export function mountStopProgress() {
  // Hook all known stop buttons. Use capture-phase delegation on
  // document so dynamically-rendered Stop buttons (cluster, run cards)
  // get the same treatment. We DON'T preventDefault — the original
  // handler still runs and POSTs /api/stop; we just open the modal in
  // parallel and observe the drain.
  document.addEventListener('click', (ev) => {
    if (active) return;
    const btn = ev.target.closest && ev.target.closest('#stopBtn, [data-role="topbar-stop"], [data-action="stop-run"]');
    if (!btn) return;
    if (btn.disabled) return;
    // Defer one tick so the original click handler fires the POST first.
    setTimeout(() => openStopProgress(btn), 0);
  }, true);
}

function openStopProgress(triggerBtn) {
  active = true;
  const runId = triggerBtn?.dataset?.runId || '';
  const bd = document.createElement('div');
  bd.className = 'modal-backdrop';
  bd.innerHTML = `
    <div class="modal-panel stop-progress-panel" role="dialog" aria-modal="true" aria-label="Stopping run">
      <div class="modal-head">
        <span class="stop-progress-title">Stopping run${runId ? ' <span class="mono">' + escapeHTML(runId) + '</span>' : ''}</span>
      </div>
      <div class="modal-body">
        <div class="stop-progress-stages" data-role="stages">
          ${stageRow('signal',   'Signal sent to runner')}
          ${stageRow('drain',    'Draining pending track-IDs')}
          ${stageRow('downloads','Closing in-flight downloads')}
          ${stageRow('flush',    'Flushing CSV to disk')}
          ${stageRow('seal',     'Sealing run meta JSON')}
        </div>
        <div class="stop-progress-meta hint" data-role="meta">Polling…</div>
      </div>
      <div class="modal-foot stop-progress-foot">
        <button type="button" class="btn btn-secondary" data-role="force">Force close dialog</button>
        <button type="button" class="btn btn-primary" data-role="close" disabled>Close</button>
      </div>
    </div>`;
  document.body.appendChild(bd);

  const stagesEl = bd.querySelector('[data-role="stages"]');
  const metaEl   = bd.querySelector('[data-role="meta"]');
  const closeBtn = bd.querySelector('[data-role="close"]');
  const forceBtn = bd.querySelector('[data-role="force"]');

  let stopped = false;
  let pollTimer = null;
  const cleanup = () => {
    stopped = true;
    if (pollTimer) clearTimeout(pollTimer);
    bd.remove();
    active = false;
  };
  forceBtn.addEventListener('click', () => {
    // Operator override — close the dialog without waiting for seal.
    // The runner keeps draining server-side; the dialog is just an
    // observability surface, not a synchronisation point.
    cleanup();
  });
  closeBtn.addEventListener('click', cleanup);

  // Mark the first stage immediately — the POST has already left.
  setStage(stagesEl, 'signal', 'ok');

  async function poll() {
    if (stopped) return;
    let s = null;
    try {
      const r = await apiFetch('/api/status');
      if (r.ok) s = await r.json();
    } catch { /* network blip — retry */ }

    if (s) {
      const pending = Number(s.pending_trackids ?? 0);
      const dlQueue = Number(s.download_in_queue ?? 0);
      const recsLive = Number(s.records_in_memory ?? 0);
      const recsFlushed = Number(s.records_flushed ?? 0);
      const procFD = (s.proc && Number(s.proc.fd_in_use)) || 0;

      // Stage transitions: heuristic, based on which counter still has
      // work. Once a counter is 0 AND it was non-zero earlier, mark the
      // stage done; if it was 0 from the start, leave as pending until
      // the run's `active` flag flips, then stamp ok.
      setStage(stagesEl, 'drain',     pending  > 0 ? 'busy' : 'ok',  `pending track-IDs: ${pending}`);
      setStage(stagesEl, 'downloads', dlQueue  > 0 ? 'busy' : 'ok',  `in queue: ${dlQueue}`);
      setStage(stagesEl, 'flush',     recsLive > 0 ? 'busy' : 'ok',  `live: ${recsLive} · flushed: ${recsFlushed}`);

      const sealed = !s.active && pending === 0 && dlQueue === 0 && recsLive === 0;
      if (sealed) {
        setStage(stagesEl, 'seal', 'ok', 'meta JSON written');
        metaEl.textContent = 'Run sealed.';
        closeBtn.disabled = false;
        forceBtn.style.display = 'none';
        return; // stop polling
      } else if (!s.active) {
        setStage(stagesEl, 'seal', 'busy', 'sealing…');
      }
      metaEl.textContent = `pending=${pending} · downloads_in_queue=${dlQueue} · records_live=${recsLive} · flushed=${recsFlushed} · fd=${procFD}`;
    } else {
      metaEl.textContent = 'Lost connection to worker — keep this dialog open and the run will reseal when it returns.';
    }
    pollTimer = setTimeout(poll, POLL_MS);
  }
  poll();
}

function stageRow(id, label) {
  return `
    <div class="stop-progress-stage" data-stage="${id}" data-status="pending">
      <span class="stop-progress-stage-dot" aria-hidden="true"></span>
      <span class="stop-progress-stage-label">${label}</span>
      <span class="stop-progress-stage-detail" data-role="detail"></span>
    </div>`;
}

function setStage(root, id, status, detail) {
  const row = root.querySelector(`[data-stage="${id}"]`);
  if (!row) return;
  row.dataset.status = status;
  if (detail !== undefined) {
    const dEl = row.querySelector('[data-role="detail"]');
    if (dEl) dEl.textContent = detail;
  }
}

function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}
