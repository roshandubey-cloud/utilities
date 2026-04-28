// run-header.js — sticky banner shown whenever a run is active.
// Polls /api/status every 1 s while active; hides itself when no run.
// "Stop run" delegates to POST /api/stop and shows a toast.

import { apiFetch, apiPostJSON } from './api.js';
import { pushToast } from './toast.js';

const POLL_ACTIVE_MS = 1000;
const POLL_IDLE_MS   = 4000;

export function mountRunHeader(rootSelector) {
  const root = document.querySelector(rootSelector);
  if (!root) return;
  const idEl     = root.querySelector('[data-role="id"]');
  const elapsedEl= root.querySelector('[data-role="elapsed"]');
  const filesEl  = root.querySelector('[data-role="files"]');
  const mbpsEl   = root.querySelector('[data-role="mbps"]');
  const stopEl   = root.querySelector('[data-role="stop"]');

  let startedAt = 0;
  let elapsedTimer = null;

  function setActive(on) {
    root.dataset.active = on ? 'true' : 'false';
    if (!on) {
      if (elapsedTimer) clearInterval(elapsedTimer);
      elapsedTimer = null;
    }
  }

  function fmtElapsed(sec) {
    if (sec < 60) return `${sec}s`;
    const m = Math.floor(sec / 60);
    const s = sec % 60;
    if (m < 60) return `${m}m ${String(s).padStart(2, '0')}s`;
    const h = Math.floor(m / 60);
    return `${h}h ${String(m % 60).padStart(2, '0')}m`;
  }

  if (stopEl) {
    stopEl.addEventListener('click', async (ev) => {
      ev.preventDefault();
      stopEl.disabled = true;
      try {
        await apiPostJSON('/api/stop', {});
        pushToast('Run stopped', 'info');
      } catch (e) {
        pushToast(`Stop failed: ${e.message || e}`, 'error');
      } finally {
        stopEl.disabled = false;
      }
    });
  }

  async function refresh() {
    let active = false;
    try {
      const res = await apiFetch('/api/status');
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const j = await res.json();
      active = !!j.active;
      if (active) {
        const m = j.metrics || {};
        if (idEl)      idEl.textContent      = j.run_id ? ' · ' + j.run_id : '';
        if (filesEl)   filesEl.textContent   = String(m.total_files || 0);
        if (mbpsEl)    mbpsEl.textContent    = (m.overall_mbps || 0).toFixed(2);
        if (j.run_started_at) {
          const t = new Date(j.run_started_at).getTime();
          if (Number.isFinite(t)) startedAt = t;
        }
        if (elapsedEl && startedAt) {
          elapsedEl.textContent = fmtElapsed(Math.max(0, Math.floor((Date.now() - startedAt) / 1000)));
        }
        if (!elapsedTimer) {
          elapsedTimer = setInterval(() => {
            if (elapsedEl && startedAt) {
              elapsedEl.textContent = fmtElapsed(Math.max(0, Math.floor((Date.now() - startedAt) / 1000)));
            }
          }, 1000);
        }
        setActive(true);
      } else {
        setActive(false);
        startedAt = 0;
      }
    } catch {
      setActive(false);
    } finally {
      setTimeout(refresh, active ? POLL_ACTIVE_MS : POLL_IDLE_MS);
    }
  }
  refresh();
}
