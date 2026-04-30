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
import { form as formModal, confirm as confirmModal } from './modal.js';

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

  // Inject the Workers section as the bottom-most "library" group in the
  // sidebar. The primary nav has its own "Cluster" view at the top; this
  // section is the persistent shortcut to the worker URLs themselves.
  const section = document.createElement('div');
  section.className = 'shell-sidebar-section';
  section.innerHTML = `
    <div class="shell-sidebar-section-header">
      <span>Workers</span>
      <button type="button" class="shell-sidebar-section-header-add" data-role="add-worker"
              title="Add worker URL">+</button>
    </div>
    <div data-role="sidebar-workers">
      <div class="shell-sidebar-empty">Add a sftp-loadtest URL to fan out a run.</div>
    </div>`;
  sidebar.appendChild(section);

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
      btn.addEventListener('click', async (ev) => {
        ev.stopPropagation();
        const ok = await confirmModal({
          title: 'Remove worker',
          message: 'This removes the URL from your local list. Any run already dispatched to this worker keeps running.',
          danger: true,
          okLabel: 'Remove',
        });
        if (!ok) return;
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

async function promptAddWorker() {
  // Single modal collects URL + optional BasicAuth credentials. Replaces
  // the old window.prompt() chain which was unreliable (blocked in Wails
  // desktop builds, hard to cancel cleanly in browser).
  const result = await formModal({
    title: 'Add worker',
    submitLabel: 'Add worker',
    fields: [
      { name: 'url', label: 'URL', placeholder: 'http://10.0.0.5:8080', required: true,
        hint: 'Any reachable sftp-loadtest instance — its /api will receive the per-worker config.' },
      { name: 'auth_user', label: 'BasicAuth user', placeholder: '(optional)',
        hint: 'Leave both empty if the worker has no -auth-user flag set.' },
      { name: 'auth_pass', label: 'BasicAuth password', type: 'password', placeholder: '(optional)' },
    ],
  });
  if (!result) return;
  const url = (result.url || '').trim();
  if (!url) return;
  if (!/^https?:\/\//i.test(url)) {
    pushToast('Worker URL must start with http:// or https://', 'error');
    return;
  }
  addWorker({ url, auth_user: result.auth_user || '', auth_pass: result.auth_pass || '' });
  pushToast(`Added worker ${url}`, 'success');
}

// ---------- Cluster view (main pane) ----------
//
// Renders inside [data-view="cluster"]. Shows the list of saved workers
// in a roomier layout (compared to the sidebar's row format) plus the
// status of the cluster coordinator (idle / active / aggregated counters).

export function mountClusterView() {
  const view = document.querySelector('[data-view="cluster"]');
  if (!view || view.dataset.clusterMounted) return;
  view.dataset.clusterMounted = '1';

  view.innerHTML = `
    <section class="cluster-view-panel">
      <header class="cluster-view-head">
        <div>
          <div class="cluster-view-title">Cluster mode</div>
          <div class="cluster-view-sub">Fan a run out across multiple sftp-loadtest instances. Each worker dials the SFTP target independently and the master aggregates results.</div>
        </div>
        <button type="button" class="btn btn-primary" data-role="cluster-add">+ Add worker</button>
      </header>
      <div class="cluster-view-status" data-role="cluster-status">
        <div class="cluster-view-status-cell"><span class="label">state</span><span class="value" data-role="state">idle</span></div>
        <div class="cluster-view-status-cell"><span class="label">workers</span><span class="value" data-role="worker-count">0</span></div>
        <div class="cluster-view-status-cell"><span class="label">files</span><span class="value" data-role="files">0</span></div>
        <div class="cluster-view-status-cell"><span class="label">throughput</span><span class="value" data-role="mbps">0</span> <span class="label">MB/s</span></div>
        <div class="cluster-view-status-cell"><span class="label">failed</span><span class="value" data-role="failed">0</span></div>
      </div>
      <div class="cluster-view-list" data-role="cluster-list">
        <div class="cluster-view-empty">No workers yet. Click “Add worker” to register a URL.</div>
      </div>
    </section>`;

  view.querySelector('[data-role="cluster-add"]').addEventListener('click', () => promptAddWorker());

  function renderList() {
    const list = readAll();
    const slot = view.querySelector('[data-role="cluster-list"]');
    view.querySelector('[data-role="worker-count"]').textContent =
      `${list.filter((w) => w.enabled).length} / ${list.length}`;
    if (list.length === 0) {
      slot.innerHTML = '<div class="cluster-view-empty">No workers yet. Click “Add worker” to register a URL.</div>';
      return;
    }
    slot.innerHTML = `
      <table class="cluster-view-table">
        <thead><tr><th>URL</th><th>Auth user</th><th>Enabled</th><th></th></tr></thead>
        <tbody>${list.map((w) => `
          <tr data-id="${escapeAttr(w.id)}">
            <td class="mono">${escapeHTML(w.url)}</td>
            <td>${escapeHTML(w.auth_user || '—')}</td>
            <td>
              <label class="cluster-view-toggle">
                <input type="checkbox" data-action="toggle" data-id="${escapeAttr(w.id)}" ${w.enabled ? 'checked' : ''}>
                <span>${w.enabled ? 'on' : 'off'}</span>
              </label>
            </td>
            <td><button type="button" class="btn btn-ghost btn-sm" data-action="delete" data-id="${escapeAttr(w.id)}">Remove</button></td>
          </tr>`).join('')}
        </tbody>
      </table>`;
    slot.querySelectorAll('[data-action="toggle"]').forEach((cb) => {
      cb.addEventListener('change', () => { toggleWorker(cb.dataset.id); renderList(); });
    });
    slot.querySelectorAll('[data-action="delete"]').forEach((btn) => {
      btn.addEventListener('click', async () => {
        const ok = await confirmModal({ title: 'Remove worker', message: 'Remove this URL from the local list?', danger: true, okLabel: 'Remove' });
        if (!ok) return;
        removeWorker(btn.dataset.id);
        renderList();
      });
    });
  }
  renderList();
  setInterval(renderList, REFRESH_MS);
  window.addEventListener('storage', (ev) => { if (ev.key === KEY) renderList(); });

  // Poll cluster status while view is active.
  async function pollStatus() {
    try {
      const r = await apiFetch('/api/cluster/status');
      if (!r.ok) throw new Error();
      const j = await r.json();
      view.querySelector('[data-role="state"]').textContent = j.active ? 'active' : 'idle';
      view.querySelector('[data-role="files"]').textContent = String(j.total_files || 0);
      view.querySelector('[data-role="mbps"]').textContent = (j.overall_mbps || 0).toFixed(2);
      view.querySelector('[data-role="failed"]').textContent = String(j.failed_files || 0);
    } catch { /* silent */ }
    setTimeout(pollStatus, REFRESH_MS);
  }
  pollStatus();
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
