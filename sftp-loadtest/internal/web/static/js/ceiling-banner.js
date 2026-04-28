// ceiling-banner.js — surface dispatch-skip events while a run is active.
//
// The runner's dispatcher uses a non-blocking semaphore per (user × parallel
// streams). When the semaphore is full at tick time, the file is SKIPPED and
// DispatchSkips is incremented. Without UI surfacing, operators never realise
// they're running below the requested rate — they see "all uploads OK in the
// records table" and assume the tool met their fpm target.
//
// This module polls /api/status every 2s while a run is active. When the
// skip counter rises:
//   * On the first non-zero observation, a one-shot toast announces the
//     ceiling hit with a one-line remediation suggestion.
//   * A persistent banner mounts above the Live activity panel showing the
//     skipped count, the percentage of attempted files lost to the ceiling,
//     and the same suggestion. The banner clears when the run ends.

import { apiFetch } from './api.js';
import { pushToast } from './toast.js';

const POLL_MS = 2000;

export function mountCeilingBanner() {
  let lastSkips = 0;
  let toasted = false;
  let bannerEl = null;

  async function tick() {
    try {
      const res = await apiFetch('/api/status');
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const j = await res.json();
      if (!j.active) {
        clearBanner();
        toasted = false;
        lastSkips = 0;
        return;
      }
      const skips = Number(j.dispatch_skips || 0);
      const m = j.metrics || {};
      const totalFiles = Number(m.total_files || 0);
      const attempted = totalFiles + skips;
      const skipPct = attempted > 0 ? (skips / attempted) * 100 : 0;

      if (skips > 0 && skips > lastSkips) {
        if (!toasted) {
          pushToast(
            `Capacity ceiling hit — ${skips.toLocaleString()} file(s) skipped. ` +
            `Increase parallel streams or add more upload users to keep up.`,
            'warn',
            { timeout: 8000 }
          );
          toasted = true;
        }
        renderBanner(skips, skipPct);
      } else if (skips === 0) {
        clearBanner();
      }
      lastSkips = skips;
    } catch {
      // Network blip — try again next tick.
    } finally {
      setTimeout(tick, POLL_MS);
    }
  }

  function renderBanner(skips, pct) {
    if (!bannerEl) {
      bannerEl = document.createElement('div');
      bannerEl.className = 'ceiling-banner';
      bannerEl.setAttribute('role', 'alert');
      const recordsPanel = document.querySelector('[data-component="records"]');
      if (recordsPanel && recordsPanel.parentNode) {
        recordsPanel.parentNode.insertBefore(bannerEl, recordsPanel);
      } else {
        document.querySelector('.app-content')?.prepend(bannerEl);
      }
    }
    bannerEl.innerHTML = `
      <span class="ceiling-banner-icon" aria-hidden="true">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
          <path d="M10.6 3.5l-8 14a1.6 1.6 0 0 0 1.4 2.5h16a1.6 1.6 0 0 0 1.4-2.5l-8-14a1.6 1.6 0 0 0-2.8 0z"/>
          <path d="M12 9v4"/>
          <path d="M12 17h.01"/>
        </svg>
      </span>
      <div class="ceiling-banner-text">
        <strong>Capacity ceiling hit.</strong>
        ${skips.toLocaleString()} file${skips === 1 ? '' : 's'} skipped (${pct.toFixed(1)}% of attempted) because every SSH slot was busy at dispatch time.
        Increase <span class="mono">parallel_streams</span> per user, add more users, or lower the requested files-per-minute.
      </div>`;
  }

  function clearBanner() {
    if (bannerEl) {
      bannerEl.remove();
      bannerEl = null;
    }
  }

  tick();
}
