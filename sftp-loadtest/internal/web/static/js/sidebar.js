// sidebar.js — α5 of the v0.9.0 redesign.
//
// Fills the four sidebar sections built by shell.js with live data:
//
//   Connections    ← localStorage 'sftp-loadtest-conn-history-v1'
//                    (host:port pairs the Test Connection panel remembers).
//                    Click a row → fills #conn-host / #conn-port and scrolls
//                    Quick Checks into view.
//
//   Saved configs  ← localStorage via saved-configs.js list/load.
//                    Click a row → loadConfig(id), shows a toast on success.
//                    Right-click (or hover-shown × button) deletes.
//
//   Recent runs    ← /api/runs polled at 3 Hz cadence (matches runs-history).
//                    Each row shows id + status icon + relative-time.
//                    Click → scrolls the runs-history card for that run into
//                    view (β1 will replace this with a dedicated detail pane).
//
//   Trusted hosts  ← /api/hostkeys; row hover reveals a Forget button.
//
// All four refresh on a heartbeat so external mutations (probe accepts,
// host trust, palette-driven preset save) appear without page reload.

import { apiFetch } from './api.js';
import { list as listConfigs, load as loadConfig, remove as removeConfig } from './saved-configs.js';
import { pushToast } from './toast.js';
import { listSaved, applyEntry, promptDelete, SAVED_KEY } from './saved-connections.js';

const CONN_HISTORY_KEY = 'sftp-loadtest-conn-history-v1';
const REFRESH_MS = 3000;

export function mountSidebar() {
  const sidebar = document.querySelector('.shell-sidebar');
  if (!sidebar) return;

  const slots = {
    connections: sidebar.querySelector('[data-role="sidebar-connections"]'),
    configs:     sidebar.querySelector('[data-role="sidebar-configs"]'),
    runs:        sidebar.querySelector('[data-role="sidebar-runs"]'),
    trust:       sidebar.querySelector('[data-role="sidebar-trust"]'),
  };

  async function refresh() {
    try {
      renderConnections(slots.connections);
      renderConfigs(slots.configs);
      await Promise.all([renderRuns(slots.runs), renderTrust(slots.trust)]);
    } finally {
      setTimeout(refresh, REFRESH_MS);
    }
  }
  refresh();

  // Listen for storage events so OTHER tabs / windows that change the
  // saved configs / connection history reflect here without waiting for
  // the next heartbeat.
  window.addEventListener('storage', (ev) => {
    if (!ev.key) return;
    if (ev.key === CONN_HISTORY_KEY || ev.key === SAVED_KEY) renderConnections(slots.connections);
    if (ev.key.startsWith('sftp-loadtest-saved-configs')) renderConfigs(slots.configs);
  });
}

// ---------- Connections ----------
// Two layers of state in this slot:
//   1. Saved entries (user-curated, named, with optional creds): clicking
//      one fills host/port/username/password.
//   2. Recent entries (auto-tracked from successful probes, host:port
//      only): shown as a dimmed sub-list when nothing's been saved or
//      after the saved list, so the user always has both surfaces.
function renderConnections(slot) {
  if (!slot) return;
  const saved = listSaved();
  const recent = readConnHistory();
  if (saved.length === 0 && recent.length === 0) {
    slot.innerHTML = '<div class="shell-sidebar-empty">No connections yet. Hit Save… on the Test connection card.</div>';
    return;
  }
  const savedHTML = saved.map((entry) => `
    <div class="shell-sidebar-row shell-sidebar-row-saved"
         data-action="saved-conn" data-id="${escapeAttr(entry.id)}"
         title="${escapeAttr(entry.host)}:${entry.port}${entry.username ? ' as '+entry.username : ''}${entry.has_password ? ' (with password)' : ''}">
      <span class="row-icon" aria-hidden="true">
        <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor"
             stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"><path d="M2 6l3-3 9 9-3 3z"/><path d="M11 6l-2 2"/></svg>
      </span>
      <span class="row-label">${escapeHTML(entry.name)}</span>
      <span class="row-meta">${escapeHTML(entry.host)}:${entry.port}</span>
      <button type="button" class="shell-sidebar-row-x" data-role="forget"
              aria-label="Forget ${escapeAttr(entry.name)}" title="Forget">×</button>
    </div>`).join('');
  // Recent rows that aren't already a saved entry's host:port.
  const savedKeys = new Set(saved.map((e) => `${e.host}:${e.port}`));
  const recentRows = recent.filter((e) => !savedKeys.has(`${e.host}:${e.port}`));
  const recentHTML = recentRows.map((entry) => `
    <div class="shell-sidebar-row shell-sidebar-row-recent" data-action="conn"
         data-host="${escapeAttr(entry.host)}" data-port="${entry.port}">
      <span class="row-icon" aria-hidden="true">
        <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor"
             stroke-width="1.4" stroke-linecap="round"><circle cx="8" cy="8" r="6"/><path d="M2 8h12M8 2c2 2 2 10 0 12M8 2c-2 2-2 10 0 12"/></svg>
      </span>
      <span class="row-label" title="${escapeAttr(entry.host)}:${entry.port}">${escapeHTML(entry.host)}</span>
      <span class="row-meta">${entry.port}</span>
    </div>`).join('');
  const divider = (savedHTML && recentHTML)
    ? '<div class="shell-sidebar-divider">recent</div>'
    : '';
  slot.innerHTML = savedHTML + divider + recentHTML;

  slot.querySelectorAll('[data-action="saved-conn"]').forEach((row) => {
    const entry = saved.find((e) => e.id === row.dataset.id);
    row.addEventListener('click', (ev) => {
      // Don't fire row click when the user hits the inline forget button.
      if (ev.target.closest('[data-role="forget"]')) return;
      applyEntry(entry);
      document.querySelector('[data-component="connection"]')
        ?.scrollIntoView({ behavior: 'smooth', block: 'start' });
    });
    const forget = row.querySelector('[data-role="forget"]');
    forget?.addEventListener('click', async (ev) => {
      ev.preventDefault();
      ev.stopPropagation();
      await promptDelete(entry);
    });
  });
  slot.querySelectorAll('[data-action="conn"]').forEach((row) => {
    row.addEventListener('click', () => {
      const hostInput = document.getElementById('conn-host');
      const portInput = document.getElementById('conn-port');
      if (hostInput) {
        hostInput.value = row.dataset.host;
        hostInput.dispatchEvent(new Event('input', { bubbles: true }));
      }
      if (portInput) {
        portInput.value = row.dataset.port;
        portInput.dispatchEvent(new Event('input', { bubbles: true }));
      }
      document.querySelector('[data-component="connection"]')
        ?.scrollIntoView({ behavior: 'smooth', block: 'start' });
    });
  });
}

function readConnHistory() {
  try {
    return JSON.parse(localStorage.getItem(CONN_HISTORY_KEY) || '[]');
  } catch { return []; }
}

// ---------- Saved configs ----------
function renderConfigs(slot) {
  if (!slot) return;
  const list = listConfigs();
  if (list.length === 0) {
    slot.innerHTML = '<div class="shell-sidebar-empty">No presets yet. Save the current form via ⌘K → “Save current config…”.</div>';
    return;
  }
  slot.innerHTML = list.map((cfg) => `
    <div class="shell-sidebar-row sidebar-row-with-action" data-action="cfg" data-id="${escapeAttr(cfg.id)}">
      <span class="row-icon" aria-hidden="true">
        <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor"
             stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round">
          <path d="M3 2h7l3 3v9H3z"/><path d="M10 2v3h3"/></svg>
      </span>
      <span class="row-label" title="${escapeAttr(cfg.name)}">${escapeHTML(cfg.name)}</span>
      <button class="row-action-btn" type="button" data-action="cfg-delete"
              data-id="${escapeAttr(cfg.id)}" title="Delete preset">×</button>
    </div>`).join('');
  slot.querySelectorAll('[data-action="cfg"]').forEach((row) => {
    row.addEventListener('click', (ev) => {
      // Skip when the click bubbled up from the delete button.
      if (ev.target.closest('[data-action="cfg-delete"]')) return;
      const id = row.dataset.id;
      if (loadConfig(id)) {
        const name = row.querySelector('.row-label')?.textContent || '';
        pushToast(`Loaded preset “${name}”`, 'info', { timeout: 3000 });
      }
    });
  });
  slot.querySelectorAll('[data-action="cfg-delete"]').forEach((btn) => {
    btn.addEventListener('click', (ev) => {
      ev.stopPropagation();
      const id = btn.dataset.id;
      const name = btn.closest('[data-action="cfg"]')?.querySelector('.row-label')?.textContent || '';
      if (confirm(`Delete preset “${name}”?`)) {
        removeConfig(id);
        renderConfigs(slot);
      }
    });
  });
}

// ---------- Recent runs ----------
async function renderRuns(slot) {
  if (!slot) return;
  let runs = [];
  try {
    const r = await apiFetch('/api/runs');
    if (r.ok) {
      const j = await r.json();
      runs = (j.runs || []).filter((x) => Number(x.total_files) > 0).slice(0, 10);
    }
  } catch { /* leave empty */ }
  if (runs.length === 0) {
    slot.innerHTML = '<div class="shell-sidebar-empty">Finished runs will appear here.</div>';
    return;
  }
  slot.innerHTML = runs.map((r) => {
    const failed = Number(r.failed_files || 0);
    const total = Number(r.total_files || 0);
    const ok = total > 0 && failed === 0;
    const status = r.interrupted ? 'interrupted' : (ok ? 'ok' : 'warn');
    const icon = status === 'ok'
      ? '<svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="var(--success-fg-soft)" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M3 8l3 3 7-7"/></svg>'
      : status === 'interrupted'
      ? '<svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="var(--warning-fg-soft)" stroke-width="1.6" stroke-linecap="round"><path d="M8 4v5"/><path d="M8 12h.01"/><circle cx="8" cy="8" r="6"/></svg>'
      : '<svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="var(--danger-fg-soft)" stroke-width="1.6" stroke-linecap="round"><path d="M5 5l6 6M11 5l-6 6"/></svg>';
    return `
      <div class="shell-sidebar-row" data-action="run" data-id="${escapeAttr(r.id)}">
        <span class="row-icon" aria-hidden="true">${icon}</span>
        <span class="row-label" title="${escapeAttr(r.id)}">${escapeHTML(r.id)}</span>
        <span class="row-meta">${formatRel(r.started_at)}</span>
      </div>`;
  }).join('');
  slot.querySelectorAll('[data-action="run"]').forEach((row) => {
    row.addEventListener('click', () => {
      const id = row.dataset.id;
      // Open the β1 detail pane instead of just swapping the records
      // table. The proxy with data-view-detail is intercepted by
      // run-detail.js's capture-phase listener.
      const proxy = document.createElement('button');
      proxy.setAttribute('data-view-detail', id);
      document.body.appendChild(proxy);
      proxy.click();
      proxy.remove();
    });
  });
}

// ---------- Trusted hosts ----------
async function renderTrust(slot) {
  if (!slot) return;
  let hosts = [];
  let mode = 'store';
  try {
    const r = await apiFetch('/api/hostkeys');
    if (r.ok) {
      const j = await r.json();
      hosts = j.hosts || [];
      mode = j.mode || 'store';
    }
  } catch { /* leave empty */ }
  if (mode === 'file') {
    slot.innerHTML = '<div class="shell-sidebar-empty">Managed externally (-known-hosts file).</div>';
    return;
  }
  if (hosts.length === 0) {
    slot.innerHTML = '<div class="shell-sidebar-empty">No trusted hosts yet.</div>';
    return;
  }
  slot.innerHTML = hosts.map((h) => `
    <div class="shell-sidebar-row sidebar-row-with-action" data-action="trust"
         data-host="${escapeAttr(h.host)}" data-port="${h.port}"
         title="${escapeAttr(h.fingerprint || '')}">
      <span class="row-icon" aria-hidden="true">
        <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor"
             stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round">
          <path d="M8 2l5 2v4c0 3-2 5-5 6-3-1-5-3-5-6V4z"/><path d="M6 8l1.5 1.5L10 7"/></svg>
      </span>
      <span class="row-label">${escapeHTML(h.host)}</span>
      <span class="row-meta">${h.port}</span>
      <button class="row-action-btn" type="button" data-action="trust-forget"
              data-host="${escapeAttr(h.host)}" data-port="${h.port}" title="Forget">×</button>
    </div>`).join('');
  slot.querySelectorAll('[data-action="trust-forget"]').forEach((btn) => {
    btn.addEventListener('click', async (ev) => {
      ev.stopPropagation();
      const host = btn.dataset.host;
      const port = Number(btn.dataset.port);
      if (!confirm(`Forget the trusted key for ${host}:${port}?`)) return;
      try {
        const r = await apiFetch('/api/hostkeys/remove', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ host, port }),
        });
        if (r.ok) {
          pushToast(`Forgot ${host}:${port}`, 'info', { timeout: 3000 });
          renderTrust(slot);
        } else {
          pushToast('Could not forget host', 'error');
        }
      } catch (e) {
        pushToast(`Network error: ${e.message || e}`, 'error');
      }
    });
  });
}

function formatRel(iso) {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  const diff = Date.now() - d.getTime();
  if (diff < 60_000) return 'now';
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)}m`;
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)}h`;
  return `${Math.floor(diff / 86_400_000)}d`;
}

function escapeHTML(s) { return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c])); }
function escapeAttr(s) { return String(s).replace(/"/g, '&quot;'); }
