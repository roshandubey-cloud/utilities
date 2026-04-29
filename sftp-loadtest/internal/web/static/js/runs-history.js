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
  const skips = Number(r.dispatch_skips || 0);
  const attempted = total + skips;
  const throttledPct = attempted > 0 ? (skips / attempted) * 100 : 0;
  const throttledBadge = skips > 0
    ? `<span class="badge badge-warning" title="${skips.toLocaleString()} file(s) skipped at dispatch time because every SSH slot was busy. Increase parallel_streams or add users to keep up at this fpm."><span class="dot"></span>Throttled · ${throttledPct.toFixed(1)}% skipped</span>`
    : '';
  const interruptedBadge = r.interrupted
    ? `<span class="badge badge-warning" title="The process exited before this run could finalise. Counts were reconstructed from the on-disk CSV; in-flight state at crash time is lost."><span class="dot"></span>Interrupted</span>`
    : '';
  const csvUrl = `/api/report.csv?run=${encodeURIComponent(r.id)}`;
  return `
    <article class="runs-history-card">
      <header class="runs-history-card-head">
        <div class="runs-history-id">
          <div class="mono">${escapeHTML(r.id)}</div>
          <div class="body-small" style="color:var(--text-tertiary)">${formatStarted(r.started_at)}${r.stopped_at ? ' · ' + formatDuration(r.started_at, r.stopped_at) : ''}${throttledBadge ? ' · ' + throttledBadge : ''}${interruptedBadge ? ' · ' + interruptedBadge : ''}</div>
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
      ${latencyMarkup(r)}
      ${analysisMarkup(r)}
    </article>`;
}

// Renders the per-stage latency percentile points the runner persisted.
// Hidden entirely when the run pre-dates this feature (no latency on the
// payload). Upload-COR is shown next to Upload so the operator can read
// the queue-wait skew at a glance.
function latencyMarkup(r) {
  const lat = r.latency;
  if (!lat) return '';
  const ms = (ns) => ns == null ? '—' : (ns / 1e6).toFixed(ns >= 100e6 ? 0 : ns >= 10e6 ? 1 : 2);
  function stage(label, s, hint) {
    if (!s) return `
      <div class="runs-history-lat-stage">
        <div class="eyebrow">${escapeHTML(label)}</div>
        <div class="metric-default" style="color:var(--text-tertiary)">—</div>
        <div class="body-small">${escapeHTML(hint || 'no observations')}</div>
      </div>`;
    return `
      <div class="runs-history-lat-stage">
        <div class="eyebrow">${escapeHTML(label)}</div>
        <div class="runs-history-lat-row body-small tabular">
          <span><span class="muted">p50</span> ${ms(s.p50_ns)}<span class="muted"> ms</span></span>
          <span><span class="muted">p95</span> ${ms(s.p95_ns)}<span class="muted"> ms</span></span>
          <span><span class="muted">p99</span> ${ms(s.p99_ns)}<span class="muted"> ms</span></span>
          <span><span class="muted">p99.9</span> ${ms(s.p999_ns)}<span class="muted"> ms</span></span>
        </div>
        <div class="body-small" style="color:var(--text-tertiary)">${s.count.toLocaleString()} samples · max ${ms(s.max_ns)} ms${hint ? ' · ' + escapeHTML(hint) : ''}</div>
      </div>`;
  }
  return `
    <div class="runs-history-latency">
      ${stage('Upload latency', lat.upload, 'wire time')}
      ${stage('Upload latency · COR', lat.upload_cor, 'incl. queue wait')}
      ${stage('Dial', lat.dial, 'cold reconnects')}
    </div>`;
}

// Renders the host-capacity line + the analyzer's suggestions.
// Hidden entirely when the run pre-dates the analyzer (no peak fields persisted).
function analysisMarkup(r) {
  const hasInfra = (r.peak_cpu_percent != null) || (r.peak_window_mbps != null) || (r.num_cpu != null);
  const suggestions = Array.isArray(r.suggestions) ? r.suggestions : [];
  if (!hasInfra && suggestions.length === 0) return '';
  const infraBits = [];
  if (r.peak_cpu_percent != null && r.num_cpu) {
    infraBits.push(`CPU peak ${Number(r.peak_cpu_percent).toFixed(0)}% / ${r.num_cpu} cores`);
  } else if (r.peak_cpu_percent != null) {
    infraBits.push(`CPU peak ${Number(r.peak_cpu_percent).toFixed(0)}%`);
  }
  if (r.peak_window_mbps != null && Number(r.peak_window_mbps) > 0) {
    infraBits.push(`net peak ${Number(r.peak_window_mbps).toFixed(1)} MB/s`);
  }
  if (r.peak_fd_in_use != null && Number(r.peak_fd_in_use) > 0) {
    infraBits.push(`FD peak ${Number(r.peak_fd_in_use)}`);
  }
  if (r.peak_heap_mb != null && Number(r.peak_heap_mb) > 0) {
    infraBits.push(`heap ${Number(r.peak_heap_mb).toFixed(0)} MiB`);
  }
  const infraLine = infraBits.length
    ? `<div class="runs-history-infra body-small">Local host: ${escapeHTML(infraBits.join(' · '))}</div>`
    : '';
  const sugList = suggestions.length
    ? `<ul class="runs-history-suggestions">
         ${suggestions.map(s => `
           <li class="runs-history-suggestion runs-history-sev-${escapeAttr(s.severity || 'info')}">
             <div class="runs-history-suggestion-title">${escapeHTML(s.title || '')}</div>
             ${s.detail ? `<div class="runs-history-suggestion-detail body-small">${escapeHTML(s.detail)}</div>` : ''}
             ${s.action ? `<div class="runs-history-suggestion-action body-small"><strong>Try:</strong> ${escapeHTML(s.action)}</div>` : ''}
           </li>`).join('')}
       </ul>`
    : '';
  return `<div class="runs-history-analysis">${infraLine}${sugList}</div>`;
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
