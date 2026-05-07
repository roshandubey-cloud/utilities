// runs-history.js — rich Previous-runs panel.
//
// Replaces the legacy 5-column table with overview cards that surface what
// the user actually wants to know about each historical run: success rate,
// upload/download user counts, parallel streams, files-per-minute, plus the
// usual id/timestamps/throughput. Driven by /api/runs (extended in v0.4.6
// to include the new persisted fields).

import { apiFetch } from './api.js';

// Polling cadence. Was 8 s — operators sat staring at a stale "no
// completed runs yet" empty state for the better part of a minute
// after a 5-second smoke run finished. 3 s is a fair compromise: the
// panel typically updates within one beat of the run finishing while
// the request rate stays trivial.
const REFRESH_MS = 3000;

// v0.15.0 — run comparison. Module-scope state for the two run IDs
// currently selected for diff. Persisted to localStorage so a
// refresh during a comparison doesn't drop the selection.
const CMP_KEY = 'sftp-loadtest-cmp-v1';
const cmpState = (() => {
  try { return JSON.parse(localStorage.getItem(CMP_KEY) || '[]'); } catch { return []; }
})();
function cmpSave() { try { localStorage.setItem(CMP_KEY, JSON.stringify(cmpState)); } catch {} }
function cmpToggle(id) {
  const i = cmpState.indexOf(id);
  if (i >= 0) cmpState.splice(i, 1);
  else if (cmpState.length < 2) cmpState.push(id);
  else { cmpState.shift(); cmpState.push(id); } // keep most-recent two
  cmpSave();
}
function cmpClear() { cmpState.length = 0; cmpSave(); }

export function mountRunsHistory(rootSelector) {
  const root = document.querySelector(rootSelector);
  if (!root) return;

  // The α2+ shell places this component into the History view container
  // already; we no longer relocate. Earlier builds moved it next to the
  // legacy "Previous runs" .card inside .grid, but in v0.9.1 .grid lives
  // in the Configure view — relocating there would hide this panel from
  // the History view it's supposed to live in. Honour the shell.

  const slot = root.querySelector('[data-role="content"]');
  const counter = root.querySelector('[data-role="count"]');

  async function refresh() {
    try {
      // Solo runs (one row per /api/start on this master) AND cluster
      // runs (one row per /api/cluster/start, archived by ArchiveOnStop)
      // are fetched in parallel and merged into a single timeline so the
      // operator sees a unified history. Cluster runs gain a "cluster"
      // badge + an expand button that lazy-loads per-worker breakdowns.
      const [soloRes, clusterRes] = await Promise.all([
        apiFetch('/api/runs').catch(() => null),
        apiFetch('/api/cluster/runs').catch(() => null),
      ]);
      const solo = soloRes && soloRes.ok ? ((await soloRes.json()).runs || []).filter((r) => r.total_files > 0) : [];
      const cluster = clusterRes && clusterRes.ok ? ((await clusterRes.json()).runs || []) : [];
      const merged = mergeRuns(solo, cluster);
      if (counter) counter.textContent = merged.length === 0
        ? 'no completed runs yet'
        : (merged.length === 1 ? '1 completed run' : `${merged.length} completed runs`);
      if (merged.length === 0) {
        slot.innerHTML = `
          <div class="runs-history-empty">
            <div class="hero-empty-icon" aria-hidden="true">
              <svg viewBox="0 0 64 64" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
                <rect x="10" y="14" width="44" height="36" rx="2"/>
                <path d="M10 22h44"/>
              </svg>
            </div>
            <div class="body-secondary">Finished runs will be listed here. Cluster runs are archived automatically when you press Stop.</div>
          </div>`;
        return;
      }
      // v0.15.0 — render the comparison banner + cards. Banner is
      // sticky at the top of the panel; shows a "pick another run"
      // hint when 1 is selected, full delta when 2 are selected.
      const top10 = merged.slice(0, 10);
      slot.innerHTML = comparisonBanner(top10) + top10.map(rowMarkup).join('');
      slot.querySelectorAll('[data-action="cmp-toggle"]').forEach((cb) => {
        cb.addEventListener('change', (ev) => {
          cmpToggle(ev.target.dataset.runId);
          // Re-render so the banner reflects the new selection.
          refresh();
        });
      });
      const clearBtn = slot.querySelector('[data-action="cmp-clear"]');
      if (clearBtn) clearBtn.addEventListener('click', () => { cmpClear(); refresh(); });
      slot.querySelectorAll('[data-action="view"]').forEach((btn) => {
        btn.addEventListener('click', (ev) => {
          ev.preventDefault();
          const proxy = document.createElement('button');
          proxy.dataset.view = btn.dataset.runId;
          document.body.appendChild(proxy);
          proxy.click();
          proxy.remove();
          const tbl = document.querySelector('[data-component="records"]');
          if (tbl) tbl.scrollIntoView({ behavior: 'smooth', block: 'start' });
        });
      });
      slot.querySelectorAll('[data-action="toggle-cluster"]').forEach((btn) => {
        btn.addEventListener('click', (ev) => {
          ev.preventDefault();
          const card = btn.closest('.runs-history-card');
          const drawer = card?.querySelector('[data-role="cluster-drawer"]');
          if (!drawer) return;
          const open = drawer.dataset.open === 'true';
          drawer.dataset.open = open ? 'false' : 'true';
          btn.textContent = open ? 'Show workers' : 'Hide workers';
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

// mergeRuns sorts solo + cluster runs by started_at descending into one
// timeline. Each entry is tagged with a "kind" so rowMarkup can branch.
// Cluster runs are normalized into the same field-name shape solo runs
// use so most of rowMarkup keeps working without per-kind plumbing.
function mergeRuns(solo, cluster) {
  const out = [];
  for (const r of solo) {
    out.push({
      kind: 'solo',
      id: r.id,
      started_at: r.started_at,
      stopped_at: r.stopped_at,
      total_files: Number(r.total_files || 0),
      total_bytes: Number(r.total_bytes || 0),
      failed_files: Number(r.failed_files || 0),
      overall_mbps: Number(r.overall_mbps || 0),
      raw: r,
    });
  }
  for (const r of cluster) {
    out.push({
      kind: 'cluster',
      id: r.id,
      started_at: r.started_at,
      stopped_at: r.stopped_at,
      total_files: Number(r.total_files || 0),
      total_bytes: Number(r.total_bytes || 0),
      failed_files: Number(r.failed_files || 0),
      overall_mbps: Number(r.overall_mbps || 0),
      master_version: r.master_version,
      workers: r.workers || [],
      raw: r,
    });
  }
  out.sort((a, b) => {
    const ta = a.started_at ? new Date(a.started_at).getTime() : 0;
    const tb = b.started_at ? new Date(b.started_at).getTime() : 0;
    return tb - ta;
  });
  return out;
}

function clusterDrawerMarkup(r) {
  if (!r.workers || r.workers.length === 0) return '';
  const rows = r.workers.map((w, i) => {
    const idx = String(i + 1).padStart(2, '0');
    const csvHref = w.file_csv ? `/api/cluster/runs/file?id=${encodeURIComponent(r.id)}&name=${encodeURIComponent(w.file_csv)}` : '';
    const metaHref = w.file_meta ? `/api/cluster/runs/file?id=${encodeURIComponent(r.id)}&name=${encodeURIComponent(w.file_meta)}` : '';
    const reach = w.reachable
      ? `<span class="badge badge-success"><span class="dot"></span>reachable</span>`
      : `<span class="badge badge-warning"><span class="dot"></span>${escapeHTML(w.err || 'unreachable')}</span>`;
    const skewBadge = w.version_mismatch
      ? `<span class="badge badge-warning" title="Worker reports version ${escapeHTML(w.version || '?')} which differs from master."><span class="dot"></span>version skew</span>`
      : '';
    const fetchWarn = (w.csv_fetch_err || w.meta_fetch_err)
      ? `<span class="badge badge-warning" title="${escapeHTML(w.csv_fetch_err || w.meta_fetch_err)}"><span class="dot"></span>partial archive</span>`
      : '';
    return `
      <li class="runs-history-cluster-worker">
        <div class="runs-history-cluster-worker-head">
          <div class="mono">worker-${idx} <span class="muted">·</span> ${escapeHTML(w.url)}</div>
          <div class="runs-history-cluster-worker-badges">${reach}${skewBadge}${fetchWarn}</div>
        </div>
        <div class="body-small" style="color:var(--text-secondary)">
          ${Number(w.total_files || 0).toLocaleString()} files · ${formatBytes(w.total_bytes)} · ${formatRate(w.overall_mbps)} MB/s${Number(w.failed_files || 0) > 0 ? ` · <span style="color:var(--danger-fg-soft)">${w.failed_files} failed</span>` : ''}
        </div>
        <div class="runs-history-cluster-worker-actions">
          ${csvHref ? `<a class="btn btn-sm btn-ghost" href="${csvHref}" download data-external="1">CSV</a>` : ''}
          ${metaHref ? `<a class="btn btn-sm btn-ghost" href="${metaHref}" download data-external="1">meta JSON</a>` : ''}
        </div>
      </li>`;
  }).join('');
  return `
    <div class="runs-history-cluster-drawer" data-role="cluster-drawer" data-open="false">
      <ul class="runs-history-cluster-list">${rows}</ul>
    </div>`;
}

function rowMarkup(entry) {
  // entry came from mergeRuns and carries either kind=solo or kind=cluster.
  // Solo rows route to the rich raw-data path (skips, interrupted, latency,
  // analysis). Cluster rows render the aggregated headline + a drawer of
  // per-worker breakdowns.
  if (entry.kind === 'cluster') return clusterRowMarkup(entry);
  const r = entry.raw;
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
  // Workload-shape badges so the operator can see at a glance what
  // this run actually exercised — without these, a normal-only run
  // and a normal+large+download run looked identical in the card list.
  const shapeBadges = [];
  if (r.normal_enabled) shapeBadges.push(`<span class="badge badge-info" title="Steady-rate small files were enabled."><span class="dot"></span>normal</span>`);
  if (r.large_enabled) shapeBadges.push(`<span class="badge badge-info" title="Interval large files were enabled."><span class="dot"></span>large</span>`);
  if (r.download_enabled) {
    const mode = r.download_match_mode || 'trackid';
    shapeBadges.push(`<span class="badge badge-info" title="Download verification was enabled (${mode} match mode)."><span class="dot"></span>download · ${escapeHTML(mode)}</span>`);
  }
  const shapeLine = shapeBadges.length ? ' · ' + shapeBadges.join(' ') : '';
  const csvUrl = `/api/report.csv?run=${encodeURIComponent(r.id)}`;
  return `
    <article class="runs-history-card">
      <header class="runs-history-card-head">
        <div class="runs-history-id">
          <div class="mono">${escapeHTML(r.id)}</div>
          <div class="body-small" style="color:var(--text-tertiary)">${formatStarted(r.started_at)}${r.stopped_at ? ' · ' + formatDuration(r.started_at, r.stopped_at) : ''}${shapeLine}${throttledBadge ? ' · ' + throttledBadge : ''}${interruptedBadge ? ' · ' + interruptedBadge : ''}</div>
        </div>
        <div class="runs-history-actions">
          <label class="check-inline" title="Pick two runs to compare." style="font-size:var(--fs-12)">
            <input type="checkbox" data-action="cmp-toggle" data-run-id="${escapeAttr(r.id)}" ${cmpState.includes(r.id) ? 'checked' : ''}>
            <span>Compare</span>
          </label>
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
               <div class="body-small">${r.download_parallel_streams || '?'} streams${r.download_match_mode === 'filename' ? ' · filename match' : ''}</div>`
            : `<div class="metric-default" style="color:var(--text-tertiary)">—</div>
               <div class="body-small">disabled</div>`}
        </div>
      </div>
      ${latencyMarkup(r)}
      ${analysisMarkup(r)}
    </article>`;
}

// clusterRowMarkup renders one cluster run. The headline mirrors the
// solo card's stats grid (success rate, files, throughput) but pulls
// from cluster-aggregated counters; below it sits an expand button
// that toggles a drawer of per-worker rows with download links.
function clusterRowMarkup(entry) {
  const total = Number(entry.total_files || 0);
  const failed = Number(entry.failed_files || 0);
  const succeeded = total - failed;
  const successPct = total > 0 ? (succeeded / total) * 100 : 0;
  const successCls = successPct >= 99 ? 'ok' : successPct >= 85 ? 'warn' : 'bad';
  const workers = entry.workers || [];
  const reachable = workers.filter((w) => w.reachable).length;
  const totalW = workers.length;
  const skewCount = workers.filter((w) => w.version_mismatch).length;
  const masterMetaHref = `/api/cluster/runs/file?id=${encodeURIComponent(entry.id)}&name=meta.json`;
  return `
    <article class="runs-history-card runs-history-card--cluster">
      <header class="runs-history-card-head">
        <div class="runs-history-id">
          <div class="mono">
            ${escapeHTML(entry.id)}
            <span class="badge badge-info" title="Cluster run — fan-out across ${totalW} worker${totalW === 1 ? '' : 's'} archived by the master."><span class="dot"></span>cluster · ${totalW} worker${totalW === 1 ? '' : 's'}</span>
            ${skewCount > 0 ? `<span class="badge badge-warning" title="${skewCount} worker(s) reported a different platform version than the master."><span class="dot"></span>${skewCount} version-skew</span>` : ''}
          </div>
          <div class="body-small" style="color:var(--text-tertiary)">${formatStarted(entry.started_at)}${entry.stopped_at ? ' · ' + formatDuration(entry.started_at, entry.stopped_at) : ''}${entry.master_version ? ' · master ' + escapeHTML(entry.master_version) : ''}</div>
        </div>
        <div class="runs-history-actions">
          ${entry.merged_csv ? `<a class="btn btn-sm btn-primary" href="/api/cluster/runs/file?id=${encodeURIComponent(entry.id)}&name=merged.csv" download data-external="1" title="Single CSV with every worker's rows interleaved chronologically. First column is the worker label so you can tell which node ran each upload/download.">Download merged CSV (${Number(entry.merged_rows || 0).toLocaleString()} rows)</a>` : ''}
          <a class="btn btn-sm btn-ghost" href="${masterMetaHref}" download data-external="1">aggregated JSON</a>
          <button class="btn btn-sm btn-secondary" type="button" data-action="toggle-cluster">Show workers</button>
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
          <div class="body-small">${formatBytes(entry.total_bytes)} · ${formatRate(entry.overall_mbps)} Mbps cluster-wide</div>
        </div>
        <div class="runs-history-stat">
          <div class="eyebrow">Reachable workers</div>
          <div class="metric-default tabular">${reachable}<span class="runs-history-pct"> / ${totalW}</span></div>
          <div class="body-small">${totalW - reachable === 0 ? 'every worker archived cleanly' : `${totalW - reachable} unreachable at archive time`}</div>
        </div>
        <div class="runs-history-stat">
          <div class="eyebrow">Origin</div>
          <div class="metric-default" style="color:var(--text-secondary)">cluster fan-out</div>
          <div class="body-small">expand below for per-worker CSV + meta</div>
        </div>
      </div>
      ${clusterDrawerMarkup(entry)}
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

// v0.15.0 — comparison banner. Renders nothing when no runs selected,
// a single-row hint when 1 is selected, and a delta row when 2 are
// selected. Computes deltas client-side from the run summaries already
// in the merged list — no extra API calls.
function comparisonBanner(merged) {
  if (cmpState.length === 0) return '';
  // Resolve selected IDs back into run records. Filter to runs the
  // current page has data for; ignore stale ids.
  const idIndex = new Map();
  merged.forEach((entry) => {
    const r = entry.kind === 'cluster' ? entry.cluster : entry.raw;
    if (r && r.id) idIndex.set(r.id, r);
  });
  const picks = cmpState.map((id) => idIndex.get(id)).filter(Boolean);
  if (picks.length === 1) {
    return `<div class="runs-cmp-banner runs-cmp-banner-1">
      <span><b>Pick another run to compare</b> — selected: <code>${escapeHTML(picks[0].id)}</code></span>
      <button type="button" class="btn btn-sm btn-ghost" data-action="cmp-clear">Clear</button>
    </div>`;
  }
  const [a, b] = picks;
  const deltas = [
    cmpDelta('Files',         Number(a.total_files || 0),  Number(b.total_files || 0)),
    cmpDelta('Throughput',    Number(a.overall_mbps || 0), Number(b.overall_mbps || 0), 'Mbps'),
    cmpDelta('Failed',        Number(a.failed_files || 0), Number(b.failed_files || 0), '', /*lowerIsBetter*/ true),
    cmpDelta('Skipped',       Number(a.dispatch_skips || 0), Number(b.dispatch_skips || 0), '', true),
    cmpDelta('p99 latency',   pickP99(a),                  pickP99(b),                   'ms', true),
  ];
  return `<div class="runs-cmp-banner runs-cmp-banner-2">
    <div class="runs-cmp-head">
      <span><b>Comparing</b></span>
      <code>${escapeHTML(a.id)}</code>
      <span class="runs-cmp-arrow">→</span>
      <code>${escapeHTML(b.id)}</code>
      <button type="button" class="btn btn-sm btn-ghost" data-action="cmp-clear">Clear</button>
    </div>
    <div class="runs-cmp-deltas">${deltas.join('')}</div>
  </div>`;
}

function pickP99(r) {
  // Solo runs persist latency_p99_ms in the summary; cluster runs put
  // it under aggregate. Either way return ms or 0.
  return Number(r.latency_p99_ms || (r.aggregate && r.aggregate.latency_p99_ms) || 0);
}

function cmpDelta(label, a, b, unit = '', lowerIsBetter = false) {
  const delta = b - a;
  const pct = a > 0 ? (delta / a) * 100 : 0;
  const better = lowerIsBetter ? delta < 0 : delta > 0;
  const cls = delta === 0 ? 'eq' : (better ? 'good' : 'bad');
  const arrow = delta === 0 ? '=' : (delta > 0 ? '↑' : '↓');
  return `<div class="runs-cmp-delta runs-cmp-delta-${cls}">
    <div class="eyebrow">${escapeHTML(label)}</div>
    <div class="metric-default tabular">${a.toFixed(2)}${unit ? ' ' + unit : ''} <span class="runs-cmp-arrow">→</span> ${b.toFixed(2)}${unit ? ' ' + unit : ''}</div>
    <div class="body-small">${arrow} ${Math.abs(delta).toFixed(2)}${unit ? ' ' + unit : ''} (${pct >= 0 ? '+' : ''}${pct.toFixed(1)}%)</div>
  </div>`;
}
