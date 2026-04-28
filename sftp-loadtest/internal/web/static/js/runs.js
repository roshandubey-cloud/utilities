// runs.js — hero surface. The hero is the ACTION (start a load test); the
// most recent run is shown as a subtle status strip beneath, not as a
// 4-tile metric grid that dominates the page.

import { apiFetch } from './api.js';

const REFRESH_MS = 4000;

export function mountHeroRun(rootSelector) {
  const root = document.querySelector(rootSelector);
  if (!root) return;
  const slot = root.querySelector('[data-role="hero-content"]');
  let timer;
  async function refresh() {
    try {
      const res = await apiFetch('/api/runs');
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const j = await res.json();
      const runs = (j.runs || []).filter((r) => r.total_files > 0);
      slot.innerHTML = render(runs[0] || null);
      wireCTAs(slot);
    } catch (e) {
      slot.innerHTML = render(null, e.message || String(e));
      wireCTAs(slot);
    } finally {
      timer = setTimeout(refresh, REFRESH_MS);
    }
  }
  refresh();
}

function render(latest, errorMsg) {
  const action = `
    <div class="hero-action">
      <div class="hero-action-text">
        <div class="hero-title">Run a load test</div>
        <div class="hero-subtitle">Generate uploads at a controlled rate, track processing time per file, capture downloads, stream a CSV report.</div>
      </div>
      <div class="hero-action-cta">
        <button class="btn btn-secondary" type="button" data-role="export-cta" title="Save the current configuration as JSON">Export config</button>
        <button class="btn btn-primary btn-lg" type="button" data-role="start-cta">Start a new load test</button>
      </div>
    </div>`;

  let strip = '';
  if (latest) {
    const csvUrl = `/api/report.csv?run=${encodeURIComponent(latest.id)}`;
    const dur = formatDuration(latest.started_at, latest.stopped_at, latest.active);
    const stats = [
      `${formatInt(latest.total_files)} files`,
      formatBytes(latest.total_bytes),
      `${formatRate(latest.overall_mbps)} Mbps`,
      dur,
    ].filter(Boolean).join(' · ');
    strip = `
      <div class="hero-last-run" data-active="${latest.active ? 'true' : 'false'}">
        <span class="hero-last-run-label">${latest.active ? 'Run in progress' : 'Last run'}</span>
        <span class="hero-last-run-id mono">${escapeHTML(latest.id)}</span>
        <span class="hero-last-run-meta">${escapeHTML(stats)}</span>
        <span class="hero-last-run-time" title="${escapeHTML(latest.started_at || '')}">${formatStarted(latest.started_at)}</span>
        <a class="hero-last-run-csv" href="${csvUrl}" download data-external="1">Download CSV</a>
      </div>`;
  } else if (errorMsg) {
    strip = `<div class="hero-last-run hero-last-run-error">Couldn't reach <code>/api/runs</code>: ${escapeHTML(errorMsg)}</div>`;
  } else {
    strip = `<div class="hero-last-run hero-last-run-empty">No runs yet — your run history will appear here.</div>`;
  }

  return action + strip;
}

function wireExportCTA(slot) {
  const btn = slot.querySelector('[data-role="export-cta"]');
  if (!btn) return;
  btn.addEventListener('click', (ev) => {
    ev.preventDefault();
    // Drive the legacy Export Config button so the existing serialization
    // (with passwords-stripped-by-default + opt-in include) keeps applying.
    const legacy = document.getElementById('exportBtn');
    if (legacy) legacy.click();
    else if (typeof window.exportConfig === 'function') window.exportConfig();
  });
}

function wireCTAs(slot) {
  const cta = slot.querySelector('[data-role="start-cta"]');
  if (cta) {
    cta.addEventListener('click', (ev) => {
      ev.preventDefault();
      const target = document.querySelector('#legacy-shell');
      if (target) target.scrollIntoView({ behavior: 'smooth', block: 'start' });
    });
  }
  wireExportCTA(slot);
}

// ---------- formatters ----------
function formatInt(n) {
  if (n == null) return '—';
  return Number(n).toLocaleString();
}
function formatBytes(n) {
  if (!n) return '0 B';
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
  const now = new Date();
  const diffMs = now - d;
  if (diffMs < 60_000) return 'just now';
  if (diffMs < 3_600_000) return `${Math.floor(diffMs / 60_000)} min ago`;
  if (diffMs < 86_400_000) return `${Math.floor(diffMs / 3_600_000)} h ago`;
  return d.toLocaleString();
}
function formatDuration(startISO, stopISO, active) {
  if (!startISO) return '';
  const start = new Date(startISO).getTime();
  const stop = stopISO ? new Date(stopISO).getTime() : (active ? Date.now() : NaN);
  if (!Number.isFinite(start) || !Number.isFinite(stop)) return '';
  const sec = Math.max(0, Math.floor((stop - start) / 1000));
  if (sec < 60) return `${sec}s`;
  const m = Math.floor(sec / 60);
  const s = sec % 60;
  if (m < 60) return `${m}m ${String(s).padStart(2, '0')}s`;
  const h = Math.floor(m / 60);
  const mm = m % 60;
  return `${h}h ${String(mm).padStart(2, '0')}m`;
}
function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}
