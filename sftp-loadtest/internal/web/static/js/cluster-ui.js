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
           data-url="${escapeAttr(w.url)}"
           data-auth-user="${escapeAttr(w.auth_user || '')}"
           data-auth-pass="${escapeAttr(w.auth_pass || '')}"
           data-health="unknown"
           title="${escapeAttr(w.url)}">
        <span class="worker-led" data-role="worker-led" aria-hidden="true"
              title="Health unknown — will check next tick"></span>
        <span class="row-label">${escapeHTML(prettyURL(w.url))}</span>
        ${w.source === 'ssh' ? '<span class="cluster-ssh-badge" title="Bootstrapped over SSH">🔗 SSH</span>' : ''}
        <span class="row-meta" data-role="worker-state" data-enabled="${w.enabled}">${w.enabled ? 'on' : 'off'}</span>
        <button type="button" class="row-action-btn" data-action="del" data-id="${w.id}" title="Remove">×</button>
      </div>`).join('');
    // Kick off an immediate health check for every row, then keep
    // refreshing on the heartbeat below.
    pollWorkerHealth(slot);
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
  // Health poll — every 5 s, ping each registered worker via the
  // master-side proxy /api/worker/probe and update the LED. Slower
  // than the local render heartbeat (3 s) so we don't hammer remote
  // workers unnecessarily.
  setInterval(() => {
    const slot = sidebar.querySelector('[data-role="sidebar-workers"]');
    if (slot) pollWorkerHealth(slot);
  }, 5000);
}

// pollWorkerHealth fans out one /api/worker/probe per row and writes
// the result back to the row's data-health + LED tooltip. Probe is
// concurrent — slow workers don't block the others.
async function pollWorkerHealth(slot) {
  const rows = slot.querySelectorAll('.shell-sidebar-row[data-url]');
  rows.forEach(async (row) => {
    const url = row.dataset.url;
    if (!url) return;
    try {
      const r = await apiFetch('/api/worker/probe', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          url,
          auth_user: row.dataset.authUser || '',
          auth_pass: row.dataset.authPass || '',
        }),
      });
      const j = await r.json().catch(() => ({}));
      const led = row.querySelector('[data-role="worker-led"]');
      if (!led) return;
      if (j.ok && j.active) {
        row.dataset.health = 'active';
        led.title = `Reachable · run active (${j.active_run_id || 'unknown id'}) · ${j.latency_ms} ms`;
      } else if (j.ok) {
        row.dataset.health = 'idle';
        led.title = `Reachable · idle · ${j.latency_ms} ms`;
      } else {
        row.dataset.health = 'down';
        led.title = j.error
          ? `Unreachable: ${j.error}`
          : 'Unreachable';
      }
    } catch (e) {
      const led = row.querySelector('[data-role="worker-led"]');
      if (led) {
        row.dataset.health = 'down';
        led.title = `Probe error: ${e.message || e}`;
      }
    }
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
              <strong>The remote needs nothing pre-installed</strong> beyond a working SSH server.
            </div>
            <div class="modal-field-grid-2">
              <div class="modal-field">
                <label class="modal-field-label" for="ssh_host">Host <span class="modal-field-req">*</span></label>
                <input class="modal-field-input" id="ssh_host" type="text" placeholder="10.0.0.5 or my-vm.example.com" />
                <div class="modal-field-hint">Public IP, private IP, or DNS name of the remote machine. Use <span class="mono">ssh user@host</span> from your terminal first to confirm reachability.</div>
              </div>
              <div class="modal-field">
                <label class="modal-field-label" for="ssh_port">Port</label>
                <input class="modal-field-input" id="ssh_port" type="text" value="22" />
                <div class="modal-field-hint">Default 22. Some hosts use 2222 or a custom port — check the cloud console.</div>
              </div>
            </div>
            <div class="modal-field">
              <label class="modal-field-label" for="ssh_user">User <span class="modal-field-req">*</span></label>
              <input class="modal-field-input" id="ssh_user" type="text" placeholder="(varies — see hint below)" />
              <div class="modal-field-hint" data-role="user-hint">
                Common defaults: <span class="mono">ec2-user</span> (Amazon Linux), <span class="mono">ubuntu</span> (Ubuntu AMIs), <span class="mono">admin</span> (Debian),
                <span class="mono">azureuser</span> (Azure), <span class="mono">opc</span> (Oracle), <span class="mono">root</span> (some VPS), or your local username.
              </div>
            </div>
            <div class="modal-field">
              <label class="modal-field-label" for="ssh_password">Password</label>
              <input class="modal-field-input" id="ssh_password" type="password" placeholder="(or use a private key below)" />
              <div class="modal-field-hint">Most cloud VMs disable password auth — use the private-key disclosure below in that case.</div>
            </div>
            <details class="modal-disclosure" id="ssh-private-key-disclosure">
              <summary>Use SSH private key instead</summary>
              <div class="modal-field">
                <label class="modal-field-label" for="ssh_key">Private key (PEM)</label>
                <textarea class="modal-field-input modal-field-textarea" id="ssh_key" rows="5"
                          placeholder="-----BEGIN OPENSSH PRIVATE KEY-----"></textarea>
                <div class="modal-field-hint">Paste the contents of your <span class="mono">~/.ssh/id_*</span> or the <span class="mono">.pem</span> the cloud console gave you. Held in memory only — never written to localStorage.</div>
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
            <div class="modal-actions-inline">
              <button type="button" class="btn btn-ghost" data-role="ssh-preflight">Test SSH access</button>
              <span class="modal-actions-hint">Verifies reach + auth + write rights without installing anything.</span>
            </div>
            <div class="cluster-ssh-preflight-log" data-role="preflight-log" hidden></div>
            <div class="cluster-ssh-spawn-log" data-role="spawn-log" hidden></div>
            <details class="modal-disclosure cluster-ssh-gotchas">
              <summary>Common SSH gotchas (click if a step keeps failing)</summary>
              <ul class="cluster-ssh-gotchas-list">
                <li><b>Connection refused</b> → SSH daemon not running, or port wrong. Run <span class="mono">systemctl status sshd</span> on the remote, or check the cloud security group / firewall allows port 22 from your IP.</li>
                <li><b>i/o timeout</b> → firewall blocks your IP. AWS: Security Group inbound rule for port 22; GCP: VPC firewall; Azure: NSG. Check from <span class="mono">curl -v telnet://host:22</span>.</li>
                <li><b>no such host</b> → DNS lookup failed. Try the IP directly, or <span class="mono">dig &lt;host&gt;</span> from your terminal.</li>
                <li><b>permission denied (publickey)</b> → server allows ONLY key auth. Open the disclosure above and paste your private key.</li>
                <li><b>permission denied (password)</b> → wrong username or password. Try <span class="mono">root</span> first if you don't know — many distros let root in.</li>
                <li><b>install path NOT writable</b> → operator's user can't write to <span class="mono">/tmp</span>. Try a different user (<span class="mono">root</span>, <span class="mono">sudo</span>-capable user), or set the binary path under <span class="mono">/home/&lt;you&gt;/sftp-loadtest</span>.</li>
                <li><b>curl / unzip not found</b> → switch the install method to "Upload local binary" — bypasses the dependency.</li>
                <li><b>Don't have an SSH server on the remote yet?</b> Linux: <span class="mono">apt install openssh-server</span> or <span class="mono">yum install openssh-server</span> + <span class="mono">systemctl enable --now sshd</span>. Windows: install OpenSSH Server from "Optional features".</li>
              </ul>
            </details>
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

    // Smart username hint — when the operator types a host that looks
    // like a known cloud provider, hint at the most likely username
    // for that platform so they don't have to guess. Live as the host
    // is typed.
    const hostInput = bd.querySelector('#ssh_host');
    const userHint = bd.querySelector('[data-role="user-hint"]');
    if (hostInput && userHint) {
      const defaultHint = userHint.innerHTML;
      hostInput.addEventListener('input', () => {
        const h = hostInput.value.toLowerCase();
        let suggestion = '';
        if (h.includes('amazonaws.com') || h.includes('compute.internal') || h.includes('ec2-')) {
          suggestion = '<strong>AWS detected</strong> — try <span class="mono">ec2-user</span> (Amazon Linux), <span class="mono">ubuntu</span> (Ubuntu AMI), <span class="mono">admin</span> (Debian), or <span class="mono">root</span> (some marketplace AMIs).';
        } else if (h.includes('cloudapp.azure.com') || h.includes('azure.com')) {
          suggestion = '<strong>Azure detected</strong> — usually <span class="mono">azureuser</span> unless you set a custom name when creating the VM.';
        } else if (h.includes('googleusercontent.com') || h.includes('compute.googleapis.com')) {
          suggestion = '<strong>GCP detected</strong> — your local username (the one in <span class="mono">gcloud auth list</span>) when using OS Login; otherwise the SSH key\'s metadata username.';
        } else if (h.includes('oraclecloud.com')) {
          suggestion = '<strong>Oracle Cloud detected</strong> — usually <span class="mono">opc</span> on Oracle Linux, <span class="mono">ubuntu</span> on Ubuntu images.';
        } else if (h.includes('digitalocean') || h.includes('linode') || h.includes('vultr')) {
          suggestion = '<strong>VPS detected</strong> — usually <span class="mono">root</span> for fresh droplets/instances, your account user otherwise.';
        }
        userHint.innerHTML = suggestion || defaultHint;
      });
    }

    // Test SSH access button — POSTs /api/worker/preflight and renders a
    // checklist of remote-side capabilities (reachable / arch detected /
    // install path writable / curl + unzip if needed). Operator runs this
    // BEFORE Spawn so they don't commit to a multi-step install before
    // knowing the basics work.
    const preflightBtn = bd.querySelector('[data-role="ssh-preflight"]');
    const preflightLog = bd.querySelector('[data-role="preflight-log"]');
    if (preflightBtn && preflightLog) {
      preflightBtn.addEventListener('click', async () => {
        const host = (bd.querySelector('#ssh_host').value || '').trim();
        const port = (bd.querySelector('#ssh_port').value || '22').trim();
        const sshUser = (bd.querySelector('#ssh_user').value || '').trim();
        const password = bd.querySelector('#ssh_password').value || '';
        const pkPem = bd.querySelector('#ssh_key').value || '';
        const passphrase = bd.querySelector('#ssh_passphrase').value || '';
        if (!host || !sshUser) {
          pushToast('Host and User are required', 'error');
          return;
        }
        if (!password && !pkPem) {
          pushToast('Provide a password or a private key', 'error');
          return;
        }
        preflightLog.hidden = false;
        preflightLog.innerHTML = '<div class="cluster-ssh-spawn-log-row"><span class="status">⏳</span><span class="label">Running preflight…</span></div>';
        preflightBtn.disabled = true;
        const previousLabel = preflightBtn.textContent;
        preflightBtn.textContent = 'Testing…';
        try {
          const r = await apiFetch('/api/worker/preflight', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
              host, port, user: sshUser,
              password,
              private_key_pem: pkPem,
              passphrase,
            }),
          });
          if (!r.ok) {
            const txt = await r.text();
            preflightLog.innerHTML = '';
            preflightLog.appendChild(renderErrorCard(txt || 'HTTP ' + r.status));
            return;
          }
          const j = await r.json();
          // Render each log line — Preflight already prefixes ✓ / ✗ on
          // each step, so we just need consistent row markup.
          preflightLog.innerHTML = '';
          (j.log || []).forEach((line) => {
            const row = document.createElement('div');
            const isErr = line.startsWith('✗');
            row.className = 'cluster-ssh-spawn-log-row' + (isErr ? ' is-error' : '');
            const sym = line.startsWith('✓') ? '✓' : (isErr ? '✗' : '·');
            const text = line.replace(/^[✓✗·]\s*/, '');
            row.innerHTML = `<span class="status">${sym}</span><span class="label">${escapeHTML(text)}</span>`;
            preflightLog.appendChild(row);
          });
          // Verdict pill — the operator's at-a-glance answer.
          const verdict = document.createElement('div');
          verdict.className = 'cluster-ssh-preflight-verdict' + (j.ok ? ' is-ok' : ' is-warn');
          if (j.ok) {
            verdict.textContent = `READY — arch: ${j.arch}, user: ${j.whoami || '(unknown)'}, host: ${j.hostname || '(unknown)'}`;
          } else if (!j.reachable) {
            verdict.textContent = `BLOCKED — cannot reach ${host}:${port} (check SSH credentials + firewall)`;
          } else if (!j.can_write) {
            verdict.textContent = `BLOCKED — install path not writable by user ${j.whoami || sshUser}`;
          } else if (!j.arch) {
            verdict.textContent = `BLOCKED — uname could not detect a supported platform`;
          } else {
            verdict.textContent = `INCOMPLETE — see failed checks above`;
          }
          preflightLog.appendChild(verdict);
          // When something failed, surface a situation-aware fix card.
          // Walk the log for the first ✗ line and feed it to the
          // decoder; the operator gets a step-by-step Try-this list
          // tied to the exact failure mode.
          if (!j.ok || !j.reachable || !j.can_write || (!j.has_curl || !j.has_unzip)) {
            let failLine = '';
            if (!j.reachable) failLine = j.error || 'connect: connection refused';
            else if (!j.can_write) failLine = 'install path NOT writable';
            else if (!j.has_curl) failLine = 'curl not found';
            else if (!j.has_unzip) failLine = 'unzip not found';
            else failLine = (j.log || []).find((l) => String(l).startsWith('✗')) || '';
            if (failLine) {
              const card = renderErrorCard(failLine);
              card.classList.add('cluster-ssh-error-card-inline');
              preflightLog.appendChild(card);
            }
          }
          // Disable the Spawn primary button if the install method is
          // download but curl/unzip are missing — the spawn would just
          // fail later. The operator either ticks "upload" or fixes the
          // remote.
          const installMethod = panel.querySelector('input[name="ssh_install"]:checked')?.value || 'download';
          if (installMethod === 'download' && (!j.has_curl || !j.has_unzip)) {
            const warn = document.createElement('div');
            warn.className = 'cluster-ssh-preflight-verdict is-warn';
            warn.textContent = 'curl + unzip required for download method — switch to upload OR install them on the remote.';
            preflightLog.appendChild(warn);
          }
        } catch (e) {
          preflightLog.innerHTML = '';
          preflightLog.appendChild(renderErrorCard(e.message || String(e)));
        } finally {
          preflightBtn.disabled = false;
          preflightBtn.textContent = previousLabel;
        }
      });
    }

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
          // Render the partial log + a decoded error card so the
          // operator gets situation-aware guidance instead of a raw
          // stack trace.
          (j.log || []).forEach((line, i) => {
            const row = logBox.querySelector(`[data-step="${i}"]`);
            if (row) row.querySelector('.status').textContent = '✓';
          });
          const errMsg = j.error || ('HTTP ' + r.status);
          logBox.appendChild(renderErrorCard(errMsg));
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
        logBox.appendChild(renderErrorCard(e.message || String(e)));
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

// decodeSpawnError maps a raw SSH / install / spawn error string into
// situation-aware guidance. Returns {title, why, fix[]} so the UI can
// render an actionable card instead of a wall of stack-trace text.
// When the input doesn't match a known pattern, returns null and the
// caller falls back to the raw string.
export function decodeSpawnError(raw) {
  if (!raw) return null;
  const m = String(raw).toLowerCase();
  // Network reach / firewall / DNS — these come BEFORE any auth happens,
  // so the operator should fix the network before doing anything else.
  if (m.includes('connection refused')) {
    return {
      title: 'Connection refused — SSH not listening on that host:port',
      why: 'The TCP socket said "no thanks." Either the SSH daemon is not running on the remote, or it is listening on a different port than the one you typed.',
      fix: [
        'On the remote, check `systemctl status sshd` (Linux) or `Get-Service sshd` (Windows). Start it if stopped: `sudo systemctl enable --now sshd`.',
        'If SSH is running but on a non-default port, set Port to that value (some setups use 2222).',
        'If the remote is `127.0.0.1`, you are pointing at YOUR OWN machine — that is rarely what you want. Use the remote\'s actual IP or hostname.',
        'If the host is in another VPC / cloud, confirm the security group / firewall allows port 22 from your IP.',
      ],
    };
  }
  if (m.includes('no such host') || m.includes('lookup')) {
    return {
      title: 'DNS lookup failed — hostname did not resolve',
      why: 'Your machine could not translate the hostname to an IP address.',
      fix: [
        'Try the IP address directly (e.g. `54.193.10.2` instead of `my-vm.example.com`).',
        'From your terminal: `dig <hostname>` — if it returns NXDOMAIN, the name is wrong or unreachable.',
        'If the host is in a private VPC, you will need a VPN or a bastion to reach it.',
      ],
    };
  }
  if (m.includes('i/o timeout') || m.includes('deadline exceeded') || m.includes('no route to host') || m.includes('network is unreachable')) {
    return {
      title: 'Network timeout — packets are not reaching the host',
      why: 'TCP handshake never completed. Almost always a firewall or routing problem between your machine and the remote.',
      fix: [
        'Cloud security groups: open port 22 inbound from your public IP. AWS: Security Groups; GCP: VPC firewall; Azure: NSG.',
        'From your terminal: `nc -vz <host> 22` should connect within a second. If it hangs, port is blocked.',
        'VPN / Tailscale / Zero Trust gateway: confirm your client is up and the host is reachable through it.',
        'Local firewall on YOUR machine could also be blocking outbound 22.',
      ],
    };
  }
  // Auth — credentials worked enough to talk SSH but not enough to log in.
  if (m.includes('unable to authenticate') || (m.includes('handshake failed') && m.includes('publickey'))) {
    return {
      title: 'Authentication failed — server rejected the credentials',
      why: 'The TCP + SSH handshake worked, but neither password nor key matched what the remote accepts.',
      fix: [
        'Double-check the username — common defaults vary by image: `ec2-user` on Amazon Linux, `ubuntu` on Ubuntu AMIs, `admin` on Debian, `azureuser` on Azure, `opc` on Oracle, sometimes plain `root`.',
        'If your terminal `ssh user@host` works, copy that exact username here.',
        'Most cloud images disable password auth — open "Use SSH private key instead" and paste the `.pem` from the cloud console.',
        'For an encrypted key, fill the Passphrase field too.',
        'Check `/var/log/auth.log` (Linux) on the remote for the rejection reason — that\'s the ground truth.',
      ],
    };
  }
  if (m.includes('handshake failed') || m.includes('connection reset')) {
    return {
      title: 'SSH handshake aborted by the server',
      why: 'The remote answered TCP but tore the SSH conversation down before auth completed. Often fail2ban / sshguard / a rate-limiter kicking in.',
      fix: [
        'Wait 5–10 minutes — fail2ban typically bans for 10 minutes after a few bad attempts.',
        'Check `/var/log/auth.log` on the remote for `Connection from ... closed`.',
        'If this is repeatable from the same source IP, you may be permanently banned — connect from a different network or unblock yourself on the remote.',
      ],
    };
  }
  // Filesystem / install path issues surfaced by Preflight.
  if (m.includes('not writable') || m.includes('permission denied') && m.includes('write')) {
    return {
      title: 'Install path is not writable by the SSH user',
      why: 'The operator user does not have write access to the directory where the binary will live.',
      fix: [
        'Use a path the user CAN write — try setting the remote binary path to `/home/<your-user>/sftp-loadtest` instead of `/tmp/sftp-loadtest`.',
        'Or SSH in as a user with write access to `/tmp` (often `root` or any sudo-capable user).',
        'On hardened hosts, `/tmp` is sometimes mounted noexec — choose a different path.',
      ],
    };
  }
  if (m.includes('curl') && m.includes('not found')) {
    return {
      title: 'curl is missing on the remote',
      why: '"Download from GitHub" install method needs curl + unzip on the remote to fetch the release zip.',
      fix: [
        'Switch the install method to "Upload local binary over SSH" — that bypasses curl entirely.',
        'Or install curl: `apt install curl unzip` (Debian/Ubuntu) / `yum install curl unzip` (RHEL/Amazon Linux).',
      ],
    };
  }
  if (m.includes('unzip') && m.includes('not found')) {
    return {
      title: 'unzip is missing on the remote',
      why: '"Download from GitHub" install method needs unzip to extract the release.',
      fix: [
        'Switch the install method to "Upload local binary over SSH" — that delivers the binary directly.',
        'Or install unzip: `apt install unzip` / `yum install unzip`.',
      ],
    };
  }
  if (m.includes('uname') && m.includes('unsupported')) {
    return {
      title: 'Remote platform not supported',
      why: 'The detected OS / architecture is not in the release matrix.',
      fix: [
        'Supported: linux-amd64, linux-arm64, darwin-amd64, darwin-arm64, windows-amd64. Other platforms are not built.',
        'On unusual ARM boards / BSD / Solaris this tool will not run as a worker.',
      ],
    };
  }
  return null;
}

// renderErrorCard turns decodeSpawnError output into a DOM element
// the spawn-log and preflight-log can append. Falls back to a plain
// row if no decoder match — never swallows the original message.
export function renderErrorCard(rawError) {
  const card = document.createElement('div');
  card.className = 'cluster-ssh-error-card';
  const decoded = decodeSpawnError(rawError);
  if (!decoded) {
    card.classList.add('is-bare');
    card.innerHTML =
      '<div class="cluster-ssh-error-title">Spawn failed</div>' +
      `<div class="cluster-ssh-error-raw mono">${escapeHTML(rawError || 'unknown error')}</div>`;
    return card;
  }
  const fixHTML = decoded.fix.map((step) => `<li>${escapeHTML(step).replace(/`([^`]+)`/g, '<span class="mono">$1</span>')}</li>`).join('');
  card.innerHTML =
    `<div class="cluster-ssh-error-title">${escapeHTML(decoded.title)}</div>` +
    `<div class="cluster-ssh-error-why">${escapeHTML(decoded.why)}</div>` +
    `<div class="cluster-ssh-error-fix-label">Try this:</div>` +
    `<ol class="cluster-ssh-error-fix">${fixHTML}</ol>` +
    `<details class="cluster-ssh-error-raw-disclosure"><summary>Raw error</summary>` +
    `<div class="cluster-ssh-error-raw mono">${escapeHTML(rawError)}</div></details>`;
  return card;
}
