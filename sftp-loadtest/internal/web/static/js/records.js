// records.js — live activity table (replaces the 14-column legacy table).
//
// Renders 6 high-signal columns by default: user, kind, file, size, throughput,
// status. Each row expands inline to reveal the timing detail (upload window,
// track-id, processing time, download window) on click — no horizontal scroll
// needed to find the column you want.
//
// Polls /api/status every 2 s while a run is active; falls back to 5 s when
// idle so we don't hammer the server pre-run.

import { apiFetch } from './api.js';

const POLL_ACTIVE_MS = 2000;
const POLL_IDLE_MS   = 5000;
const MAX_ROWS = 50;

export function mountRecords(rootSelector) {
  const root = document.querySelector(rootSelector);
  if (!root) return;
  const tbody = root.querySelector('[data-role="rows"]');
  const empty = root.querySelector('[data-role="empty"]');
  const counter = root.querySelector('[data-role="count"]');
  const liveDot = root.querySelector('[data-role="live-dot"]');

  // Track expanded row IDs across re-renders so user-expanded rows persist.
  const expanded = new Set();

  // Click delegation — toggle expansion when the row's chevron is clicked.
  root.addEventListener('click', (ev) => {
    const trigger = ev.target.closest('[data-action="toggle-expand"]');
    if (!trigger) return;
    const id = trigger.dataset.id;
    if (!id) return;
    if (expanded.has(id)) expanded.delete(id);
    else expanded.add(id);
    // Re-render only this row so we don't lose scroll position.
    renderRow(tbody, getRecordById(id), expanded.has(id), true);
  });

  let cache = []; // last rendered records, used by the click delegate to find a record by id
  function getRecordById(id) { return cache.find((r) => recordID(r) === id) || null; }

  async function refresh() {
    let active = false;
    try {
      const res = await apiFetch('/api/status');
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const j = await res.json();
      active = !!j.active;
      const recs = (j.records || []).slice().reverse().slice(0, MAX_ROWS);
      cache = recs;

      // Re-render the table only when records changed (cheap content hash).
      const hash = recs.map(r => `${recordID(r)}|${r.EndTime||''}|${r.DownloadEndTime||''}|${r.Error||''}`).join(',');
      if (root.dataset.hash !== hash) {
        root.dataset.hash = hash;
        tbody.innerHTML = '';
        for (const r of recs) renderRow(tbody, r, expanded.has(recordID(r)), false);
      }
      if (counter) counter.textContent = recs.length === 0 ? 'No activity yet' : (recs.length === 1 ? '1 file' : `${recs.length} files`);
      if (empty) empty.hidden = recs.length > 0;
      if (liveDot) liveDot.dataset.state = active ? 'running' : 'idle';
    } catch (e) {
      if (counter) counter.textContent = 'Disconnected';
    } finally {
      setTimeout(refresh, active ? POLL_ACTIVE_MS : POLL_IDLE_MS);
    }
  }
  refresh();
}

function recordID(r) {
  return `${r.User || ''}::${r.Filename || ''}::${r.StartTime || ''}`;
}

function renderRow(tbody, r, isExpanded, replaceExisting) {
  if (!r) return;
  const id = recordID(r);
  const html = rowMarkup(r, id, isExpanded);
  if (replaceExisting) {
    const existing = tbody.querySelector(`[data-row-id="${escapeAttr(id)}"]`);
    if (existing) {
      existing.outerHTML = html;
      return;
    }
  }
  tbody.insertAdjacentHTML('beforeend', html);
}

function rowMarkup(r, id, isExpanded) {
  const status = computeStatus(r);
  const sizeStr = formatBytes(r.SizeBytes || 0);
  const upMbps = effSpeed(r.SizeBytes, r.StartTime, r.EndTime, r.SpeedMBps);
  const dlMbps = effSpeed(r.DownloadSizeBytes, r.DownloadStartTime, r.DownloadEndTime, r.DownloadSpeedMBps);
  const fileShort = r.Filename || '';
  const kindBadge = r.Kind === 'large'
    ? `<span class="rec-kind-badge rec-kind-large" title="Large file">L</span>`
    : `<span class="rec-kind-badge rec-kind-normal" title="Normal file">N</span>`;

  const main = `
    <tr data-row-id="${escapeAttr(id)}" data-status="${status.code}">
      <td class="rec-cell rec-user" title="${escapeAttr(r.User || '')}">${escapeHTML(r.User || '')}</td>
      <td class="rec-cell rec-kind">${kindBadge}</td>
      <td class="rec-cell rec-file" title="${escapeAttr(fileShort)}">${escapeHTML(fileShort)}</td>
      <td class="rec-cell rec-num">${sizeStr}</td>
      <td class="rec-cell rec-num">${upMbps != null ? upMbps.toFixed(2) : '—'}</td>
      <td class="rec-cell rec-status">
        <span class="rec-status-pill rec-status-${status.code}" title="${escapeAttr(status.title)}">
          <span class="rec-status-dot" aria-hidden="true"></span>${status.label}
        </span>
        <button class="rec-expand-btn" type="button" data-action="toggle-expand" data-id="${escapeAttr(id)}" aria-expanded="${isExpanded ? 'true' : 'false'}" aria-label="Show timing detail">
          <span class="icon icon-xs" aria-hidden="true">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" style="transform: rotate(${isExpanded ? '90' : '0'}deg); transition: transform var(--t-default) var(--ease-out);"><path d="M9 6l6 6-6 6"/></svg>
          </span>
        </button>
      </td>
    </tr>`;

  if (!isExpanded) return main;

  const detail = `
    <tr class="rec-detail-row" data-row-id="${escapeAttr(id + '__detail')}">
      <td colspan="6" class="rec-detail-cell">
        <div class="rec-detail-grid">
          <dl class="rec-detail-section">
            <dt>Upload window</dt>
            <dd><span class="mono">${formatTime(r.StartTime)}</span> &rarr; <span class="mono">${formatTime(r.EndTime) || '—'}</span></dd>
            <dt>Throughput (up)</dt>
            <dd>${upMbps != null ? upMbps.toFixed(2) + ' Mbps' : '—'}</dd>
          </dl>
          <dl class="rec-detail-section">
            <dt>Track ID</dt>
            <dd class="mono">${escapeHTML(r.TrackID || '')}${r.TrackID ? '' : '<span class="body-small">(pending)</span>'}</dd>
            <dt>Detected at</dt>
            <dd><span class="mono">${formatTime(r.TrackIDDetectedAt) || '—'}</span></dd>
            <dt>Processing time</dt>
            <dd>${formatProcMin(r.TrackIDWait)}</dd>
          </dl>
          <dl class="rec-detail-section">
            <dt>Download user</dt>
            <dd>${escapeHTML(r.DownloadUser || '—')}</dd>
            <dt>Download window</dt>
            <dd><span class="mono">${formatTime(r.DownloadStartTime) || '—'}</span> &rarr; <span class="mono">${formatTime(r.DownloadEndTime) || '—'}</span></dd>
            <dt>Throughput (dl)</dt>
            <dd>${dlMbps != null ? dlMbps.toFixed(2) + ' Mbps' : '—'}</dd>
          </dl>
          ${r.Error || r.DownloadError ? `<dl class="rec-detail-section rec-detail-error">${r.Error ? `<dt>Upload error</dt><dd>${escapeHTML(r.ErrorCode || 'ERR')}: ${escapeHTML(r.Error)}</dd>` : ''}${r.DownloadError ? `<dt>Download error</dt><dd>${escapeHTML(r.DownloadError)}</dd>` : ''}</dl>` : ''}
        </div>
      </td>
    </tr>`;

  return main + detail;
}

// ---------- helpers ----------
function computeStatus(r) {
  if (r.Error) return { code: 'error',  label: 'Failed',     title: r.ErrorCode || 'error' };
  if (r.DownloadError) return { code: 'warn',  label: 'DL fail',     title: 'Download error' };
  if (r.DownloadEndTime) return { code: 'ok',    label: 'Complete',    title: 'Upload + download succeeded' };
  if (r.TrackID) return { code: 'partial', label: 'Track-ID',    title: 'Awaiting download' };
  if (r.EndTime) return { code: 'pending', label: 'Awaiting ID', title: 'Awaiting #trackid rename' };
  return { code: 'pending', label: 'Uploading',  title: 'Upload in progress' };
}
function effSpeed(bytes, start, end, recorded) {
  if (recorded != null && recorded > 0) return recorded;
  if (!bytes || !start || !end) return null;
  const ms = new Date(end).getTime() - new Date(start).getTime();
  if (ms <= 0) return null;
  return (bytes * 8) / (ms * 1000); // Mbps
}
function formatBytes(b) {
  if (!b) return '0 B';
  const u = ['B', 'KB', 'MB', 'GB'];
  let i = 0; let v = Number(b);
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
  return v >= 100 ? `${v.toFixed(0)} ${u[i]}` : v >= 10 ? `${v.toFixed(1)} ${u[i]}` : `${v.toFixed(2)} ${u[i]}`;
}
function formatTime(iso) {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleTimeString('en-US', { hour12: false });
}
function formatProcMin(ns) {
  if (!ns || ns <= 0) return '—';
  return `${(ns / 60e9).toFixed(2)} min`;
}
function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}
function escapeAttr(s) {
  return String(s).replace(/"/g, '&quot;');
}
