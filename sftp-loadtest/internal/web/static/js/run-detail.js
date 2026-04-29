// run-detail.js — β1 of the v0.9.0 redesign.
//
// Full-pane view of a finished run. Replaces the inline runs-history
// card as the primary "look at the past" surface. The sidebar's Recent
// runs section (and the runs-history cards that still render below)
// open this view; the back button returns to the workbench layout.
//
// Sources: /api/runs (single entry by id) for the meta, and
// /api/report.csv?run=<id> for the records.
//
// What renders:
//   * Run header — id, started/stopped, duration, badges (interrupted, throttled).
//   * KPI strip — total files, success rate, throughput, dispatch_skips.
//   * Latency charts — Upload + COR + Dial as small bar charts of the
//     four percentiles. Reuses the chart-panel chrome from α3.
//   * Workload card — users / streams / fpm / download mode.
//   * Local host peaks — CPU, FD, goroutines, heap, peak Mbps.
//   * Suggestions — same severity-coloured analyzer panel.
//   * Records table — paged tail of the CSV with filter chips.

import { apiFetch } from './api.js';
import { pushToast } from './toast.js';

const CHIPS = [
  { id: 'all',     label: 'All' },
  { id: 'failed',  label: 'Failed' },
  { id: 'slow',    label: 'Slow' },
  { id: 'pending', label: 'Pending' },
];

let viewEl = null;

export function mountRunDetail() {
  // Listen for sidebar / runs-history clicks. Both fire a synthetic
  // [data-view="<id>"] proxy; we intercept it BEFORE records.js's
  // bubble-phase handler so the detail view opens AND the records
  // table swap still happens. records.js listens at document level,
  // so we use capture phase to short-circuit when we want a detail
  // takeover, not a records swap.
  document.addEventListener('click', (ev) => {
    const proxy = ev.target.closest && ev.target.closest('[data-view-detail]');
    if (proxy) {
      ev.preventDefault();
      ev.stopPropagation();
      const id = proxy.getAttribute('data-view-detail');
      if (id) openDetail(id);
    }
  }, true);

  // Add a "Detail" button to each runs-history card. The cards are
  // rendered async (3 s polling) so we observe insertions and inject
  // the button when a card appears without one.
  const observer = new MutationObserver(decorateCards);
  observer.observe(document.body, { childList: true, subtree: true });
  decorateCards();
}

function decorateCards() {
  document.querySelectorAll('[data-component="runs-history"] .runs-history-card').forEach((card) => {
    if (card.dataset.detailDecorated) return;
    card.dataset.detailDecorated = '1';
    const actions = card.querySelector('.runs-history-actions');
    const idEl = card.querySelector('.runs-history-id .mono');
    if (!actions || !idEl) return;
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'btn btn-sm btn-secondary';
    btn.textContent = 'Open';
    btn.title = 'Open detail view';
    btn.dataset.viewDetail = idEl.textContent.trim();
    actions.appendChild(btn);
  });
}

export async function openDetail(runID) {
  if (!runID) return;
  const main = document.querySelector('.shell-main');
  if (!main) return;

  if (!viewEl) {
    viewEl = document.createElement('section');
    viewEl.className = 'run-detail-view';
    viewEl.dataset.component = 'run-detail';
    main.parentNode.insertBefore(viewEl, main);
  }
  // Hide the workbench, show the detail.
  main.dataset.hidden = '1';
  main.style.display = 'none';
  viewEl.style.display = '';

  viewEl.innerHTML = `<div class="run-detail-loading">Loading run…</div>`;

  let meta;
  try {
    const r = await apiFetch('/api/runs');
    if (!r.ok) throw new Error(`HTTP ${r.status}`);
    const j = await r.json();
    meta = (j.runs || []).find((x) => x.id === runID);
  } catch (e) {
    viewEl.innerHTML = `<div class="run-detail-error">Failed to load: ${escapeHTML(e.message || String(e))}</div>`;
    return;
  }
  if (!meta) {
    viewEl.innerHTML = `<div class="run-detail-error">Run <span class="mono">${escapeHTML(runID)}</span> not found.</div>`;
    return;
  }

  viewEl.innerHTML = renderDetail(meta);
  wireDetail(viewEl, meta);
}

function closeDetail() {
  if (viewEl) {
    viewEl.style.display = 'none';
  }
  const main = document.querySelector('.shell-main');
  if (main) {
    main.dataset.hidden = '0';
    main.style.display = '';
  }
}

function renderDetail(m) {
  const total = Number(m.total_files || 0);
  const failed = Number(m.failed_files || 0);
  const ok = total - failed;
  const successPct = total > 0 ? (ok / total) * 100 : 0;
  const skips = Number(m.dispatch_skips || 0);
  const attempted = total + skips;
  const skippedPct = attempted > 0 ? (skips / attempted) * 100 : 0;
  const lat = m.latency || {};
  const csvUrl = `/api/report.csv?run=${encodeURIComponent(m.id)}`;
  const dur = formatDuration(m.started_at, m.stopped_at);

  return `
    <header class="run-detail-head">
      <button class="btn btn-ghost run-detail-back" type="button" data-role="back">
        <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor"
             stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M9 4l-4 4 4 4"/><path d="M5 8h7"/></svg>
        <span>Back</span>
      </button>
      <div class="run-detail-title">
        <div class="run-detail-id mono">${escapeHTML(m.id)}</div>
        <div class="run-detail-sub body-small">
          ${m.started_at ? formatStarted(m.started_at) : '—'}${dur ? ' · ' + dur : ''}
          ${m.interrupted ? ' · <span class="badge badge-warning"><span class="dot"></span>Interrupted</span>' : ''}
          ${skips > 0 ? ` · <span class="badge badge-warning"><span class="dot"></span>Throttled · ${skippedPct.toFixed(1)}%</span>` : ''}
        </div>
      </div>
      <div class="run-detail-actions">
        <a class="btn btn-secondary" href="${csvUrl}" download data-external="1">Download CSV</a>
      </div>
    </header>

    <div class="run-detail-kpis">
      ${kpi('Success rate', total > 0 ? successPct.toFixed(1) + '%' : '—', successPct >= 99 ? 'ok' : successPct >= 85 ? 'warn' : 'bad')}
      ${kpi('Files', total.toLocaleString(), '')}
      ${kpi('Bytes', formatBytes(m.total_bytes), '')}
      ${kpi('Overall MB/s', formatRate(m.overall_mbps), '')}
      ${kpi('Failed', failed.toLocaleString(), failed > 0 ? 'bad' : '')}
      ${kpi('Skipped', skips.toLocaleString(), skips > 0 ? 'warn' : '')}
    </div>

    <div class="run-detail-grid">
      <div class="run-detail-panel">
        <div class="run-detail-panel-title">Latency percentiles</div>
        ${renderLatencyBars(lat)}
      </div>
      <div class="run-detail-panel">
        <div class="run-detail-panel-title">Workload</div>
        <dl class="run-detail-defs">
          ${defRow('Upload users',   String(m.upload_users || 0))}
          ${defRow('Parallel streams', String(m.parallel_streams || '—'))}
          ${defRow('Files per minute', String(m.files_per_minute || '—'))}
          ${defRow('Download', m.download_enabled
            ? `${m.download_users || 0} users · ${m.download_parallel_streams || '?'} streams · ${m.download_match_mode === 'filename' ? 'filename match' : 'trackid'}`
            : 'disabled')}
        </dl>
      </div>
      <div class="run-detail-panel">
        <div class="run-detail-panel-title">Local host peaks</div>
        <dl class="run-detail-defs">
          ${defRow('Peak CPU', m.peak_cpu_percent != null ? `${Number(m.peak_cpu_percent).toFixed(0)}%` : '—')}
          ${defRow('Avg CPU',  m.avg_cpu_percent  != null ? `${Number(m.avg_cpu_percent).toFixed(0)}%` : '—')}
          ${defRow('Cores',    String(m.num_cpu || '—'))}
          ${defRow('Peak FD',  m.peak_fd_in_use != null ? Number(m.peak_fd_in_use).toLocaleString() : '—')}
          ${defRow('Peak goroutines', m.peak_goroutines != null ? String(m.peak_goroutines) : '—')}
          ${defRow('Peak heap',  m.peak_heap_mb != null ? `${Number(m.peak_heap_mb).toFixed(0)} MiB` : '—')}
          ${defRow('Peak window MB/s', m.peak_window_mbps != null ? Number(m.peak_window_mbps).toFixed(1) : '—')}
        </dl>
      </div>
    </div>

    ${(m.suggestions && m.suggestions.length > 0) ? `
      <div class="run-detail-panel run-detail-suggestions">
        <div class="run-detail-panel-title">Analysis</div>
        <ul class="runs-history-suggestions">
          ${m.suggestions.map((s) => `
            <li class="runs-history-suggestion runs-history-sev-${escapeAttr(s.severity || 'info')}">
              <div class="runs-history-suggestion-title">${escapeHTML(s.title || '')}</div>
              ${s.detail ? `<div class="runs-history-suggestion-detail body-small">${escapeHTML(s.detail)}</div>` : ''}
              ${s.action ? `<div class="runs-history-suggestion-action body-small"><strong>Try:</strong> ${escapeHTML(s.action)}</div>` : ''}
            </li>`).join('')}
        </ul>
      </div>
    ` : ''}

    <div class="run-detail-panel">
      <div class="run-detail-panel-title">Records</div>
      <div class="run-detail-records-chips">
        ${CHIPS.map((c) => `<button class="filter-chip" type="button" data-filter="${c.id}" data-active="${c.id === 'all'}">${escapeHTML(c.label)}</button>`).join('')}
        <span class="run-detail-records-count" data-role="records-count"></span>
      </div>
      <div class="run-detail-records" data-role="records">Loading records…</div>
    </div>`;
}

function kpi(label, value, tone) {
  return `
    <div class="run-detail-kpi ${tone ? `run-detail-kpi-${tone}` : ''}">
      <div class="kpi-label">${escapeHTML(label)}</div>
      <div class="kpi-value tabular">${escapeHTML(value)}</div>
    </div>`;
}

function defRow(label, value) {
  return `<dt>${escapeHTML(label)}</dt><dd>${escapeHTML(String(value))}</dd>`;
}

function renderLatencyBars(lat) {
  const stages = [
    { key: 'upload',     label: 'Upload (wire)' },
    { key: 'upload_cor', label: 'Upload (COR)' },
    { key: 'dial',       label: 'Dial (cold reconnect)' },
  ];
  const cells = stages.map((s) => {
    const stage = lat[s.key];
    if (!stage) {
      return `
        <div class="run-detail-latency-row">
          <div class="run-detail-latency-label">${escapeHTML(s.label)}</div>
          <div class="run-detail-latency-bars run-detail-latency-empty">no observations</div>
        </div>`;
    }
    return `
      <div class="run-detail-latency-row">
        <div class="run-detail-latency-label">${escapeHTML(s.label)} <span class="muted">${formatNumberShort(stage.count)} samples</span></div>
        <div class="run-detail-latency-bars">
          ${barCell('p50',   stage.p50_ns,  stage.max_ns)}
          ${barCell('p95',   stage.p95_ns,  stage.max_ns)}
          ${barCell('p99',   stage.p99_ns,  stage.max_ns)}
          ${barCell('p99.9', stage.p999_ns, stage.max_ns)}
          ${barCell('max',   stage.max_ns,  stage.max_ns)}
        </div>
      </div>`;
  }).join('');
  return `<div class="run-detail-latency">${cells}</div>`;
}

function barCell(label, ns, maxNs) {
  if (ns == null || maxNs == null || maxNs <= 0) {
    return `<div class="run-detail-latency-cell"><div class="cell-label">${label}</div><div class="cell-value">—</div></div>`;
  }
  const pct = Math.max(2, Math.min(100, (ns / maxNs) * 100));
  return `
    <div class="run-detail-latency-cell">
      <div class="cell-label">${label}</div>
      <div class="cell-bar"><span style="width:${pct.toFixed(1)}%"></span></div>
      <div class="cell-value">${formatMs(ns)}</div>
    </div>`;
}

function wireDetail(view, meta) {
  view.querySelector('[data-role="back"]').addEventListener('click', (ev) => {
    ev.preventDefault();
    closeDetail();
  });
  // Filter chips swap CSS scope; records render below.
  const records = view.querySelector('[data-role="records"]');
  const countEl = view.querySelector('[data-role="records-count"]');
  view.querySelectorAll('.filter-chip').forEach((c) => {
    c.addEventListener('click', () => {
      view.querySelectorAll('.filter-chip').forEach((x) => x.dataset.active = 'false');
      c.dataset.active = 'true';
      renderRecords(records, countEl, meta.id, c.dataset.filter);
    });
  });
  renderRecords(records, countEl, meta.id, 'all');
}

async function renderRecords(slot, countEl, runID, filter) {
  slot.textContent = 'Loading records…';
  let csv;
  try {
    const r = await apiFetch(`/api/report.csv?run=${encodeURIComponent(runID)}`);
    csv = await r.text();
  } catch (e) {
    slot.textContent = 'Failed to load CSV.';
    return;
  }
  const rows = parseCSV(csv);
  if (!rows.length) {
    slot.innerHTML = '<div class="empty">No records.</div>';
    if (countEl) countEl.textContent = '';
    return;
  }
  const header = rows[0];
  const filtered = rows.slice(1).filter((r) => matchesFilter(r, header, filter)).slice(0, 200);
  if (countEl) countEl.textContent = `showing ${filtered.length} of ${rows.length - 1}`;
  // Pick a compact column set for the detail view.
  const cols = ['user', 'kind', 'filename', 'duration_sec', 'size_bytes', 'upload_mbps', 'error_code', 'download_user'];
  const idx = cols.map((c) => header.indexOf(c));
  slot.innerHTML = `
    <div class="tbl-wrap">
      <table>
        <thead><tr>${cols.map((c) => `<th>${escapeHTML(c)}</th>`).join('')}</tr></thead>
        <tbody>
          ${filtered.map((row) => `
            <tr>${idx.map((i) => `<td>${escapeHTML((i >= 0 && row[i] != null) ? row[i] : '')}</td>`).join('')}</tr>
          `).join('')}
        </tbody>
      </table>
    </div>`;
}

function matchesFilter(row, header, filter) {
  if (filter === 'all') return true;
  const errCol = header.indexOf('error_code');
  const trackCol = header.indexOf('track_id');
  if (filter === 'failed')  return errCol >= 0 && row[errCol] && row[errCol].trim() !== '';
  if (filter === 'pending') return trackCol >= 0 && (!row[trackCol] || row[trackCol].trim() === '');
  if (filter === 'slow')    return row.some((c) => c === 'true' || c === 'TRUE');
  return true;
}

// CSV parser — tolerant of the analysis trailer (#-prefixed rows after a
// blank line). Stops at the first blank line.
function parseCSV(text) {
  const out = [];
  for (const raw of text.split(/\r?\n/)) {
    if (raw === '') break;
    if (raw.startsWith('#')) continue;
    out.push(raw.split(',').map((s) => s));
  }
  return out;
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
function formatNumberShort(n) {
  if (n == null) return '—';
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)}k`;
  return String(n);
}
function formatMs(ns) {
  if (ns == null) return '—';
  const ms = ns / 1e6;
  if (ms >= 1000) return `${(ms / 1000).toFixed(2)}s`;
  if (ms >= 100)  return `${ms.toFixed(0)}ms`;
  return `${ms.toFixed(1)}ms`;
}
function formatStarted(iso) {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
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
