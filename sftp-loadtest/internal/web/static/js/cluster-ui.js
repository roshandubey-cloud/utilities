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
export function addWorker({ url, auth_user, auth_pass, source, spawn_id }) {
  const arr = readAll();
  const entry = {
    id: newID(),
    url: String(url || '').replace(/\/+$/, ''),
    auth_user: auth_user || '',
    auth_pass: auth_pass || '',
    enabled: true,
    addedAt: new Date().toISOString(),
    // source = "ssh" marks an SSH-bootstrapped worker — its lifecycle is
    // tied to the master's spawn registry, so Forget must POST despawn
    // before dropping the local entry. spawn_id is the master-side id.
    source: source || 'manual',
    spawn_id: spawn_id || '',
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
           data-source="${escapeAttr(w.source || 'manual')}"
           title="${escapeAttr(w.url)}">
        <span class="row-icon" aria-hidden="true">
          <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor"
               stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round">
            <rect x="2" y="3" width="12" height="4" rx="1"/><rect x="2" y="9" width="12" height="4" rx="1"/>
            <circle cx="4" cy="5" r="0.6" fill="currentColor"/><circle cx="4" cy="11" r="0.6" fill="currentColor"/></svg>
        </span>
        <span class="row-label">${escapeHTML(prettyURL(w.url))}</span>
        ${w.source === 'ssh' ? '<span class="cluster-ssh-badge" title="Bootstrapped over SSH">🔗 SSH</span>' : ''}
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
        const id = btn.dataset.id;
        const entry = readAll().find((w) => w.id === id);
        const isSSH = entry && entry.source === 'ssh';
        const ok = await confirmModal({
          title: 'Remove worker',
          message: isSSH
            ? 'This will kill the remote sftp-loadtest process and close the SSH tunnel.'
            : 'This removes the URL from your local list. Any run already dispatched to this worker keeps running.',
          danger: true,
          okLabel: 'Remove',
        });
        if (!ok) return;
        if (isSSH && entry.spawn_id) {
          // POST despawn BEFORE removing the localStorage entry. If the
          // server returns a non-2xx we still drop the entry — the
          // operator's intent is "make this go away" and a stale spawn
          // id on the master is a smaller problem than a row stuck in
          // the sidebar that can't be removed.
          try {
            await apiFetch('/api/worker/despawn', {
              method: 'POST',
              headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify({ id: entry.spawn_id }),
            });
          } catch (e) {
            pushToast(`Despawn warning: ${e.message || e}`, 'warn');
          }
        }
        removeWorker(id);
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

// promptAddWorker opens the dual-tab Add worker modal:
//   - Direct URL: register an already-running sftp-loadtest URL.
//   - SSH bootstrap: install + spawn the binary on a remote via SSH and
//     register the local tunnel URL the master allocates.
// Returns when either tab succeeds (worker registered) or the modal closes.
async function promptAddWorker() {
  return new Promise((resolve) => {
    const bd = document.createElement('div');
    bd.className = 'modal-backdrop';
    bd.dataset.component = 'modal';
    bd.innerHTML = `
      <div class="modal-panel modal-panel-wide" role="dialog" aria-modal="true" aria-label="Add worker">
        <div class="modal-head">Add worker</div>
        <div class="modal-tabs" role="tablist">
          <button type="button" class="modal-tab is-active" data-tab="direct" role="tab">Direct URL</button>
          <button type="button" class="modal-tab" data-tab="ssh" role="tab">SSH bootstrap</button>
        </div>
        <div class="modal-body">
          <div class="modal-tab-panel" data-tab-panel="direct">
            <div class="modal-field">
              <label class="modal-field-label" for="addw_url">URL <span class="modal-field-req">*</span></label>
              <input class="modal-field-input" id="addw_url" type="text" placeholder="http://10.0.0.5:8080" />
              <div class="modal-field-hint">Any reachable sftp-loadtest instance — its /api will receive the per-worker config.</div>
            </div>
            <div class="modal-field">
              <label class="modal-field-label" for="addw_user">BasicAuth user</label>
              <input class="modal-field-input" id="addw_user" type="text" placeholder="(optional)" />
            </div>
            <div class="modal-field">
              <label class="modal-field-label" for="addw_pass">BasicAuth password</label>
              <input class="modal-field-input" id="addw_pass" type="password" placeholder="(optional)" />
            </div>
          </div>
          <div class="modal-tab-panel" data-tab-panel="ssh" hidden>
            <div class="modal-tab-blurb">
              Master will SSH to the host, install the sftp-loadtest binary, spawn it on
              <span class="mono">127.0.0.1:18081</span>, and tunnel HTTP back through the SSH session — no extra port to open.
            </div>
            <div class="modal-field-grid-2">
              <div class="modal-field">
                <label class="modal-field-label" for="ssh_host">Host <span class="modal-field-req">*</span></label>
                <input class="modal-field-input" id="ssh_host" type="text" placeholder="10.0.0.5" />
              </div>
              <div class="modal-field">
                <label class="modal-field-label" for="ssh_port">Port</label>
                <input class="modal-field-input" id="ssh_port" type="text" value="22" />
              </div>
            </div>
            <div class="modal-field">
              <label class="modal-field-label" for="ssh_user">User <span class="modal-field-req">*</span></label>
              <input class="modal-field-input" id="ssh_user" type="text" placeholder="ec2-user" />
            </div>
            <div class="modal-field">
              <label class="modal-field-label" for="ssh_password">Password</label>
              <input class="modal-field-input" id="ssh_password" type="password" placeholder="(or use a private key below)" />
            </div>
            <details class="modal-disclosure" id="ssh-private-key-disclosure">
              <summary>Use SSH private key instead</summary>
              <div class="modal-field">
                <label class="modal-field-label" for="ssh_key">Private key (PEM)</label>
                <textarea class="modal-field-input modal-field-textarea" id="ssh_key" rows="5"
                          placeholder="-----BEGIN OPENSSH PRIVATE KEY-----"></textarea>
              </div>
              <div class="modal-field">
                <label class="modal-field-label" for="ssh_passphrase">Passphrase (if encrypted)</label>
                <input class="modal-field-input" id="ssh_passphrase" type="password" />
              </div>
            </details>
            <div class="modal-field">
              <label class="modal-field-label">Install method</label>
              <label class="modal-radio"><input type="radio" name="ssh_install" value="download" checked /> Download from GitHub release (needs internet on remote)</label>
              <label class="modal-radio"><input type="radio" name="ssh_install" value="upload" /> Upload local binary over SSH (no egress required)</label>
            </div>
            <div class="cluster-ssh-spawn-log" data-role="spawn-log" hidden></div>
          </div>
        </div>
        <div class="modal-foot">
          <button type="button" class="btn btn-ghost" data-role="cancel">Cancel</button>
          <button type="button" class="btn btn-primary" data-role="primary" data-tab-target="direct">Add worker</button>
        </div>
      </div>`;
    document.body.appendChild(bd);
    const panel = bd.querySelector('.modal-panel');
    const tabs = bd.querySelectorAll('.modal-tab');
    const panels = bd.querySelectorAll('.modal-tab-panel');
    const primary = bd.querySelector('[data-role="primary"]');
    const cancelBtn = bd.querySelector('[data-role="cancel"]');
    const close = (val) => {
      document.removeEventListener('keydown', onKey, true);
      bd.remove();
      resolve(val);
    };
    const onKey = (ev) => { if (ev.key === 'Escape') { ev.preventDefault(); close(null); } };
    document.addEventListener('keydown', onKey, true);
    cancelBtn.addEventListener('click', () => close(null));
    bd.addEventListener('click', (ev) => { if (ev.target === bd) close(null); });

    let activeTab = 'direct';
    function switchTab(name) {
      activeTab = name;
      tabs.forEach((t) => t.classList.toggle('is-active', t.dataset.tab === name));
      panels.forEach((p) => { p.hidden = p.dataset.tabPanel !== name; });
      primary.dataset.tabTarget = name;
      primary.textContent = name === 'ssh' ? 'Spawn worker' : 'Add worker';
      primary.disabled = false;
    }
    tabs.forEach((t) => t.addEventListener('click', () => switchTab(t.dataset.tab)));

    primary.addEventListener('click', async () => {
      if (activeTab === 'direct') {
        const url = (panel.querySelector('#addw_url').value || '').trim();
        const user = panel.querySelector('#addw_user').value || '';
        const pass = panel.querySelector('#addw_pass').value || '';
        if (!url) return;
        if (!/^https?:\/\//i.test(url)) {
          pushToast('Worker URL must start with http:// or https://', 'error');
          return;
        }
        addWorker({ url, auth_user: user, auth_pass: pass });
        pushToast(`Added worker ${url}`, 'success');
        close({ kind: 'direct', url });
        return;
      }
      // SSH bootstrap.
      const host = (panel.querySelector('#ssh_host').value || '').trim();
      const port = (panel.querySelector('#ssh_port').value || '22').trim();
      const sshUser = (panel.querySelector('#ssh_user').value || '').trim();
      const password = panel.querySelector('#ssh_password').value || '';
      const pkPem = panel.querySelector('#ssh_key').value || '';
      const passphrase = panel.querySelector('#ssh_passphrase').value || '';
      const install = panel.querySelector('input[name="ssh_install"]:checked')?.value || 'download';
      if (!host || !sshUser) {
        pushToast('Host and User are required', 'error');
        return;
      }
      if (!password && !pkPem) {
        pushToast('Provide a password or a private key', 'error');
        return;
      }
      const logBox = panel.querySelector('[data-role="spawn-log"]');
      logBox.hidden = false;
      logBox.innerHTML = '';
      const steps = [
        'Dialing SSH',
        'Detecting arch',
        'Reaping orphan workers',
        install === 'upload' ? 'Uploading binary' : 'Installing (downloading)',
        'Smoke test',
        'Spawning worker',
        'Tunnel ready',
      ];
      steps.forEach((s, i) => {
        const li = document.createElement('div');
        li.className = 'cluster-ssh-spawn-log-row';
        li.dataset.step = String(i);
        li.innerHTML = `<span class="status">⏳</span><span class="label">${escapeHTML(s)}</span>`;
        logBox.appendChild(li);
      });
      primary.disabled = true;
      primary.textContent = 'Spawning…';
      try {
        const r = await apiFetch('/api/worker/spawn', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            host, port, user: sshUser,
            password,
            private_key_pem: pkPem,
            passphrase,
            install_method: install,
          }),
        });
        const j = await r.json().catch(() => ({}));
        if (!r.ok || j.ok === false) {
          // Render the partial log + error.
          (j.log || []).forEach((line, i) => {
            const row = logBox.querySelector(`[data-step="${i}"]`);
            if (row) row.querySelector('.status').textContent = '✓';
          });
          const err = document.createElement('div');
          err.className = 'cluster-ssh-spawn-log-row is-error';
          err.innerHTML = `<span class="status">✗</span><span class="label">${escapeHTML(j.error || ('HTTP ' + r.status))}</span>`;
          logBox.appendChild(err);
          primary.disabled = false;
          primary.textContent = 'Retry';
          return;
        }
        // Success — mark every step ✓ then add the worker.
        steps.forEach((_, i) => {
          const row = logBox.querySelector(`[data-step="${i}"]`);
          if (row) row.querySelector('.status').textContent = '✓';
        });
        addWorker({
          url: j.url,
          auth_user: '',
          auth_pass: '',
          source: 'ssh',
          spawn_id: j.id,
        });
        pushToast(`Spawned worker on ${host} (${j.arch})`, 'success');
        close({ kind: 'ssh', id: j.id, url: j.url });
      } catch (e) {
        const err = document.createElement('div');
        err.className = 'cluster-ssh-spawn-log-row is-error';
        err.innerHTML = `<span class="status">✗</span><span class="label">${escapeHTML(e.message || String(e))}</span>`;
        logBox.appendChild(err);
        primary.disabled = false;
        primary.textContent = 'Retry';
      }
    });
  });
}

// ---------- Cluster view (main pane) ----------
//
// Renders inside [data-view="cluster"]. Shows the list of saved workers
// in a roomier layout (compared to the sidebar's row format) plus the
// status of the cluster coordinator (idle / active / aggregated counters).

export function mountClusterView() {
  // CRITICAL: scope to .shell-main. The sidebar's primary-nav rows ALSO
  // carry data-view="cluster" (for the click-to-switch-view handler);
  // an unscoped querySelector matches the sidebar row first and renders
  // the cluster view's HTML INTO the sidebar instead of the main pane.
  const view = document.querySelector('.shell-main [data-view="cluster"]');
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
        <thead><tr><th>URL</th><th>Source</th><th>Auth user</th><th>Enabled</th><th></th></tr></thead>
        <tbody>${list.map((w) => `
          <tr data-id="${escapeAttr(w.id)}" data-source="${escapeAttr(w.source || 'manual')}">
            <td class="mono">${escapeHTML(w.url)}</td>
            <td>${w.source === 'ssh' ? '<span class="cluster-ssh-badge">🔗 SSH</span>' : 'manual'}</td>
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
        const id = btn.dataset.id;
        const entry = readAll().find((w) => w.id === id);
        const isSSH = entry && entry.source === 'ssh';
        const ok = await confirmModal({
          title: 'Remove worker',
          message: isSSH
            ? 'This will kill the remote sftp-loadtest process and close the SSH tunnel.'
            : 'Remove this URL from the local list?',
          danger: true, okLabel: 'Remove' });
        if (!ok) return;
        if (isSSH && entry.spawn_id) {
          try {
            await apiFetch('/api/worker/despawn', {
              method: 'POST',
              headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify({ id: entry.spawn_id }),
            });
          } catch (e) {
            pushToast(`Despawn warning: ${e.message || e}`, 'warn');
          }
        }
        removeWorker(id);
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
      // Disable + force-uncheck. The toggle is meaningless with no
      // workers; previously letting the user check it produced a toast
      // at Start time. Better UX: prevent the check in the first place
      // and reflect the state visually.
      cb.disabled = true;
      if (cb.checked) {
        cb.checked = false;
        try { localStorage.setItem('sftp-loadtest-distribute-v1', '0'); } catch {}
      }
      row.dataset.disabled = '1';
    } else {
      status.textContent = `${enabled.length} worker${enabled.length === 1 ? '' : 's'} enabled · fpm will be split across them`;
      status.dataset.kind = '';
      cb.disabled = false;
      row.dataset.disabled = '0';
    }
  }
  // Sync immediately AND on any storage event for the workers key, so
  // adding / enabling a worker in the sidebar instantly enables the
  // toggle without waiting for the heartbeat tick.
  syncStatus();
  setInterval(syncStatus, REFRESH_MS);
  window.addEventListener('storage', (ev) => {
    if (ev && ev.key === 'sftp-loadtest-workers-v1') syncStatus();
  });
  cb.addEventListener('change', () => {
    try { localStorage.setItem('sftp-loadtest-distribute-v1', cb.checked ? '1' : '0'); } catch {}
  });
  // Restore checked state from localStorage — but only if there's at
  // least one worker enabled. Otherwise we'd flash checked → unchecked
  // on every page load.
  try {
    const persisted = localStorage.getItem('sftp-loadtest-distribute-v1') === '1';
    const haveWorkers = readAll().some((w) => w.enabled);
    cb.checked = persisted && haveWorkers;
  } catch {}
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
    if (!cb || !cb.checked) return; // distribute off → legacy local-run path
    const enabled = readAll().filter((w) => w.enabled);
    if (enabled.length === 0) {
      // Distribute is on but no worker is enabled. Silently falling
      // through to the legacy local-run path was the previous bug —
      // the operator clicked Start expecting fan-out and got a single
      // local run instead. Block + tell them what to do.
      ev.preventDefault();
      ev.stopImmediatePropagation();
      pushToast('Distribute is on but no workers are enabled. Add or enable a worker in the sidebar, or turn Distribute off.', 'warn');
      return;
    }
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
        // config is an object, not a string. The wrapper JSON.stringify
        // serialises it inline; the server's json.RawMessage receives
        // the object literal which splitConfig parses as a map. Wrapping
        // with an extra JSON.stringify(cfg) produced a doubly-escaped
        // string that splitConfig refused with "config not an object".
        body: JSON.stringify({
          workers: enabled.map((w) => ({ url: w.url, auth_user: w.auth_user, auth_pass: w.auth_pass })),
          config: cfg,
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
