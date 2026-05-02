// ---------- helpers ----------
const $ = (id) => document.getElementById(id);
const fmt = {
  mb:   (b) => (b / (1024 * 1024)).toFixed(2),
  num:  (n, d=2) => (n || 0).toFixed(d),
  time: (s) => s ? new Date(s).toLocaleTimeString() : '',
};

// apiFetch is the single entry point for every server call. It always sends
// X-Requested-With (which the server's CSRFGuard middleware requires on state-
// changing requests) and Accept: application/json so the browser doesn't get
// surprised by an HTML error page mid-parse. Use this for /api/* calls only.
async function apiFetch(url, init) {
  init = init || {};
  init.headers = Object.assign({
    'X-Requested-With': 'sftp-loadtest',
    'Accept': 'application/json',
  }, init.headers || {});
  return fetch(url, init);
}

// ---------- quick-pick chips (folder presets + recent connections) ----------
// Any button with data-fill="<input-id>" data-value="<value>" writes that value
// into the input and fires a change event so persistence + validation trigger.
document.addEventListener('click', (ev) => {
  const btn = ev.target.closest('.chip-btn');
  if (!btn) return;
  if (btn.dataset.fill) {
    const el = $(btn.dataset.fill);
    if (el) {
      el.value = btn.dataset.value;
      el.dispatchEvent(new Event('change'));
      el.focus();
    }
  } else if (btn.dataset.removeConn) {
    removeConn(btn.dataset.removeConn);
  }
});

// ---------- connection history (host:port, last 8) ----------
const CONN_KEY = 'sftp-loadtest-conn-history-v1';
const CONN_MAX = 8;

function readConnHistory() {
  try { return JSON.parse(localStorage.getItem(CONN_KEY) || '[]'); } catch { return []; }
}
function writeConnHistory(list) {
  try { localStorage.setItem(CONN_KEY, JSON.stringify(list.slice(0, CONN_MAX))); } catch {}
}
function rememberConn(host, port) {
  if (!host) return;
  const key = `${host}:${port}`;
  const list = readConnHistory().filter(e => `${e.host}:${e.port}` !== key);
  list.unshift({ host, port });
  writeConnHistory(list);
  renderConnHistory();
}
function removeConn(key) {
  const list = readConnHistory().filter(e => `${e.host}:${e.port}` !== key);
  writeConnHistory(list);
  renderConnHistory();
}
function renderConnHistory() {
  const list = readConnHistory();
  // populate the native autocomplete datalists (host + port)
  const hostDL = $('host_history');
  const portDL = $('port_history');
  hostDL.innerHTML = '';
  portDL.innerHTML = '';
  const seenHosts = new Set(), seenPorts = new Set();
  list.forEach(e => {
    if (!seenHosts.has(e.host)) { seenHosts.add(e.host); hostDL.insertAdjacentHTML('beforeend', `<option value="${e.host}">`); }
    const p = String(e.port);
    if (!seenPorts.has(p)) { seenPorts.add(p); portDL.insertAdjacentHTML('beforeend', `<option value="${p}">`); }
  });
  // render the clickable chip row under the host/port fields
  const chipHost = $('conn_chips');
  chipHost.innerHTML = '';
  if (list.length === 0) {
    chipHost.innerHTML = '<span class="hint" style="margin:0">No recent hosts yet — the last 8 you run against will appear here.</span>';
    return;
  }
  chipHost.insertAdjacentHTML('beforeend', '<span class="hint" style="margin:0 4px 0 0">Recent:</span>');
  list.forEach(e => {
    const label = `${e.host}:${e.port}`;
    const row = document.createElement('span');
    row.style.display = 'inline-flex';
    row.style.alignItems = 'center';
    row.style.gap = '2px';
    row.innerHTML =
      `<button type="button" class="chip-btn" title="Use ${label}" data-use-host="${e.host}" data-use-port="${e.port}">${label}</button>` +
      `<button type="button" class="chip-btn del" title="Remove" data-remove-conn="${label}">×</button>`;
    chipHost.appendChild(row);
  });
  chipHost.querySelectorAll('[data-use-host]').forEach(b => {
    b.addEventListener('click', () => {
      $('host').value = b.dataset.useHost;
      $('port').value = b.dataset.usePort;
      $('host').dispatchEvent(new Event('change'));
      $('port').dispatchEvent(new Event('change'));
    });
  });
}

// ---------- CSV password masking ----------
// Focus/blur model: the textarea is always editable. While it has focus
// (user is typing/pasting) it displays raw content. The moment focus
// leaves it, the raw value is captured to dataset.raw and the visible
// content is replaced with a masked version (password column → ••••••••).
// Form submission and localStorage always read the raw value, never the
// masked display — so nothing downstream sees the mask.
const CSV_FIELDS = ['normal_users', 'large_users', 'download_users'];

function maskCsv(raw) {
  return (raw || '').split('\n').map(line => {
    if (!line.trim()) return line;
    const parts = line.split(',');
    if (parts.length < 2) return line; // malformed row — show as-is
    const pw = parts[1] || '';
    // Fixed-width mask so line lengths don't leak password length.
    parts[1] = pw ? '••••••••' : '';
    return parts.join(',');
  }).join('\n');
}

function getCsvRaw(fieldId) {
  const el = $(fieldId);
  if (!el) return '';
  // If the user is actively editing, el.value is the raw they just typed.
  // Otherwise dataset.raw is the authoritative copy captured on last blur.
  return (el.dataset.editing === '1') ? el.value : (el.dataset.raw || '');
}

function setCsvRaw(fieldId, raw) {
  const el = $(fieldId);
  if (!el) return;
  el.dataset.raw = raw || '';
  if (el.dataset.editing !== '1') el.value = maskCsv(raw);
}

document.querySelectorAll('textarea.csv-users').forEach(ta => {
  ta.addEventListener('focus', () => {
    // Reveal raw content so the user can read/paste/edit without fighting mask chars.
    ta.value = ta.dataset.raw || '';
    ta.dataset.editing = '1';
  });
  ta.addEventListener('blur', () => {
    // Capture whatever they typed as the new raw, then re-mask the display.
    ta.dataset.raw = ta.value;
    ta.value = maskCsv(ta.value);
    ta.dataset.editing = '0';
    saveConfig();
  });
});

// ---------- test connection ----------
// Validates host/port reachability before a real run. If a user CSV is
// present, also validates SSH/SFTP/auth and (if upload-folder is set) that
// the folder is listable. Picks the first user from normal users CSV;
// falls back to the first download user; falls back to TCP-only.
async function probeConnection() {
  const out = $('probe_result');
  out.textContent = 'Testing…';
  out.style.color = 'var(--muted)';
  const host = $('host').value.trim();
  const port = parseInt($('port').value) || 0;
  if (!host || !port) {
    out.textContent = 'Enter host and port first.';
    out.style.color = 'var(--err)';
    return;
  }
  // First user from whichever CSV is populated.
  function firstUser(csv) {
    for (const line of (csv || '').split('\n')) {
      const trimmed = line.trim();
      if (!trimmed) continue;
      const parts = trimmed.split(',');
      if (parts.length >= 2) return { user: parts[0].trim(), pass: parts[1] };
    }
    return null;
  }
  const cred = firstUser(getCsvRaw('normal_users')) || firstUser(getCsvRaw('download_users'));
  const folder = $('folder').value.trim();
  const tofu = ($('probe_tofu') && $('probe_tofu').checked) ||
               !!document.querySelector('[data-role="tofu"]')?.checked;
  const body = { host, port };
  // Protocol + TLS knobs — without these, /api/probe defaults to sftp and
  // tries an SSH handshake against an FTPS port, which surfaces as the
  // misleading "SSH handshake failed" error. Read the same form elements
  // submitForm uses so the probe and the run agree on what they're hitting.
  const protocol = (document.getElementById('protocol')?.value || 'sftp').toLowerCase();
  body.protocol = protocol;
  if (protocol === 'ftps') {
    const tlsMode = (document.getElementById('tls_mode')?.value || '').toLowerCase();
    if (tlsMode) body.tls_mode = tlsMode;
    const tlsServer = (document.getElementById('tls_server_name')?.value || '').trim();
    if (tlsServer) body.tls_server_name = tlsServer;
    if (document.getElementById('tls_skip_verify')?.checked) body.tls_insecure_skip_verify = true;
  }
  if (cred) { body.username = cred.user; body.password = cred.pass; body.folder = folder; }
  if (tofu) { body.trust_on_first_use = true; }
  try {
    const res = await apiFetch('/api/probe', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    const r = await res.json();
    if (r.ok) {
      let msg = `✓ ${cred ? 'authenticated as ' + cred.user : 'reachable'} — `;
      const t = [];
      if (r.tcp_ms !== undefined)      t.push(`tcp ${r.tcp_ms} ms`);
      if (r.ssh_sftp_ms !== undefined) t.push(`ssh+sftp ${r.ssh_sftp_ms} ms`);
      if (r.list_ms !== undefined)     t.push(`list ${r.list_ms} ms`);
      msg += t.join(' · ');
      if (r.note) msg += ` — ${r.note}`;
      // If TOFU just captured a new key, surface its fingerprint loudly so
      // the operator can verify it out-of-band before running real load.
      if (r.captured_fingerprint) {
        msg += `\n  added new host key for ${r.captured_for_host || host}: ${r.captured_fingerprint}`;
      }
      out.textContent = msg;
      out.style.color = 'var(--ok)';
      out.style.whiteSpace = 'pre-wrap';
    } else {
      out.textContent = `✗ failed at ${r.stage || '?'}: ${r.error || 'unknown error'}`;
      out.style.color = 'var(--err)';
      out.style.whiteSpace = 'pre-wrap';
    }
  } catch (e) {
    out.textContent = '✗ probe error: ' + e.message;
    out.style.color = 'var(--err)';
  }
}
$('probeBtn').addEventListener('click', probeConnection);

// ---------- conditional panels ----------
// Every checkbox with data-toggles on a card flips the card's "off" class,
// which hides the body via CSS. Works without layout jumps and fits the
// "lightweight" theme — no JS animations.
document.querySelectorAll('input[data-toggles]').forEach(cb => {
  const target = document.getElementById(cb.dataset.toggles);
  const sync = () => target.classList.toggle('off', !cb.checked);
  cb.addEventListener('change', sync);
  sync();
});

// ---------- config persistence (localStorage) ----------
const CFG_KEYS = ['host','port','folder','parallel','duration','poll','timeout_min','max_fails',
  'fpm','nmin','nmax','ncontent','normal_users',
  'lmin','lmax','lunit','interval','large_users',
  'dfolder','dparallel','download_users'];
const TOGGLES = ['normal_enabled','large_enabled','download_enabled'];
const LS_KEY = 'sftp-loadtest-config-v1';
const SAVE_PWD_KEY = 'sftp-loadtest-save-passwords-v1';

// stripPasswordsFromCSV blanks out the password column (position 1) on every
// row of a user CSV. Format is "user,pass,pat1*,pat2*,..." — we keep the
// surrounding shape so an imported file (or a rejoined-after-clear UI) still
// loads cleanly; the operator just has to retype the passwords.
function stripPasswordsFromCSV(raw) {
  if (!raw) return raw;
  return raw.split('\n').map(line => {
    if (!line.trim()) return line;
    const cols = line.split(',');
    if (cols.length >= 2) cols[1] = '';
    return cols.join(',');
  }).join('\n');
}

function shouldSavePasswords() {
  try { return localStorage.getItem(SAVE_PWD_KEY) === '1'; } catch { return false; }
}
function setSavePasswords(on) {
  try { localStorage.setItem(SAVE_PWD_KEY, on ? '1' : '0'); } catch {}
  // Re-save the current form so the new setting takes effect immediately —
  // turning the toggle off should erase any stored passwords on the next save.
  saveConfig();
}

function restoreConfig() {
  try {
    const saved = JSON.parse(localStorage.getItem(LS_KEY) || '{}');
    CFG_KEYS.forEach(k => {
      if (saved[k] === undefined || !$(k)) return;
      if (CSV_FIELDS.includes(k)) setCsvRaw(k, saved[k]);   // seed masked view from saved raw
      else $(k).value = saved[k];
    });
    TOGGLES.forEach(k => {
      if (saved[k] !== undefined && $(k)) {
        $(k).checked = !!saved[k];
        $(k).dispatchEvent(new Event('change'));
      }
    });
    if (saved.download_match_mode) setDownloadMatchMode(saved.download_match_mode);
  } catch {}
  // Reflect the persisted save-passwords toggle into the UI on load.
  const cb = $('save_passwords');
  if (cb) cb.checked = shouldSavePasswords();
}
function saveConfig() {
  const out = {};
  const keepPwd = shouldSavePasswords();
  CFG_KEYS.forEach(k => {
    const el = $(k); if (!el) return;
    if (CSV_FIELDS.includes(k)) {
      const raw = getCsvRaw(k);
      out[k] = keepPwd ? raw : stripPasswordsFromCSV(raw);
    } else {
      out[k] = el.value;
    }
  });
  TOGGLES.forEach(k => { const el = $(k); if (el) out[k] = el.checked; });
  out.download_match_mode = getDownloadMatchMode();
  try { localStorage.setItem(LS_KEY, JSON.stringify(out)); } catch {}
}

// Re-save the form when the operator flips the round-trip mode so the
// choice survives a page reload without waiting for another field to
// trigger a save.
document.addEventListener('change', (ev) => {
  if (ev.target && ev.target.name === 'download_match_mode') {
    saveConfig();
  }
});

// clearStoredCredentials wipes localStorage entries that hold or could hold
// passwords, and blanks the password column in every CSV textarea on screen.
// Connection history (host:port only) is preserved.
function clearStoredCredentials() {
  if (!confirm('Clear all saved passwords from this browser? Hostnames and other config are kept.')) return;
  try { localStorage.removeItem(LS_KEY); } catch {}
  CSV_FIELDS.forEach(id => {
    const stripped = stripPasswordsFromCSV(getCsvRaw(id));
    setCsvRaw(id, stripped);
  });
  saveConfig();
  alert('Saved passwords cleared. Hosts and connection history kept.');
}

CFG_KEYS.concat(TOGGLES).forEach(k => { const el = $(k); if (el) el.addEventListener('change', saveConfig); });
restoreConfig();

// ---------- start / stop ----------
// ensureHostKeyTrusted runs a pre-flight /api/probe with the first user's
// credentials. If the server presents a host key we haven't seen before, the
// probe responds with requires_consent + the SHA-256 fingerprint; we show
// that to the operator in a confirm() dialog and, on Accept, re-probe with
// trust_on_first_use=true to actually append the key. Returns true when the
// key is trusted (run can proceed), false when the operator declined.
//
// Any non-host-key probe failure (auth, unreachable, etc.) returns true so
// /api/start can run and surface its own error — we only intercept the
// specific case where TLS/SSH host-key trust is missing.
async function ensureHostKeyTrusted(host, port) {
  // Only meaningful for SFTP — the SSH host-key consent flow that this
  // function drives doesn't apply to FTP/FTPS. For FTPS, the run-side
  // TOFU plumbing in protocol.go handles cert trust automatically.
  // Skipping the pre-flight probe here also avoids the misleading
  // "SSH handshake failed" message on FTPS ports (TLS cert probed
  // without protocol field defaulted to sftp).
  const protocol = (document.getElementById('protocol')?.value || 'sftp').toLowerCase();
  if (protocol !== 'sftp') return true;

  function firstUser(raw) {
    if (!raw) return null;
    for (const line of raw.split('\n')) {
      const t = line.trim();
      if (!t) continue;
      const parts = t.split(',');
      if (parts.length >= 2) return { user: parts[0].trim(), pass: parts[1] };
    }
    return null;
  }
  const cred = firstUser(getCsvRaw('normal_users')) ||
               firstUser(getCsvRaw('large_users')) ||
               firstUser(getCsvRaw('download_users'));
  if (!cred) return true; // no creds, /api/start will fail validation with a useful message

  const probeBody = { host, port, username: cred.user, password: cred.pass, protocol: 'sftp' };
  let r;
  try {
    r = await (await apiFetch('/api/probe', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(probeBody),
    })).json();
  } catch {
    return true; // probe itself failed — let /api/start show the real error
  }
  if (r.ok) return true;
  if (!r.requires_consent || !r.captured_fingerprint) {
    // Probe failed for some other reason (auth, refused, list error). Don't
    // block — let /api/start fail with the underlying error so the operator
    // sees the actual problem instead of a wrong "host key" question.
    return true;
  }

  const accept = window.confirm(
    `New SSH host key for ${r.captured_for_host || host}\n\n` +
    `${r.captured_fingerprint}\n\n` +
    `Trust this key and add it to known_hosts?\n\n` +
    `Click OK only if you've verified the fingerprint out-of-band.\n` +
    `Click Cancel to abort the run.`
  );
  if (!accept) return false;

  probeBody.trust_on_first_use = true;
  const r2 = await (await apiFetch('/api/probe', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(probeBody),
  })).json();
  if (!r2.ok) {
    throw new Error(`failed to add host key: ${r2.error || 'unknown'}`);
  }
  return true;
}

// startRunGuidance — pre-flight check that surfaces friendly toasts
// instead of letting Start run silently POST a half-baked config to
// the backend. Reads the same form fields buildRequestBody() will,
// returns false (after focusing + pulsing the offender) when the
// run can't possibly succeed.
//
// Layered checks:
//   1. Host + Port must be filled (the connection card OR the legacy
//      hidden inputs that mirror it).
//   2. At least one workload (Normal / Large / Download) must be
//      enabled — otherwise /api/start would 400.
//   3. Each enabled workload must have a non-empty users CSV.
//
// Uses window.__guide which is wired by guidance.js on app load.
function startRunGuidance() {
  const g = (typeof window !== 'undefined') ? window.__guide : null;
  if (!g) return true; // module import failed; let backend errors fall through
  // The visible host/port inputs live on the new Quick Checks card
  // (#conn-host / #conn-port). The legacy hidden #host / #port mirror
  // them — we focus the visible ones so the operator sees the cursor
  // jump.
  const hostEl = document.getElementById('conn-host') || $('host');
  const portEl = document.getElementById('conn-port') || $('port');
  if (!g.guideRequiredFields([
    { el: hostEl, label: 'Host' },
    { el: portEl, label: 'Port' },
  ], { action: 'start the run' })) return false;

  const normalOn   = $('normal_enabled')?.checked;
  const largeOn    = $('large_enabled')?.checked;
  const downloadOn = $('download_enabled')?.checked;
  if (!normalOn && !largeOn && !downloadOn) {
    g.guideCondition(false, 'Enable at least one workload (Normal, Large, or Download) to start the run.', {
      focusEl: $('normal_enabled'),
    });
    return false;
  }

  // Each enabled workload needs users. Empty users CSV → backend 400
  // with a cryptic "no rows" error far from the textarea.
  const checks = [
    { on: normalOn,   id: 'normal_users',   label: 'Normal users CSV' },
    { on: largeOn,    id: 'large_users',    label: 'Large users CSV' },
    { on: downloadOn, id: 'download_users', label: 'Download users CSV' },
  ];
  for (const c of checks) {
    if (!c.on) continue;
    const raw = (typeof getCsvRaw === 'function') ? getCsvRaw(c.id) : ($(c.id)?.value || '');
    const hasRow = raw.split('\n').map((s) => s.trim()).filter(Boolean).some((line) => line.split(',').length >= 3);
    if (!hasRow) {
      g.guideCondition(false, `Add at least one user (username,password,pattern) to ${c.label} to start the run.`, {
        focusEl: $(c.id),
      });
      return false;
    }
  }
  return true;
}

async function start() {
  $('err').textContent = '';
  // Pre-flight guidance: a click on Start run with empty Host /
  // Port / no enabled workload / empty users CSV used to silently
  // POST to /api/start and surface a backend error far from the
  // field that needed attention. Now the operator gets a focused
  // field + accent pulse + toast that names exactly what's missing.
  if (!startRunGuidance()) return;
  saveConfig();
  const body = buildRequestBody();
  try {
    const trusted = await ensureHostKeyTrusted(body.host, body.port);
    if (!trusted) {
      $('err').textContent = 'Run cancelled — server host key was not trusted.';
      return;
    }
    const res = await apiFetch('/api/start', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    if (!res.ok) throw new Error(await res.text());
    rememberConn(body.host, body.port);
    $('startBtn').disabled = true;
    $('stopBtn').disabled = false;
  } catch (e) { $('err').textContent = e.message; }
}
async function stop() { await apiFetch('/api/stop', { method: 'POST' }); }

$('startBtn').addEventListener('click', start);
$('stopBtn').addEventListener('click', stop);

// ---------- Quit button ----------
// Wails on some Windows builds renders without a native close affordance, so
// the masthead carries an explicit Quit button that calls the Wails runtime
// when present (closes the desktop window) and falls back to window.close()
// in a regular browser tab. The confirm() prompt prevents accidental closes
// during a live run.
if ($('quitBtn')) {
  $('quitBtn').addEventListener('click', () => {
    if (!window.confirm('Quit SFTP Load Test? Any active run will be cancelled.')) return;
    if (window.runtime && typeof window.runtime.Quit === 'function') {
      window.runtime.Quit();
    } else {
      // Plain browser tab: window.close() only works on tabs the script opened
      // itself; if the user has the page in a normal tab the call is a no-op
      // and we fall back to a navigation hint.
      window.close();
      setTimeout(() => {
        $('err').textContent = 'Browser blocked window.close — close this tab manually.';
      }, 100);
    }
  });
}

// ---------- speed source tagging ----------
// MB/s must never be blank or misleading. When per-file timing is unreliable
// (< 1 MiB OR < 100 ms — swamped by SFTP handshake), substitute the minute
// bucket rate, then the overall rate, with a superscript tag so the source
// is visible.
const MIN_BYTES = 1048576, MIN_DUR_MS = 100;
function effSpeed(bytes, startIso, endIso, perFileMbps, metrics) {
  const start = startIso ? new Date(startIso).getTime() : 0;
  const end = endIso ? new Date(endIso).getTime() : 0;
  if (!start || !end) return { mbps: 0, src: 'none' };
  const dur = end - start;
  if (bytes >= MIN_BYTES && dur >= MIN_DUR_MS) return { mbps: perFileMbps, src: 'file' };
  const minute = Math.floor(start / 60000);
  const bucket = (metrics.per_minute || []).find(b => b.minute === minute);
  if (bucket && bucket.mbps > 0) return { mbps: bucket.mbps, src: 'window' };
  if (metrics.overall_mbps > 0) return { mbps: metrics.overall_mbps, src: 'overall' };
  return { mbps: perFileMbps || 0, src: 'file' };
}
function fmtSpeed(info) {
  const v = (info.mbps || 0).toFixed(2);
  if (info.src === 'file' || info.src === 'none') return v;
  return v + `<sup class="muted" title="substituted from ${info.src} rate">·${info.src[0]}</sup>`;
}

// ---------- runs list (right pane header) ----------
let pinnedRun = null;
async function pollRuns() {
  try {
    const { runs } = await (await apiFetch('/api/runs')).json();
    const tb = $('runs_body');
    tb.innerHTML = '';
    if (!runs || runs.length === 0) {
      tb.innerHTML = `<tr><td colspan="6" class="muted" style="text-align:center;padding:16px">No runs yet.</td></tr>`;
      return;
    }
    runs.forEach(r => {
      const tr = document.createElement('tr');
      const started = r.started_at ? new Date(r.started_at).toLocaleString() : '';
      const pinned = pinnedRun === r.id;
      const viewBtn = pinned
        ? `<button disabled>Viewing</button>`
        : `<button class="ghost" data-view="${r.id}">View</button>`;
      const sbadge = (r.started_by === 'schedule') ? `<span class="sched-tag" title="fired automatically by the scheduler">⏰ SCHED</span>` : '';
      tr.innerHTML = `<td>${sbadge}${r.id}</td><td>${started}</td><td>${r.active ? '<b style="color:var(--accent)">running</b>' : 'stopped'}</td><td>${r.total_files || 0}</td><td>${(r.overall_mbps || 0).toFixed(2)}</td><td>${viewBtn} <a class="link" href="/api/report.csv?run=${encodeURIComponent(r.id)}">CSV</a></td>`;
      tb.appendChild(tr);
    });
    tb.querySelectorAll('[data-view]').forEach(btn => {
      btn.addEventListener('click', () => { pinnedRun = btn.dataset.view; poll(); pollRuns(); });
    });
  } catch {}
}
$('liveBtn').addEventListener('click', () => { pinnedRun = null; poll(); pollRuns(); });

// ---------- main status poll ----------
async function poll() {
  const url = pinnedRun ? `/api/status?run=${encodeURIComponent(pinnedRun)}` : '/api/status';
  try {
    const s = await (await apiFetch(url)).json();

    // proc badge
    if (s.proc) {
      $('p_cpu').textContent = (s.proc.cpu_percent || 0).toFixed(1);
      $('p_heap').textContent = (s.proc.heap_mb || 0).toFixed(1);
      $('p_sys').textContent = (s.proc.sys_mb || 0).toFixed(1);
      $('p_gr').textContent = s.proc.goroutines || 0;
      $('p_cpu').className = (s.proc.cpu_percent > 70) ? 'hot' : '';
      // FD pressure: turn the chip yellow at 70% of the soft limit, red at 90%.
      const fd = s.proc.fd_in_use;
      if (fd === undefined || fd < 0) {
        $('p_fd').textContent = '—';
        $('p_fdlim').textContent = '';
        $('p_fd').className = '';
      } else {
        $('p_fd').textContent = fd;
        $('p_fdlim').textContent = hostFDLimit ? `/${hostFDLimit}` : '';
        const ratio = hostFDLimit ? fd / hostFDLimit : 0;
        $('p_fd').className = ratio > 0.9 ? 'hot' : (ratio > 0.7 ? 'hot' : '');
        // Mirror onto the host strip when in pressure
        const cell = $('h_fdlimit');
        if (cell) cell.className = 'hv ' + (ratio > 0.9 ? 'bad' : (ratio > 0.7 ? 'warn' : ''));
      }
    }

    if (!s.metrics) {
      $('live_pill').textContent = 'idle';
      $('live_pill').classList.remove('running');
      $('sched_banner').style.display = 'none';
      // Reset network cell to idle when no metrics
      const link = window.__linkMbps || 0;
      $('h_net').textContent = link ? `${formatLink(link)} link · idle` : 'idle';
      window.__hadNet = false;
      return;
    }
    const m = s.metrics;
    // Live network throughput in the host strip — most useful "bandwidth"
    // signal for a load-test tool. Compare against your NIC nominal rate.
    {
      const obs = m.overall_mbps || 0;
      const link = window.__linkMbps || 0;
      const obsTxt = formatThroughput(obs);
      $('h_net').textContent = link ? `${obsTxt} (link: ${formatLink(link)})` : obsTxt;
      window.__hadNet = true;
    }
    $('live_pill').textContent = s.active ? 'running' : 'stopped';
    $('live_pill').classList.toggle('running', !!s.active);

    // Scheduler banner: surface a scheduled run regardless of pin state, so
    // the user never misses a background-fired run (and can grab its CSV).
    const bn = $('sched_banner');
    if (s.active && s.started_by === 'schedule') {
      $('sched_banner_text').textContent = `${s.run_id} — started ${new Date(s.started_at).toLocaleString()}`;
      $('sched_banner_csv').href = `/api/report.csv?run=${encodeURIComponent(s.run_id)}`;
      bn.style.display = 'flex';
    } else {
      bn.style.display = 'none';
    }
    // Legacy: the top "Download CSV" button used to live in the hero
    // actions row. Removed in v0.14.7 — runs-history shows per-row CSVs
    // and the schedule banner already updates its own link below. This
    // assignment stays guarded so an old build's cached HTML (no
    // #csvBtn) doesn't throw.
    const csvBtnEl = $('csvBtn');
    if (csvBtnEl && s.run_id) csvBtnEl.href = `/api/report.csv?run=${encodeURIComponent(s.run_id)}`;

    $('m_elapsed').textContent = m.elapsed || '—';
    $('m_files').textContent = m.total_files || 0;
    $('m_mb').textContent = fmt.mb(m.total_bytes || 0);
    $('m_overall').textContent = fmt.num(m.overall_mbps);
    $('m_last').textContent = fmt.num(m.last_minute_mbps);
    $('m_base').textContent = fmt.num(m.baseline_mbps);
    $('m_pending').textContent = s.pending_trackids || 0;
    $('m_slow').textContent = (m.slowdowns || []).length;
    $('m_skip').textContent = s.dispatch_skips || 0;
    $('m_dcomp').textContent = s.download_completed || 0;
    $('m_dorph').textContent = s.download_orphans || 0;
    // Toggle [.dl-only] tiles based on whether the active run actually
    // configured downloads. /api/status surfaces download_enabled (true
    // when r.Cfg.Download != nil); we mirror it onto the tiles parent
    // so CSS can hide the irrelevant rows without DOM churn each poll.
    const dlEnabled = !!s.download_enabled;
    document.querySelectorAll('.dl-only').forEach((el) => {
      el.dataset.dlEnabled = dlEnabled ? 'true' : 'false';
    });
    $('m_failf').textContent = s.failed_files || 0;

    // error-code chips
    const ec = s.errors_by_code || {};
    const chips = $('err_chips');
    chips.innerHTML = '';
    const codes = Object.keys(ec);
    if (codes.length === 0) {
      chips.innerHTML = '';
    } else {
      codes.sort().forEach(code => {
        const c = document.createElement('span');
        c.className = 'chip';
        c.innerHTML = `${code} <b>${ec[code]}</b>`;
        chips.appendChild(c);
      });
    }

    // disabled-users chips (one per user taken out of rotation this run)
    const du = s.disabled_users || [];
    $('m_disabled').textContent = du.length;
    const dchips = $('disabled_chips');
    dchips.innerHTML = '';
    du.forEach(d => {
      const c = document.createElement('span');
      c.className = 'chip';
      // Compose the chip body and tooltip from whatever the backend sent.
      // last_file is empty for non-file failures (DIAL/AUTH); only show it
      // when it's actually meaningful so the chip stays terse for those.
      const fileSuffix = d.last_file ? ` · file: ${d.last_file}` : '';
      const fileTooltip = d.last_file ? `\nlast file: ${d.last_file}` : '';
      c.title = `${d.kind} user "${d.user}" — ${d.total_failed} total fails, last=${d.last_code}${fileTooltip}`;
      c.innerHTML = `<b>⛔ ${d.user}</b> (${d.kind}, ${d.last_code}${fileSuffix})`;
      dchips.appendChild(c);
    });

    // slowdown table
    const sb = $('slow_body');
    sb.innerHTML = '';
    const enriched = s.slowdowns_enriched || [];
    const src = enriched.length ? enriched : (m.slowdowns || []);
    if (src.length === 0) {
      sb.innerHTML = `<tr><td colspan="7" class="muted" style="text-align:center;padding:16px">No slowdowns detected.</td></tr>`;
    } else {
      src.forEach(sd => {
        const tr = document.createElement('tr'); tr.className = 'slow';
        const files = sd.files || [];
        const summary = files.length
          ? files.map(f => `${f.filename}${f.kind === 'large' ? ' (LARGE)' : ''}${f.track_id ? ' → ' + f.track_id : ' → ⏳'}`).join('<br>')
          : '<span class="muted">no files</span>';
        tr.innerHTML = `<td>${fmt.time(sd.at)}</td><td>${sd.files_so_far}</td><td>${fmt.mb(sd.bytes_so_far)}</td><td>${fmt.num(sd.window_mbps)}</td><td>${fmt.num(sd.baseline_mbps)}</td><td>${fmt.num((sd.drop_pct || 0) * 100, 1)}%</td><td style="font-size:11px;white-space:normal">${summary}</td>`;
        sb.appendChild(tr);
      });
    }

    // recent uploads
    const rb = $('rec_body');
    rb.innerHTML = '';
    const recs = s.records || [];
    if (recs.length === 0) {
      rb.innerHTML = `<tr><td colspan="14" class="muted" style="text-align:center;padding:16px">${s.active ? 'Warming up…' : 'Start a run to see activity here.'}</td></tr>`;
    } else {
      recs.slice().reverse().forEach(r => {
        const tr = document.createElement('tr');
        const errTag = r.Error ? `<span class="err-text" title="${r.Error}">${(r.ErrorCode || 'ERR')}</span>` : '';
        const derrTag = r.DownloadError ? `<span class="err-text" title="${r.DownloadError}">dl</span>` : '';
        const kindBadge = r.Kind === 'large' ? '<span class="kind-large">LARGE</span>' : (r.Kind || 'normal');
        const waitSec = r.DownloadWait ? (r.DownloadWait / 1e9).toFixed(2) : '';
        const procMin = (r.TrackIDWait && r.TrackIDWait > 0) ? (r.TrackIDWait / 60e9).toFixed(2) : '';
        const upDur = new Date(r.EndTime || 0).getTime() - new Date(r.StartTime || 0).getTime();
        const up = effSpeed(r.SizeBytes, r.StartTime, r.EndTime, r.SpeedMBps, m);
        const dl = effSpeed(r.DownloadSizeBytes, r.DownloadStartTime, r.DownloadEndTime, r.DownloadSpeedMBps, m);
        tr.innerHTML =
          `<td title="${r.User}">${r.User}</td>`+
          `<td>${kindBadge}</td>`+
          `<td title="${r.Filename}">${r.Filename}</td>`+
          `<td>${fmt.time(r.StartTime)}</td>`+
          `<td>${fmt.time(r.EndTime)}</td>`+
          `<td>${(r.SizeBytes/1024).toFixed(1)}</td>`+
          `<td>${fmtSpeed(up)}</td>`+
          `<td title="${r.TrackID || ''}">${r.TrackID || ''} ${errTag}</td>`+
          `<td>${r.DownloadUser || ''}</td>`+
          `<td>${procMin}</td>`+
          `<td>${fmt.time(r.DownloadStartTime)}</td>`+
          `<td>${fmt.time(r.DownloadEndTime)}</td>`+
          `<td>${waitSec}</td>`+
          `<td>${fmtSpeed(dl)} ${derrTag}</td>`;
        rb.appendChild(tr);
      });
    }

    if (!s.active) { $('startBtn').disabled = false; $('stopBtn').disabled = true; }
  } catch {}
}

// ---------- build startReq body ----------
// Shared by the Start Run button, the Schedule button, and Export — every
// path produces exactly the same shape the /api/start endpoint expects.
function buildRequestBody() {
  // Public-key auth: read straight from the connection card's PEM
  // textarea + passphrase input. Only attach when the disclosure is
  // OPEN AND the PEM is non-empty — same gate the probe uses, so a
  // closed disclosure with stale text never silently switches the run.
  const keyDisclosure = document.querySelector('[data-role="key-disclosure"]');
  const pkEl = document.getElementById('conn-private-key');
  const pkPassEl = document.getElementById('conn-private-key-passphrase');
  let privateKeyPEM = '';
  let privateKeyPass = '';
  if (keyDisclosure && keyDisclosure.open && pkEl && pkEl.value.trim()) {
    privateKeyPEM = pkEl.value;
    if (pkPassEl && pkPassEl.value) privateKeyPass = pkPassEl.value;
  }
  // Read the v0.13.0 multi-protocol fields. The picker drives a hidden
  // #protocol input; FTPS-only knobs are read directly so empty values
  // never leak as truthy strings into /api/start.
  const protocol = (document.getElementById('protocol')?.value || 'sftp').toLowerCase();
  const tlsMode = (document.getElementById('tls_mode')?.value || '').toLowerCase();
  const tlsServerName = (document.getElementById('tls_server_name')?.value || '').trim();
  const tlsSkipVerify = !!document.getElementById('tls_skip_verify')?.checked;
  // The single TOFU toggle above the FTPS section drives both the SSH
  // host-key TOFU (used by /api/probe for SFTP) AND the FTPS leaf-cert
  // TOFU (used by /api/start when protocol=ftps). Default on so a first
  // run against a new FTPS server with a self-signed cert just works —
  // the leaf gets pinned, subsequent runs verify strictly. Operators
  // with a strict-CA-only posture can untick the box.
  const tofuChecked = !!document.querySelector('[data-role="tofu"]')?.checked;
  return {
    host: $('host').value.trim(),
    port: parseInt($('port').value) || 22,
    protocol,
    tls_mode: protocol === 'ftps' ? tlsMode : '',
    tls_server_name: protocol === 'ftps' ? tlsServerName : '',
    tls_insecure_skip_verify: protocol === 'ftps' ? tlsSkipVerify : false,
    tls_trust_on_first_use: protocol === 'ftps' ? tofuChecked : false,
    // v0.14 source/sink fields. readSource / readSink return null when
    // the operator left the picker on the default (synthetic / discard)
    // — the backend then uses the v0.13.x defaults exactly. Only one
    // import to keep the legacy serializer's DOM-coupled style.
    normal_source: $('normal_enabled').checked && window.__srcSink ? window.__srcSink.readSource('normal') : null,
    large_source:  $('large_enabled').checked  && window.__srcSink ? window.__srcSink.readSource('large')  : null,
    download_sink: $('download_enabled').checked && window.__srcSink ? window.__srcSink.readSink() : null,
    upload_folder: $('folder').value.trim(),
    parallel_streams: parseInt($('parallel').value) || 1,
    duration_hours: parseFloat($('duration').value) || 1,
    poll_seconds: parseInt($('poll').value) || 3,
    track_id_timeout_seconds: (parseInt($('timeout_min').value) || 10) * 60,
    max_consecutive_failures: parseInt($('max_fails').value) || 0,
    normal_enabled: $('normal_enabled').checked,
    files_per_minute: parseInt($('fpm').value) || 0,
    normal_min_mb: parseInt($('nmin').value) || 1,
    normal_max_mb: parseInt($('nmax').value) || 1,
    normal_content_type: $('ncontent').value || 'binary',
    normal_users_csv: getCsvRaw('normal_users'),
    large_enabled: $('large_enabled').checked,
    large_min: parseInt($('lmin').value) || 1,
    large_max: parseInt($('lmax').value) || 1,
    large_unit: $('lunit').value || 'MB',
    interval_minutes: parseInt($('interval').value) || 0,
    large_users_csv: getCsvRaw('large_users'),
    download_enabled: $('download_enabled').checked,
    download_folder: $('dfolder').value.trim(),
    download_parallel_streams: parseInt($('dparallel').value) || 1,
    download_match_mode: getDownloadMatchMode(),
    download_users_csv: getCsvRaw('download_users'),
    private_key_pem: privateKeyPEM,
    private_key_passphrase: privateKeyPass,
  };
}

// getDownloadMatchMode reads the round-trip-tracking radio. Defaults to
// "trackid" so a missing radio (older imports / DOM hiccup) preserves
// today's behaviour rather than silently flipping to filename mode.
function getDownloadMatchMode() {
  const checked = document.querySelector('input[name="download_match_mode"]:checked');
  return checked ? checked.value : 'trackid';
}

function setDownloadMatchMode(v) {
  const id = v === 'filename' ? 'dmm_filename' : 'dmm_trackid';
  const el = document.getElementById(id);
  if (el) el.checked = true;
  // v0.14.18 — also reflect into the segmented control. Radios are
  // hidden in DOM (kept for serializer back-compat); the segmented
  // buttons are what the operator actually clicks.
  document.querySelectorAll('[data-role="match-mode-picker"] button').forEach((b) => {
    b.setAttribute('aria-pressed', b.dataset.value === v ? 'true' : 'false');
  });
}

// Wire the segmented match-mode picker once the DOM is ready. Click
// flips the corresponding hidden radio (so getDownloadMatchMode reads
// the right value) AND updates aria-pressed via setDownloadMatchMode.
(function wireMatchModePicker() {
  function attach() {
    const picker = document.querySelector('[data-role="match-mode-picker"]');
    if (!picker || picker.dataset.wired) return;
    picker.dataset.wired = '1';
    picker.querySelectorAll('button').forEach((btn) => {
      btn.addEventListener('click', (ev) => {
        ev.preventDefault();
        setDownloadMatchMode(btn.dataset.value);
      });
    });
  }
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', attach);
  } else {
    attach();
  }
})();

// ---------- export / import full config ----------
// By default the exported JSON has password columns blanked out so configs
// can be shared/checked-in safely. The "Include passwords in export" checkbox
// opts in for cases where the operator deliberately wants the credentials in
// the file (e.g. for an automated runner). A second confirm() guards the
// non-default path.
function exportConfig() {
  const cfg = buildRequestBody();

  // v0.14.20 — round-trip fixes for previously-silent drops:
  //
  // 1. Private key PEM. buildRequestBody() gates the PEM on the
  //    disclosure being OPEN (correct for /api/start — a closed
  //    disclosure means the operator decided not to use key auth).
  //    For EXPORT we want the PEM round-tripped regardless of
  //    disclosure state, so the operator can close the disclosure
  //    for visual tidiness without losing the key on next save.
  //    Re-read the textarea directly here.
  const pkEl = document.getElementById('conn-private-key');
  const pkPassEl = document.getElementById('conn-private-key-passphrase');
  if (pkEl && pkEl.value.trim()) cfg.private_key_pem = pkEl.value;
  if (pkPassEl && pkPassEl.value) cfg.private_key_passphrase = pkPassEl.value;

  // 2. Target Test-connection credentials (conn-user / conn-pass).
  //    Single-user creds for the Test-connection probe — distinct
  //    from per-load CSVs. They never reach /api/start (run pulls
  //    users from CSVs), but they ARE valuable round-trip state so
  //    "load preset → click Test connection" works without re-typing.
  const cu = document.getElementById('conn-user');
  const cp = document.getElementById('conn-pass');
  if (cu && cu.value.trim()) cfg.target_username = cu.value.trim();
  if (cp && cp.value) cfg.target_password = cp.value;

  const includePwd = $('export_with_passwords') && $('export_with_passwords').checked;
  if (!includePwd) {
    cfg.normal_users_csv   = stripPasswordsFromCSV(cfg.normal_users_csv);
    cfg.large_users_csv    = stripPasswordsFromCSV(cfg.large_users_csv);
    cfg.download_users_csv = stripPasswordsFromCSV(cfg.download_users_csv);
    // Credentials get blanked the same way CSV passwords do — they
    // only ship when "include passwords" is explicitly on.
    cfg.private_key_pem = '';
    cfg.private_key_passphrase = '';
    cfg.target_password = '';
  } else if (!confirm('Export will include plaintext passwords. Continue?')) {
    return;
  }
  const payload = {
    version: 1,
    exported_at: new Date().toISOString(),
    passwords_included: !!includePwd,
    config: cfg,
  };
  const blob = new Blob([JSON.stringify(payload, null, 2)], { type: 'application/json' });
  const a = document.createElement('a');
  a.href = URL.createObjectURL(blob);
  a.download = includePwd ? 'sftp-loadtest-config-with-passwords.json' : 'sftp-loadtest-config.json';
  a.click();
  setTimeout(() => URL.revokeObjectURL(a.href), 0);
}

// Maps a config key from the JSON payload back onto the right input+type.
// Null-safe: missing keys leave the current value untouched.
function importConfigPayload(cfg) {
  const strSet = (id, v) => {
    const el = $(id);
    if (el && v !== undefined && v !== null) {
      el.value = String(v);
      // Fire input + change so anything wired to this field (the
      // configure-redesign mirrors, the saved-config dirty flag, the
      // protocol/TLS-mode auto-port-snap) sees the update. Without
      // these, a strSet-only assignment is invisible to listeners and
      // a future field could silently desync from its mirror.
      el.dispatchEvent(new Event('input', { bubbles: true }));
      el.dispatchEvent(new Event('change', { bubbles: true }));
    }
  };
  const chkSet = (id, v) => {
    const el = $(id);
    if (el && v !== undefined && v !== null) {
      el.checked = !!v;
      el.dispatchEvent(new Event('change'));
    }
  };
  // Multi-protocol restore (v0.13.0). Fall back to "sftp" so configs
  // saved before the upgrade load identically to today.
  const proto = (cfg.protocol || 'sftp').toLowerCase();
  if (typeof window !== 'undefined' && typeof window.__sftplSetProtocol === 'function') {
    window.__sftplSetProtocol(proto);
  }
  if (cfg.tls_mode && typeof window !== 'undefined' && typeof window.__sftplSetTLSMode === 'function') {
    window.__sftplSetTLSMode(cfg.tls_mode);
  }
  const tlsSkip = document.getElementById('tls_skip_verify');
  if (tlsSkip) tlsSkip.checked = !!cfg.tls_insecure_skip_verify;
  // Restore TOFU on the unified toggle. Configs saved before v0.13.12
  // didn't have tls_trust_on_first_use; default the toggle ON so a
  // load-then-run against a new FTPS server still pins the cert.
  const tofuToggle = document.querySelector('[data-role="tofu"]');
  if (tofuToggle) tofuToggle.checked = cfg.tls_trust_on_first_use !== false;
  const tlsServer = document.getElementById('tls_server_name');
  if (tlsServer) tlsServer.value = cfg.tls_server_name || '';
  strSet('host', cfg.host);
  strSet('port', cfg.port);
  strSet('folder', cfg.upload_folder);
  // Sync the Quick Checks visible inputs so the protocol picker doesn't
  // silently leave a stale port behind. strSet alone targets the legacy
  // #port hidden input; without an explicit change event the QC mirror
  // doesn't pick up the new value.
  if (cfg.host !== undefined) {
    const ch = document.getElementById('conn-host');
    if (ch) { ch.value = String(cfg.host || ''); ch.dispatchEvent(new Event('change', { bubbles: true })); }
  }
  if (cfg.port !== undefined) {
    const cp = document.getElementById('conn-port');
    if (cp) { cp.value = String(cfg.port || ''); cp.dispatchEvent(new Event('change', { bubbles: true })); }
  }
  if (cfg.upload_folder !== undefined) {
    const cf = document.getElementById('conn-folder');
    if (cf) { cf.value = String(cfg.upload_folder || ''); cf.dispatchEvent(new Event('change', { bubbles: true })); }
  }
  strSet('parallel', cfg.parallel_streams);
  strSet('duration', cfg.duration_hours);
  strSet('poll', cfg.poll_seconds);
  if (cfg.track_id_timeout_seconds !== undefined) {
    $('timeout_min').value = Math.max(1, Math.round(cfg.track_id_timeout_seconds / 60));
  }
  strSet('max_fails', cfg.max_consecutive_failures);
  chkSet('normal_enabled', cfg.normal_enabled);
  strSet('fpm', cfg.files_per_minute);
  strSet('nmin', cfg.normal_min_mb);
  strSet('nmax', cfg.normal_max_mb);
  strSet('ncontent', cfg.normal_content_type);
  if (cfg.normal_users_csv !== undefined) setCsvRaw('normal_users', cfg.normal_users_csv);
  chkSet('large_enabled', cfg.large_enabled);
  strSet('lmin', cfg.large_min);
  strSet('lmax', cfg.large_max);
  strSet('lunit', cfg.large_unit);
  strSet('interval', cfg.interval_minutes);
  if (cfg.large_users_csv !== undefined) setCsvRaw('large_users', cfg.large_users_csv);
  chkSet('download_enabled', cfg.download_enabled);
  strSet('dfolder', cfg.download_folder);
  strSet('dparallel', cfg.download_parallel_streams);
  if (cfg.download_match_mode !== undefined) setDownloadMatchMode(cfg.download_match_mode);
  if (cfg.download_users_csv !== undefined) setCsvRaw('download_users', cfg.download_users_csv);
  // Private-key import — only meaningful when the export carried passwords.
  // Open the disclosure when a PEM is present so the operator immediately
  // sees the key was loaded; leave it closed when the field is blank.
  if (cfg.private_key_pem !== undefined) {
    const pkEl = document.getElementById('conn-private-key');
    if (pkEl) pkEl.value = String(cfg.private_key_pem || '');
    const disclosure = document.querySelector('[data-role="key-disclosure"]');
    if (disclosure && cfg.private_key_pem) disclosure.open = true;
  }
  if (cfg.private_key_passphrase !== undefined) {
    const pkPassEl = document.getElementById('conn-private-key-passphrase');
    if (pkPassEl) pkPassEl.value = String(cfg.private_key_passphrase || '');
  }
  // v0.14.20 — restore target Test-connection creds round-tripped from
  // export. These are single-user probe creds (distinct from per-load
  // CSVs); the run pulls users from CSVs so these never affect run
  // behaviour, but operators expect them to survive Export → Import.
  if (cfg.target_username !== undefined) {
    const cu = document.getElementById('conn-user');
    if (cu) {
      cu.value = String(cfg.target_username || '');
      cu.dispatchEvent(new Event('input', { bubbles: true }));
    }
  }
  if (cfg.target_password !== undefined) {
    const cp = document.getElementById('conn-pass');
    if (cp) {
      cp.value = String(cfg.target_password || '');
      cp.dispatchEvent(new Event('input', { bubbles: true }));
    }
  }
  // v0.14 source/sink disclosures. window.__srcSink is wired by
  // sources-sinks.js after mount; guard so configs imported before the
  // module mounts don't blow up the rest of the restore.
  if (typeof window !== 'undefined' && window.__srcSink) {
    if (cfg.normal_source !== undefined) window.__srcSink.applySource('normal', cfg.normal_source);
    if (cfg.large_source  !== undefined) window.__srcSink.applySource('large',  cfg.large_source);
    if (cfg.download_sink !== undefined) window.__srcSink.applySink(cfg.download_sink);
  }
  saveConfig();
}

// Single file input, dual-purpose: "fill only" vs "fill + run immediately".
// The clicked button stashes its mode before opening the native picker.
let importMode = 'fill'; // 'fill' | 'run'
$('exportBtn').addEventListener('click', exportConfig);
$('importBtn').addEventListener('click', () => { importMode = 'fill'; $('importFile').click(); });
$('importRunBtn').addEventListener('click', () => { importMode = 'run'; $('importFile').click(); });
if ($('save_passwords')) $('save_passwords').addEventListener('change', (ev) => setSavePasswords(!!ev.target.checked));
if ($('clearCredsBtn')) $('clearCredsBtn').addEventListener('click', clearStoredCredentials);
$('importFile').addEventListener('change', async (ev) => {
  const file = ev.target.files && ev.target.files[0];
  if (!file) return;
  const wantRun = importMode === 'run';
  try {
    const text = await file.text();
    const parsed = JSON.parse(text);
    // Accept either "{config: {...}}" wrapper or a bare config object.
    const cfg = (parsed && typeof parsed === 'object' && parsed.config) ? parsed.config : parsed;
    importConfigPayload(cfg);
    $('err').textContent = '';
    if (wantRun) {
      // start() reads straight from the form (which we just populated), posts
      // /api/start, and lights up Stop/Download CSV like a normal manual run.
      await start();
    }
  } catch (e) {
    $('err').textContent = 'Import failed: ' + e.message;
  } finally {
    ev.target.value = ''; // allow re-importing the same file
    importMode = 'fill';
  }
});

// ---------- scheduling ----------
async function scheduleRun() {
  $('err').textContent = '';
  const runAt = $('sched_at').value;
  if (!runAt) { $('err').textContent = 'Pick a date & time to schedule.'; return; }
  // datetime-local is wall-clock in the BROWSER's timezone with no offset.
  // Convert to a fully-qualified UTC ISO so the server doesn't reinterpret
  // it in its own local TZ — critical when the UI runs on a laptop and the
  // tool runs on a server in a different timezone.
  const utcISO = new Date(runAt).toISOString();
  const body = { run_at: utcISO, note: $('sched_note').value.trim(), config: buildRequestBody() };
  try {
    const res = await apiFetch('/api/schedule', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    if (!res.ok) throw new Error(await res.text());
    $('sched_note').value = '';
    pollSchedules();
  } catch (e) { $('err').textContent = e.message; }
}
async function cancelSchedule(id) {
  try {
    await apiFetch('/api/schedule/cancel?id=' + encodeURIComponent(id), { method: 'POST' });
    pollSchedules();
  } catch {}
}
async function pollSchedules() {
  try {
    const { schedules } = await (await apiFetch('/api/schedules')).json();
    const tb = $('sched_body');
    tb.innerHTML = '';
    if (!schedules || schedules.length === 0) {
      tb.innerHTML = `<tr><td colspan="4" class="muted" style="text-align:center;padding:14px">No schedules pending.</td></tr>`;
      return;
    }
    schedules.forEach(s => {
      const tr = document.createElement('tr');
      const when = new Date(s.run_at).toLocaleString();
      tr.innerHTML = `<td title="${s.id}">${s.id}</td><td>${when}</td><td title="${s.note || ''}">${s.note || ''}</td><td><button class="ghost" data-cancel="${s.id}">Cancel</button></td>`;
      tb.appendChild(tr);
    });
    tb.querySelectorAll('[data-cancel]').forEach(b => b.addEventListener('click', () => cancelSchedule(b.dataset.cancel)));
  } catch {}
}
$('scheduleBtn').addEventListener('click', scheduleRun);

// Pretty-print a link rate. /sys/class/net/<n>/speed gives megabits/sec.
function formatLink(mbps) {
  if (!mbps) return '';
  return mbps >= 1000 ? `${(mbps/1000).toFixed(1)} Gbps` : `${mbps} Mbps`;
}
// Pretty-print observed throughput as Mbps so it lines up with how NICs
// and ISPs are rated. Internal value is MB/s; ×8 to get megabits/sec.
function formatThroughput(mbps_bytes) {
  return `${(mbps_bytes * 8).toFixed(0)} Mbps`;
}

// ---------- one-shot host capacity snapshot ----------
// Fetched at page load. Refreshed every 60s so testers see fdlimit / RAM
// / NIC info change if their environment shifts (rare, but cheap to do).
let hostFDLimit = 0; // remembered for the live FD chip in the proc badge
async function loadHost() {
  try {
    const h = await (await apiFetch('/api/host')).json();
    $('h_host').textContent = h.hostname || '—';
    $('h_os').textContent   = `${h.os || '?'}/${h.arch || '?'}`;
    $('h_cpu').textContent  = h.num_cpu || '—';
    $('h_ram').textContent  = h.total_ram_mb ? (h.total_ram_mb >= 1024 ? (h.total_ram_mb/1024).toFixed(1)+' GB' : h.total_ram_mb+' MB') : '—';
    // RLIM_INFINITY (max int64) on macOS means "no limit" — render as such
    // and treat as unlimited for the proc-badge pressure calc.
    const INF = 9223372036854775807;
    const fmtLim = v => (!v ? '—' : (v >= INF / 2 ? '∞' : v.toLocaleString()));
    if (h.fd_limit_soft) {
      hostFDLimit = (h.fd_limit_soft >= INF / 2) ? 0 : h.fd_limit_soft;
      $('h_fdlimit').textContent = `${fmtLim(h.fd_limit_soft)} / ${fmtLim(h.fd_limit_hard)}`;
    } else {
      $('h_fdlimit').textContent = '—';
    }
    // The Network cell is fed by the live /api/status poll below — it shows
    // observed throughput, which is the only honest "bandwidth" answer for
    // this tool. Capture the link-speed hint (Linux only) so the live
    // formatter can show "245 Mbps observed of 1 Gbps link" when known.
    let bestLinkMbps = 0;
    for (const n of (h.interfaces || [])) {
      if (n.speed_mbps && n.speed_mbps > bestLinkMbps) bestLinkMbps = n.speed_mbps;
    }
    window.__linkMbps = bestLinkMbps;
    if (!window.__hadNet) {
      $('h_net').textContent = bestLinkMbps ? `${formatLink(bestLinkMbps)} link · idle` : 'idle';
    }
  } catch {}
}
loadHost();
setInterval(loadHost, 60000);

// Masthead clock/date — updated every 30s. Keeps the newspaper feel without
// re-rendering on every poll.
function paintMasthead() {
  const now = new Date();
  const fmt = now.toLocaleDateString(undefined, { weekday: 'long', year: 'numeric', month: 'long', day: 'numeric' });
  const hm  = now.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
  const dEl = $('m_date'), cEl = $('m_clock');
  if (dEl) dEl.textContent = fmt;
  if (cEl) cEl.textContent = hm;
}
paintMasthead();
setInterval(paintMasthead, 30000);

renderConnHistory();
setInterval(poll, 2000);
setInterval(pollRuns, 3000);
setInterval(pollSchedules, 5000);
poll();
pollRuns();
pollSchedules();

// Expose two of legacy.js's internal helpers so the new modules can
// snapshot/restore the form without re-implementing the legacy field
// vocabulary. Public is the wrong word — these are deliberate hooks
// into the legacy bridge layer, not a stable contract.
try {
  window.__sftplBuildRequestBody = buildRequestBody;
  window.__sftplImportConfigPayload = importConfigPayload;
} catch {}
