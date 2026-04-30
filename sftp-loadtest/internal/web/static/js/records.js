// records.js — live activity table.
//
// Renders the FULL per-file column set inline (User, Kind, File, Up Start,
// Up End, Size, Up Mbps, TrackID, Proc Time, DL User, DL Start, DL End,
// DL Wait, DL Mbps, Status) so operators can scan the data they need without
// expanding rows. Row-expand is still available for the long-form error
// detail and any data that doesn't fit on one line.
//
// Polls /api/status every 2 s while a run is active; falls back to 5 s when
// idle so we don't hammer the server pre-run.

import { apiFetch } from './api.js';

const POLL_ACTIVE_MS = 2000;
const POLL_IDLE_MS   = 5000;
const MAX_ROWS = 200;

export function mountRecords(rootSelector) {
  const root = document.querySelector(rootSelector);
  if (!root) return;
  const tbody  = root.querySelector('[data-role="rows"]');
  const empty  = root.querySelector('[data-role="empty"]');
  const counter= root.querySelector('[data-role="count"]');
  const liveDot= root.querySelector('[data-role="live-dot"]');

  const expanded = new Set();

  // Pinned-run filter: when the user clicks "View" on a Previous-runs row,
  // the records table switches to that run's data via /api/status?run=<id>.
  // Click "Live" (legacy #liveBtn) or another View clears/replaces.
  let pinnedRunId = null;

  document.addEventListener('click', (ev) => {
    // Only react to the LEGACY view-record clicks, not the new shell's
    // [data-view] view-container attributes. Look specifically for
    // anchor/button elements that carry data-view="run-..." (the
    // existing runs-history "View records" hook).
    const view = ev.target.closest('[data-view]');
    if (view && view.dataset.view && /^run-/i.test(view.dataset.view)) {
      pinnedRunId = view.dataset.view;
      refresh(true);
      return;
    }
    if (ev.target.id === 'liveBtn' || ev.target.closest('#liveBtn')) {
      pinnedRunId = null;
      refresh(true);
    }
  }, true);

  root.addEventListener('click', (ev) => {
    const trigger = ev.target.closest('[data-action="toggle-expand"]');
    if (!trigger) return;
    const id = trigger.dataset.id;
    if (!id) return;
    if (expanded.has(id)) expanded.delete(id);
    else expanded.add(id);
    renderRow(tbody, getRecordById(id), expanded.has(id), true);
  });

  let cache = [];
  function getRecordById(id) { return cache.find((r) => recordID(r) === id) || null; }

  let pendingTimer = null;
  async function refresh(force) {
    if (pendingTimer) { clearTimeout(pendingTimer); pendingTimer = null; }
    let active = false;
    try {
      let recs;
      if (pinnedRunId) {
        // Historical run: /api/status only has its summary metrics; the
        // per-file records were drained to the on-disk CSV at seal time.
        // Parse that CSV to get the records back.
        const csvRes = await apiFetch(`/api/report.csv?run=${encodeURIComponent(pinnedRunId)}`);
        if (!csvRes.ok) throw new Error(`HTTP ${csvRes.status}`);
        const csvText = await csvRes.text();
        recs = parseCsvToRecords(csvText).slice(-MAX_ROWS).reverse();
        active = false;
      } else {
        const res = await apiFetch('/api/status');
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        const j = await res.json();
        active = !!j.active;
        recs = (j.records || []).slice().reverse().slice(0, MAX_ROWS);
      }
      cache = recs;

      const hash = (pinnedRunId || 'live') + '|' + recs.map(r => `${recordID(r)}|${r.EndTime||''}|${r.DownloadEndTime||''}|${r.Error||''}`).join(',');
      if (force || root.dataset.hash !== hash) {
        root.dataset.hash = hash;
        tbody.innerHTML = '';
        for (const r of recs) renderRow(tbody, r, expanded.has(recordID(r)), false);
      }
      if (counter) {
        const prefix = pinnedRunId ? `Viewing ${pinnedRunId} · ` : '';
        counter.textContent = recs.length === 0
          ? prefix + 'No activity yet'
          : prefix + (recs.length === 1 ? '1 file' : `${recs.length} files`);
      }
      if (empty) empty.hidden = recs.length > 0;
      if (liveDot) liveDot.dataset.state = active ? 'running' : 'idle';
    } catch (e) {
      if (counter) counter.textContent = `Couldn't load records (${e.message || e})`;
    } finally {
      // Pinned (historical) runs don't need fast polling — once a minute is plenty.
      const wait = pinnedRunId ? 60_000 : (active ? POLL_ACTIVE_MS : POLL_IDLE_MS);
      pendingTimer = setTimeout(() => refresh(false), wait);
    }
  }
  refresh(true);
}

// Parse the server's report.csv format back into record objects matching the
// shape /api/status returns for live records, so the same renderRow code
// works for both live and historical runs.
function parseCsvToRecords(text) {
  if (!text) return [];
  const lines = text.split(/\r?\n/).filter((l) => l.length > 0);
  if (lines.length === 0) return [];
  const header = lines[0].split(',');
  const idx = (name) => header.indexOf(name);
  const cu = idx('user'), ck = idx('kind'), cf = idx('filename');
  const cs = idx('start_time'), ce = idx('end_time');
  const csz = idx('size_bytes'), cum = idx('upload_mbps');
  const ct = idx('track_id'), ctd = idx('track_id_detected_at'), ctw = idx('track_id_wait_sec');
  const cer = idx('error'), cec = idx('error_code');
  const cdu = idx('download_user'), cds = idx('download_start'), cde = idx('download_end');
  const cdw = idx('download_wait_sec'), cdsz = idx('download_size_bytes');
  const cdm = idx('download_mbps'), cder = idx('download_error');

  const num = (s) => { const n = parseFloat(s); return isFinite(n) ? n : 0; };
  const result = [];
  for (let i = 1; i < lines.length; i++) {
    const cols = splitCsvLine(lines[i]);
    if (cols.length < header.length) continue;
    result.push({
      User:                cu  >= 0 ? cols[cu]  : '',
      Kind:                ck  >= 0 ? cols[ck]  : '',
      Filename:            cf  >= 0 ? cols[cf]  : '',
      StartTime:           cs  >= 0 ? cols[cs]  : '',
      EndTime:             ce  >= 0 ? cols[ce]  : '',
      SizeBytes:           csz >= 0 ? num(cols[csz]) : 0,
      SpeedMBps:           cum >= 0 ? num(cols[cum]) : 0,
      TrackID:             ct  >= 0 ? cols[ct]  : '',
      TrackIDDetectedAt:   ctd >= 0 ? cols[ctd] : '',
      TrackIDWait:         ctw >= 0 ? Math.round(num(cols[ctw]) * 60e9) : 0,
      Error:               cer >= 0 ? cols[cer] : '',
      ErrorCode:           cec >= 0 ? cols[cec] : '',
      DownloadUser:        cdu >= 0 ? cols[cdu] : '',
      DownloadStartTime:   cds >= 0 ? cols[cds] : '',
      DownloadEndTime:     cde >= 0 ? cols[cde] : '',
      DownloadWait:        cdw >= 0 ? Math.round(num(cols[cdw]) * 1e9) : 0,
      DownloadSizeBytes:   cdsz>= 0 ? num(cols[cdsz]) : 0,
      DownloadSpeedMBps:   cdm >= 0 ? num(cols[cdm]) : 0,
      DownloadError:       cder>= 0 ? cols[cder] : '',
    });
  }
  return result;
}

// Minimal CSV splitter that honours doubled-quote escaping. The server uses
// encoding/csv so quoted fields are present whenever a value contains
// comma/quote/newline.
function splitCsvLine(line) {
  const out = [];
  let cur = '';
  let q = false;
  for (let i = 0; i < line.length; i++) {
    const ch = line[i];
    if (q) {
      if (ch === '"') {
        if (line[i + 1] === '"') { cur += '"'; i++; }
        else { q = false; }
      } else cur += ch;
    } else {
      if (ch === ',') { out.push(cur); cur = ''; }
      else if (ch === '"') q = true;
      else cur += ch;
    }
  }
  out.push(cur);
  return out;
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
      const detailNext = existing.nextElementSibling;
      if (detailNext && detailNext.classList && detailNext.classList.contains('rec-detail-row')) {
        detailNext.remove();
      }
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
  const procMin = (r.TrackIDWait && r.TrackIDWait > 0) ? (r.TrackIDWait / 60e9).toFixed(2) : '';
  const dlWaitSec = r.DownloadWait ? (r.DownloadWait / 1e9).toFixed(2) : '';
  const fileShort = r.Filename || '';
  const kindBadge = r.Kind === 'large'
    ? `<span class="rec-kind-badge rec-kind-large" title="Large file">L</span>`
    : `<span class="rec-kind-badge rec-kind-normal" title="Normal file">N</span>`;
  const trackID = r.TrackID || '';
  const errTag = r.Error ? ` <span class="err-text" title="${escapeAttr(r.Error)}">${escapeHTML(r.ErrorCode || 'ERR')}</span>` : '';
  const dlErrTag = r.DownloadError ? ` <span class="err-text" title="${escapeAttr(r.DownloadError)}">dl</span>` : '';

  const main = `
    <tr data-row-id="${escapeAttr(id)}" data-status="${status.code}">
      <td class="rec-cell rec-user" title="${escapeAttr(r.User || '')}">${escapeHTML(r.User || '')}</td>
      <td class="rec-cell rec-kind">${kindBadge}</td>
      <td class="rec-cell rec-file" title="${escapeAttr(fileShort)}">${escapeHTML(fileShort)}</td>
      <td class="rec-cell rec-time">${formatTime(r.StartTime)}</td>
      <td class="rec-cell rec-time">${formatTime(r.EndTime)}</td>
      <td class="rec-cell rec-num">${sizeStr}</td>
      <td class="rec-cell rec-num">${upMbps != null ? upMbps.toFixed(2) : '—'}</td>
      <td class="rec-cell rec-mono" title="${escapeAttr(trackID)}">${escapeHTML(trackID)}${errTag}</td>
      <td class="rec-cell rec-num">${procMin || '—'}</td>
      <td class="rec-cell rec-user">${escapeHTML(r.DownloadUser || '')}</td>
      <td class="rec-cell rec-time">${formatTime(r.DownloadStartTime)}</td>
      <td class="rec-cell rec-time">${formatTime(r.DownloadEndTime)}</td>
      <td class="rec-cell rec-num">${dlWaitSec || '—'}</td>
      <td class="rec-cell rec-num">${dlMbps != null ? dlMbps.toFixed(2) : '—'}${dlErrTag}</td>
      <td class="rec-cell rec-status">
        <span class="rec-status-pill rec-status-${status.code}" title="${escapeAttr(status.title)}">
          <span class="rec-status-dot" aria-hidden="true"></span>${status.label}
        </span>
        <button class="rec-expand-btn" type="button" data-action="toggle-expand" data-id="${escapeAttr(id)}" aria-expanded="${isExpanded ? 'true' : 'false'}" aria-label="Show error / extra detail">
          <span class="icon icon-xs" aria-hidden="true">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" style="transform: rotate(${isExpanded ? '90' : '0'}deg); transition: transform var(--t-default) var(--ease-out);"><path d="M9 6l6 6-6 6"/></svg>
          </span>
        </button>
      </td>
    </tr>`;

  if (!isExpanded) return main;

  const errBlock = r.Error || r.DownloadError ? `
    <dl class="rec-detail-section rec-detail-error">
      ${r.Error ? `<dt>Upload error</dt><dd><span class="mono">${escapeHTML(r.ErrorCode || 'ERR')}</span> ${escapeHTML(r.Error)}</dd>` : ''}
      ${r.DownloadError ? `<dt>Download error</dt><dd>${escapeHTML(r.DownloadError)}</dd>` : ''}
    </dl>` : '';

  const detail = `
    <tr class="rec-detail-row" data-row-id="${escapeAttr(id + '__detail')}">
      <td colspan="15" class="rec-detail-cell">
        <div class="rec-detail-grid">
          <dl class="rec-detail-section">
            <dt>Filename (full)</dt>
            <dd class="mono">${escapeHTML(r.Filename || '—')}</dd>
            <dt>Track ID detected at</dt>
            <dd><span class="mono">${formatTime(r.TrackIDDetectedAt) || '—'}</span></dd>
            <dt>Bytes (raw)</dt>
            <dd><span class="mono">${(r.SizeBytes || 0).toLocaleString()}</span></dd>
          </dl>
          ${errBlock}
        </div>
      </td>
    </tr>`;

  return main + detail;
}

// ---------- helpers ----------
function computeStatus(r) {
  if (r.Error) return { code: 'error',   label: 'Failed',     title: r.ErrorCode || 'error' };
  if (r.DownloadError) return { code: 'warn', label: 'DL fail', title: 'Download error' };
  if (r.DownloadEndTime) return { code: 'ok', label: 'Complete', title: 'Upload + download succeeded' };
  if (r.TrackID) return { code: 'partial', label: 'Track-ID', title: 'Awaiting download' };
  if (r.EndTime) return { code: 'pending', label: 'Awaiting ID', title: 'Awaiting #trackid rename' };
  return { code: 'pending', label: 'Uploading', title: 'Upload in progress' };
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
function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}
function escapeAttr(s) {
  return String(s).replace(/"/g, '&quot;');
}
