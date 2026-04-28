// runs-history.js — rich Previous-runs panel.
//
// Replaces the legacy 5-column table with overview cards that surface what
// the user actually wants to know about each historical run: success rate,
// upload/download user counts, parallel streams, files-per-minute, plus the
// usual id/timestamps/throughput. Driven by /api/runs (extended in v0.4.6
// to include the new persisted fields).

import { apiFetch } from './api.js';

const REFRESH_MS = 8000;

export function mountRunsHistory(rootSelector) {
  const root = document.querySelector(rootSelector);
  if (!root) return;

  // Relocate to the legacy right-column slot where Previous-runs lived so
  // operators find it where it has always been. Tagged as a Review-step
  // panel so the wizard's existing visibility filter handles show/hide.
  const legacyRunsCard = document.querySelector('.card:has(#runs_body)') || document.getElementById('runs_body')?.closest('.card');
  if (legacyRunsCard && legacyRunsCard.parentNode && root.parentNode !== legacyRunsCard.parentNode) {
    legacyRunsCard.parentNode.insertBefore(root, legacyRunsCard.nextSibling);
  }
  root.dataset.step = 'review';

  const slot = root.querySelector('[data-role="content"]');
  const counter = root.querySelector('[data-role="count"]');

  async function refresh() {
    try {
      const res = await apiFetch('/api/runs');
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const j = await res.json();
      const runs = (j.runs || []).filter((r) => r.total_files > 0);
      if (counter) counter.textContent = runs.length === 0
        ? 'no completed runs yet'
        : (runs.length === 1 ? '1 completed run' : `${runs.length} completed runs`);
      if (runs.length === 0) {
        slot.innerHTML = `
          <div class="runs-history-empty">
            <div class="hero-empty-icon" aria-hidden="true">
              <svg viewBox="0 0 64 64" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
                <rect x="10" y="14" width="44" height="36" rx="2"/>
                <path d="M10 22h44"/>
              </svg>
            </div>
            <div class="body-secondary">Finished runs will be listed here.</div>
          </div>`;
        return;
      }
      slot.innerHTML = runs.slice(0, 10).map(rowMarkup).join('');
      slot.querySelectorAll('[data-action="view"]').forEach((btn) => {
        btn.addEventListener('click', (ev) => {
          ev.preventDefault();
          // Reuse the legacy [data-view] click hook so records.js (which
          // listens on document) swaps to this run.
          const proxy = document.createElement('button');
          proxy.dataset.view = btn.dataset.runId;
          document.body.appendChild(proxy);
          proxy.click();
          proxy.remove();
          // Scroll to live activity so the user sees the records they asked for.
          const tbl = document.querySelector('[data-component="records"]');
          if (tbl) tbl.scrollIntoView({ behavior: 'smooth', block: 'start' });
        });
      });
    } catch {
      if (counter) counter.textContent = 'disconnected';
    } finally {
      setTimeout(refresh, REFRESH_MS);
    }
  }
  refresh();
}

function rowMarkup(r) {
  const total = Number(r.total_files || 0);
  const failed = Number(r.failed_files || 0);
  const succeeded = total - failed;
  const successPct = total > 0 ? (succeeded / total) * 100 : 0;
  const successCls = successPct >= 99 ? 'ok' : successPct >= 85 ? 'warn' : 'bad';
  const csvUrl = `/api/report.csv?run=${encodeURIComponent(r.id)}`;
  return `
    <article class="runs-history-card">
      <header class="runs-history-card-head">
        <div class="runs-history-id">
          <div class="mono">${escapeHTML(r.id)}</div>
          <div class="body-small" style="color:var(--text-tertiary)">${formatStarted(r.started_at)}${r.stopped_at ? ' · ' + formatDuration(r.started_at, r.stopped_at) : ''}</div>
        </div>
        <div class="runs-history-actions">
          <a class="btn btn-sm btn-ghost" href="${csvUrl}" download data-external="1">CSV</a>
          <button class="btn btn-sm btn-secondary" type="button" data-action="view" data-run-id="${escapeAttr(r.id)}">View records</button>
        </div>
      </header>
      <div class="runs-history-stats">
        <div class="runs-history-stat">
          <div class="eyebrow">Success rate</div>
          <div class="metric-default tabular runs-history-success-${successCls}">${total > 0 ? successPct.toFixed(1) : '—'}<span class="runs-history-pct">${total > 0 ? '%' : ''}</span></div>
          <div class="body-small">${total > 0 ? `${succeeded.toLocaleString()} ok · ${failed.toLocaleString()} failed` : 'no records'}</div>
        </div>
        <div class="runs-history-stat">
          <div class="eyebrow">Files</div>
          <div class="metric-default tabular">${total.toLocaleString()}</div>
          <div class="body-small">${formatBytes(r.total_bytes)} · ${formatRate(r.overall_mbps)} Mbps</div>
        </div>
        <div class="runs-history-stat">
          <div class="eyebrow">Upload</div>
          <div class="metric-default tabular">${r.upload_users || 0}<span class="runs-history-pct"> users</span></div>
          <div class="body-small">${r.parallel_streams || '?'} streams · ${r.files_per_minute || '?'} fpm</div>
        </div>
        <div class="runs-history-stat">
          <div class="eyebrow">Download</div>
          ${r.download_enabled
            ? `<div class="metric-default tabular">${r.download_users || 0}<span class="runs-history-pct"> users</span></div>
               <div class="body-small">${r.download_parallel_streams || '?'} streams</div>`
            : `<div class="metric-default" style="color:var(--text-tertiary)">—</div>
               <div class="body-small">disabled</div>`}
        </div>
      </div>
    </article>`;
}

// ---------- formatters ----------
function formatBytes(n) {
  if (!n) return '—';
  const u = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let i = 0; let v = Number(n);
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
  const decimals = v >= 100 ? 0 : v >= 10 ? 1 : 2;
  return `${v.toFixed(decimals)} ${u[i]}`;
}
function formatRate(n) {
  if (n == null) return '—';
  if (n >= 1000) return Number(n).toFixed(0);
  if (n >= 100)  return Number(n).toFixed(1);
  return Number(n).toFixed(2);
}
function formatStarted(iso) {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const diffMs = Date.now() - d.getTime();
  if (diffMs < 60_000) return 'just now';
  if (diffMs < 3_600_000) return `${Math.floor(diffMs / 60_000)} min ago`;
  if (diffMs < 86_400_000) return `${Math.floor(diffMs / 3_600_000)} h ago`;
  return d.toLocaleString();
}
function formatDuration(startISO, stopISO) {
  if (!startISO || !stopISO) return '';
  const start = new Date(startISO).getTime();
  const stop = new Date(stopISO).getTime();
  if (!Number.isFinite(start) || !Number.isFinite(stop)) return '';
  const sec = Math.max(0, Math.floor((stop - start) / 1000));
  if (sec < 60) return `${sec}s`;
  const m = Math.floor(sec / 60);
  if (m < 60) return `${m}m ${String(sec % 60).padStart(2, '0')}s`;
  return `${Math.floor(m / 60)}h ${String(m % 60).padStart(2, '0')}m`;
}
function escapeHTML(s) { return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c])); }
function escapeAttr(s) { return String(s).replace(/"/g, '&quot;'); }
