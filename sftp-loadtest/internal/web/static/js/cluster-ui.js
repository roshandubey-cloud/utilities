// cluster-ui.js — β2 of the v0.9.0 redesign.
//
// Surfaces the cluster fan-out backend (shipped in v0.8.0) in the UI:
//
//   - Sidebar "Workers" section: list of saved worker URLs + auth.
//     Right-click / hover-X removes; click toggles enabled.
//   - Upload card: "Distribute load" row. When at least one worker is
//     enabled and the toggle is on, Start Run posts to /api/cluster/start
//     with the unified config; otherwise it falls through to the
//     legacy single-host /api/start path.
//   - Cluster status panel: when a cluster run is active, shows the
//     aggregated counters AND a per-worker row with reachability +
//     contribution breakdown. Polls /api/cluster/status at 2 Hz.
//
// Worker storage: localStorage 'sftp-loadtest-workers-v1' = [
//   { id, url, auth_user, auth_pass, enabled, addedAt }, …
// ].

import { apiFetch } from './api.js';
import { pushToast } from './toast.js';

const KEY = 'sftp-loadtest-workers-v1';

function readAll() {
  try {
    const raw = localStorage.getItem(KEY);
    if (!raw) return [];
    const arr = JSON.parse(raw);
    return Array.isArray(arr) ? arr : [];
  } catch { return []; }
}
function writeAll(arr) {
  try { localStorage.setItem(KEY, JSON.stringify(arr)); } catch {}
}
function newID() { return 'wk-' + Math.random().toString(36).slice(2, 10); }

export function listWorkers() { return readAll(); }
export function addWorker({ url, auth_user, auth_pass }) {
  const arr = readAll();
  const entry = {
    id: newID(),
    url: String(url || '').replace(/\/+$/, ''),
    auth_user: auth_user || '',
    auth_pass: auth_pass || '',
    enabled: true,
    addedAt: new Date().toISOString(),
  };
  arr.push(entry);
  writeAll(arr);
  return entry;
}
export function removeWorker(id) {
  writeAll(readAll().filter((w) => w.id !== id));
}
export function toggleWorker(id) {
  const arr = readAll();
  const w = arr.find((x) => x.id === id);
  if (w) { w.enabled = !w.enabled; writeAll(arr); }
}

// ---------- Sidebar Workers section ----------
const REFRESH_MS = 3000;

export function mountClusterSidebar() {
  const sidebar = document.querySelector('.shell-sidebar');
  if (!sidebar) return;
  if (sidebar.querySelector('[data-role="sidebar-workers"]')) return;

  // Inject the section just before the trust-hosts section.
  const section = document.createElement('div');
  section.className = 'shell-sidebar-section';
  section.innerHTML = `
    <div class="shell-sidebar-section-header">
      <span>Workers</span>
      <button type="button" class="shell-sidebar-toggle" data-role="add-worker"
              title="Add worker URL" style="width:18px; height:18px;">+</button>
    </div>
    <div data-role="sidebar-workers">
      <div class="shell-sidebar-empty">Add a sftp-loadtest URL to fan out a run.</div>
    </div>`;
  // Place above Trusted hosts — operators care about workers right next
  // to Connections/Configs/Runs.
  const trustSection = sidebar.querySelector('[data-role="sidebar-trust"]')?.closest('.shell-sidebar-section');
  if (trustSection) sidebar.insertBefore(section, trustSection);
  else sidebar.appendChild(section);

  const slot = section.querySelector('[data-role="sidebar-workers"]');
  const addBtn = section.querySelector('[data-role="add-worker"]');
  addBtn.addEventListener('click', () => promptAddWorker());

  function render() {
    const list = readAll();
    if (list.length === 0) {
      slot.innerHTML = '<div class="shell-sidebar-empty">Add a sftp-loadtest URL to fan out a run.</div>';
      return;
    }
    slot.innerHTML = list.map((w) => `
      <div class="shell-sidebar-row sidebar-row-with-action" data-id="${w.id}"
           title="${escapeAttr(w.url)}">
        <span class="row-icon" aria-hidden="true">
          <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor"
               stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round">
            <rect x="2" y="3" width="12" height="4" rx="1"/><rect x="2" y="9" width="12" height="4" rx="1"/>
            <circle cx="4" cy="5" r="0.6" fill="currentColor"/><circle cx="4" cy="11" r="0.6" fill="currentColor"/></svg>
        </span>
        <span class="row-label">${escapeHTML(prettyURL(w.url))}</span>
        <span class="row-meta" data-role="worker-state" data-enabled="${w.enabled}">${w.enabled ? 'on' : 'off'}</span>
        <button type="button" class="row-action-btn" data-action="del" data-id="${w.id}" title="Remove">×</button>
      </div>`).join('');
    slot.querySelectorAll('.shell-sidebar-row').forEach((row) => {
      row.addEventListener('click', (ev) => {
        if (ev.target.closest('[data-action="del"]')) return;
        toggleWorker(row.dataset.id);
        render();
      });
    });
    slot.querySelectorAll('[data-action="del"]').forEach((btn) => {
      btn.addEventListener('click', (ev) => {
        ev.stopPropagation();
        if (!confirm('Remove this worker URL?')) return;
        removeWorker(btn.dataset.id);
        render();
      });
    });
  }
  render();
  // Heartbeat keeps the toggle state in sync across tabs.
  setInterval(render, REFRESH_MS);
  window.addEventListener('storage', (ev) => {
    if (ev.key === KEY) render();
  });
}

function promptAddWorker() {
  // Two-step prompt — keeping UI footprint tiny. A modal would be nicer
  // but adding one for a backend-MVP feature isn't worth the spread.
  const url = window.prompt('Worker URL (e.g. http://10.0.0.5:8080):');
  if (!url || !url.trim()) return;
  if (!/^https?:\/\//i.test(url)) {
    pushToast('Worker URL must start with http:// or https://', 'error');
    return;
  }
  const auth_user = window.prompt('BasicAuth user (leave empty for none):') || '';
  let auth_pass = '';
  if (auth_user) auth_pass = window.prompt('BasicAuth password:') || '';
  addWorker({ url: url.trim(), auth_user, auth_pass });
  pushToast(`Added worker ${url}`, 'success');
}

// ---------- Upload card "Distribute load" toggle ----------
//
// Adds a single row at the top of the upload card body that, when checked,
// reroutes Start Run to /api/cluster/start with all enabled workers. A
// status line below the toggle shows how many workers are currently enabled.

export function mountDistributeToggle() {
  const upload = document.getElementById('normalCard');
  if (!upload) return;
  const body = upload.querySelector('.body');
  if (!body) return;
  if (body.querySelector('[data-role="distribute-row"]')) return;

  const row = document.createElement('div');
  row.className = 'cluster-distribute-row';
  row.dataset.role = 'distribute-row';
  row.innerHTML = `
    <label class="cluster-distribute-toggle">
      <input type="checkbox" id="cluster_distribute" />
      <span>Distribute load across workers</span>
    </label>
    <span class="cluster-distribute-status" data-role="distribute-status"></span>`;
  body.insertBefore(row, body.firstChild);

  const cb = row.querySelector('#cluster_distribute');
  const status = row.querySelector('[data-role="distribute-status"]');
  function syncStatus() {
    const enabled = readAll().filter((w) => w.enabled);
    if (enabled.length === 0) {
      status.textContent = 'no workers enabled — add one in the sidebar';
      status.dataset.kind = 'warn';
    } else {
      status.textContent = `${enabled.length} worker${enabled.length === 1 ? '' : 's'} enabled · fpm will be split across them`;
      status.dataset.kind = '';
    }
  }
  syncStatus();
  setInterval(syncStatus, REFRESH_MS);
  cb.addEventListener('change', () => {
    try { localStorage.setItem('sftp-loadtest-distribute-v1', cb.checked ? '1' : '0'); } catch {}
  });
  try { cb.checked = localStorage.getItem('sftp-loadtest-distribute-v1') === '1'; } catch {}
}

// ---------- Cluster Start interceptor ----------
//
// Hooks the global Start Run path: when distribute is checked AND at
// least one worker is enabled, intercept the legacy startBtn click,
// build the unified config, post to /api/cluster/start, and surface a
// toast on success/failure. Otherwise let the legacy click run.

export function mountClusterIntercept() {
  const startBtn = document.getElementById('startBtn');
  if (!startBtn) return;
  startBtn.addEventListener('click', async (ev) => {
    const cb = document.getElementById('cluster_distribute');
    const enabled = readAll().filter((w) => w.enabled);
    if (!cb || !cb.checked || enabled.length === 0) return; // legacy path
    ev.preventDefault();
    ev.stopImmediatePropagation();
    if (typeof window.__sftplBuildRequestBody !== 'function') {
      pushToast('Internal: cannot build request body', 'error');
      return;
    }
    const cfg = window.__sftplBuildRequestBody();
    try {
      const r = await apiFetch('/api/cluster/start', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          workers: enabled.map((w) => ({ url: w.url, auth_user: w.auth_user, auth_pass: w.auth_pass })),
          config: JSON.stringify(cfg),
        }),
      });
      if (r.ok) {
        const j = await r.json();
        pushToast(`Cluster run started across ${enabled.length} worker${enabled.length === 1 ? '' : 's'}`, 'success');
        console.log('Cluster run ids:', j.run_ids);
      } else {
        const txt = await r.text();
        pushToast(`Cluster start failed: ${txt}`, 'error');
      }
    } catch (e) {
      pushToast(`Cluster start error: ${e.message || e}`, 'error');
    }
  }, true); // capture phase so we beat the legacy bubble handler
}

function prettyURL(u) {
  try {
    const p = new URL(u);
    return `${p.hostname}${p.port ? ':' + p.port : ''}`;
  } catch {
    return u;
  }
}

function escapeHTML(s) { return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c])); }
function escapeAttr(s) { return String(s).replace(/"/g, '&quot;'); }
