// connection.js — Test Connection card behaviour.
//
// Replaces the inline probeConnection() handler. Models the probe as a staged
// pipeline (TCP → SSH+SFTP → folder list) with per-stage status, surfaces TOFU
// fingerprint capture, and ties into shared connection-history (host/port).

import { apiPostJSON, apiFetch } from './api.js';
import { pushToast } from './toast.js';
import { hostKeyConsent } from './modal.js';

const HISTORY_KEY = 'sftp-loadtest-conn-history-v1';
const HISTORY_MAX = 8;

export function mountConnectionCard(rootSelector) {
  const root = document.querySelector(rootSelector);
  if (!root) return;

  const $ = (sel) => root.querySelector(sel);

  // ---------- elements ----------
  const hostEl = $('[data-role="host"]');
  const portEl = $('[data-role="port"]');
  const userEl = $('[data-role="username"]');
  const passEl = $('[data-role="password"]');
  const folderEl = $('[data-role="folder"]');
  const tofuEl = $('[data-role="tofu"]');
  const submitEl = $('[data-role="submit"]');
  const resetEl = $('[data-role="reset"]');
  const resultEl = $('[data-role="result"]');
  const recentEl = $('[data-role="recent"]');
  const fpEl = $('[data-role="fingerprint"]');
  const keyDisclosureEl = $('[data-role="key-disclosure"]');
  const privateKeyEl = $('[data-role="private-key"]');
  const privateKeyPassEl = $('[data-role="private-key-passphrase"]');
  // Multi-protocol additions (v0.13.0). The picker drives port defaults
  // and reveals/hides FTPS-only fields; the value is mirrored into the
  // hidden #protocol input so legacy.buildRequestBody() can read it
  // without having to know about the segmented control.
  const protoPickerEl = $('[data-role="protocol-picker"]');
  const protoValueEl  = $('[data-role="protocol-value"]');
  const ftpsFieldsEl  = $('[data-role="ftps-fields"]');
  const tlsModeEl     = $('[data-role="tls-mode-picker"]');
  const tlsModeValEl  = $('[data-role="tls-mode-value"]');
  const tlsSkipEl     = $('[data-role="tls-skip-verify"]');
  const tlsServerEl   = $('[data-role="tls-server-name"]');

  // Default ports per protocol/TLS-mode so flipping the picker doesn't
  // leave the operator on a stale 22 against an FTP server.
  function defaultPortFor(proto, tlsMode) {
    if (proto === 'ftps' && tlsMode === 'implicit') return 990;
    if (proto === 'ftp' || proto === 'ftps') return 21;
    return 22;
  }
  // userEditedPort — once the operator types a non-default port we stop
  // overwriting it on protocol switches. Reset by a Reset click.
  let userEditedPort = false;
  if (portEl) {
    portEl.addEventListener('input', () => { userEditedPort = true; });
  }

  function getProtocol() { return (protoValueEl && protoValueEl.value) || 'sftp'; }
  function getTLSMode()  { return (tlsModeValEl && tlsModeValEl.value) || 'explicit'; }

  function syncProtocolUI() {
    const proto = getProtocol();
    if (ftpsFieldsEl) ftpsFieldsEl.hidden = proto !== 'ftps';
    // Hide the SSH key disclosure for FTP/FTPS — keys aren't an SSH-only
    // concept but the load tester only supports SSH key auth today.
    if (keyDisclosureEl) keyDisclosureEl.style.display = proto === 'sftp' ? '' : 'none';
    // The TOFU label talks about SSH host keys — surface a different hint
    // for FTPS so it doesn't confuse the operator.
    const tofuTextEl = root.querySelector('[data-role="tofu"] + .toggle-track + .toggle-text, [data-role="tofu"] ~ .toggle-text');
    if (tofuTextEl) {
      tofuTextEl.textContent = proto === 'ftps'
        ? 'Trust this server cert on first connect (TOFU)'
        : proto === 'sftp'
          ? 'Auto-add server key on first connect (TOFU)'
          : 'TOFU not applicable for plain FTP';
    }
    if (proto !== 'sftp' && proto !== 'ftps' && tofuEl) tofuEl.checked = false;
    // Snap the port to the protocol default unless the operator has
    // explicitly typed something. Keep their value otherwise.
    if (portEl && !userEditedPort) {
      portEl.value = String(defaultPortFor(proto, getTLSMode()));
      portEl.dispatchEvent(new Event('input', { bubbles: true }));
      portEl.dispatchEvent(new Event('change', { bubbles: true }));
    }
  }

  function setProtocol(proto) {
    if (!['sftp', 'ftp', 'ftps'].includes(proto)) proto = 'sftp';
    if (protoValueEl) {
      protoValueEl.value = proto;
      protoValueEl.dispatchEvent(new Event('input', { bubbles: true }));
      protoValueEl.dispatchEvent(new Event('change', { bubbles: true }));
    }
    if (protoPickerEl) {
      protoPickerEl.querySelectorAll('button').forEach((b) => {
        b.setAttribute('aria-pressed', b.dataset.value === proto ? 'true' : 'false');
      });
    }
    syncProtocolUI();
    // Notify any listeners (configure-redesign chip, saved-configs).
    document.dispatchEvent(new CustomEvent('sftpl:protocol-change', { detail: { protocol: proto } }));
  }

  function setTLSMode(mode) {
    if (!['explicit', 'implicit'].includes(mode)) mode = 'explicit';
    if (tlsModeValEl) tlsModeValEl.value = mode;
    if (tlsModeEl) {
      tlsModeEl.querySelectorAll('button').forEach((b) => {
        b.setAttribute('aria-pressed', b.dataset.value === mode ? 'true' : 'false');
      });
    }
    syncProtocolUI();
  }

  if (protoPickerEl) {
    protoPickerEl.querySelectorAll('button').forEach((btn) => {
      btn.addEventListener('click', (ev) => {
        ev.preventDefault();
        userEditedPort = false; // explicit protocol switch resets the override
        setProtocol(btn.dataset.value);
      });
    });
  }
  if (tlsModeEl) {
    tlsModeEl.querySelectorAll('button').forEach((btn) => {
      btn.addEventListener('click', (ev) => {
        ev.preventDefault();
        userEditedPort = false;
        setTLSMode(btn.dataset.value);
      });
    });
  }

  // Expose for legacy.js / saved-configs to call when restoring a config.
  window.__sftplSetProtocol = setProtocol;
  window.__sftplSetTLSMode  = setTLSMode;
  window.__sftplGetProtocol = getProtocol;
  window.__sftplGetTLSMode  = getTLSMode;

  // Initial paint.
  syncProtocolUI();

  // ---------- recent connections ----------
  function readHistory() {
    try { return JSON.parse(localStorage.getItem(HISTORY_KEY) || '[]'); } catch { return []; }
  }
  function writeHistory(list) {
    try { localStorage.setItem(HISTORY_KEY, JSON.stringify(list.slice(0, HISTORY_MAX))); } catch {}
  }
  function rememberConn(host, port) {
    if (!host) return;
    const key = `${host}:${port}`;
    const list = readHistory().filter((e) => `${e.host}:${e.port}` !== key);
    list.unshift({ host, port });
    writeHistory(list);
    renderRecent();
  }
  function renderRecent() {
    if (!recentEl) return;
    const list = readHistory();
    recentEl.innerHTML = '';
    if (list.length === 0) {
      recentEl.innerHTML = '<span class="help">No recent connections — your last 8 will appear here.</span>';
      return;
    }
    const label = document.createElement('span');
    label.className = 'help';
    label.textContent = 'Recent:';
    recentEl.appendChild(label);
    list.forEach((e) => {
      const btn = document.createElement('button');
      btn.type = 'button';
      btn.className = 'btn btn-sm btn-ghost';
      btn.textContent = `${e.host}:${e.port}`;
      btn.addEventListener('click', () => {
        hostEl.value = e.host;
        portEl.value = e.port;
        hostEl.dispatchEvent(new Event('change', { bubbles: true }));
      });
      recentEl.appendChild(btn);
    });
  }
  renderRecent();

  // ---------- result rendering ----------
  function setIdle() {
    resultEl.dataset.state = 'idle';
    resultEl.innerHTML = '';
  }
  function setTesting() {
    resultEl.dataset.state = 'testing';
    resultEl.innerHTML = `
      <div class="probe-headline"><span class="spinner" aria-hidden="true"></span><span>Testing connection…</span></div>
      <div class="probe-stages">
        <span class="probe-stage" data-status="pending">tcp</span>
        <span class="probe-stage" data-status="pending">ssh+sftp</span>
        <span class="probe-stage" data-status="pending">list</span>
      </div>`;
  }
  function setConsent(reply, host, port) {
    resultEl.dataset.state = 'consent';
    const fp = reply.captured_fingerprint || '(unavailable)';
    const target = reply.captured_for_host || host;
    resultEl.innerHTML = `
      <div class="probe-headline">${iconShield()}<span>New host key — your decision needed</span></div>
      <div class="help" style="white-space:normal">
        The SFTP server at <strong class="mono">${escapeHTML(target)}:${port}</strong> presented a host
        key that's not in <code>known_hosts</code> yet. This is normal the first time you connect to a
        new server — but always confirm the fingerprint matches one given to you out-of-band.
      </div>
      <div class="probe-fingerprint">
        <div class="eyebrow">SHA-256 fingerprint</div>
        <div>${escapeHTML(fp)}</div>
      </div>
      <div class="row" style="justify-content:flex-end;flex-wrap:wrap;gap:var(--sp-2);padding-top:var(--sp-2)">
        <button class="btn btn-ghost"     type="button" data-role="consent-cancel">Cancel</button>
        <button class="btn btn-primary"   type="button" data-role="consent-accept">Accept and connect</button>
      </div>`;
    resultEl.querySelector('[data-role="consent-accept"]').addEventListener('click', (ev) => {
      ev.preventDefault();
      probe(true);   // re-probe with TOFU=true so the server appends + accepts
    });
    resultEl.querySelector('[data-role="consent-cancel"]').addEventListener('click', (ev) => {
      ev.preventDefault();
      setIdle();
    });
  }
  // setRenewal — the trust store already has a key for this host:port and
  // the server presented a DIFFERENT one. Show both fingerprints with a
  // red Accept that re-probes with accept_changed=true, allowing the
  // operator to overwrite the stored key purely from the UI (no manual
  // known_hosts editing).
  function setRenewal(reply, host, port) {
    resultEl.dataset.state = 'renewal';
    const fpNew = reply.captured_fingerprint || '(unavailable)';
    const fpOld = reply.captured_previous_fingerprint || '(unavailable)';
    const target = reply.captured_for_host || host;
    resultEl.innerHTML = `
      <div class="probe-headline">${iconAlert()}<span>Host key has CHANGED</span></div>
      <div class="help" style="white-space:normal">
        The SFTP server at <strong class="mono">${escapeHTML(target)}:${port}</strong> presented a host key
        <strong>different</strong> from the one previously trusted. This can mean a legitimate key rotation —
        or a man-in-the-middle attack. Verify the new fingerprint out-of-band before accepting.
      </div>
      <div class="probe-fingerprint" data-variant="renewal">
        <div class="eyebrow">Previously trusted</div>
        <div data-role="fp-old">${escapeHTML(fpOld)}</div>
        <div class="eyebrow" style="margin-top:var(--sp-2)">Newly presented</div>
        <div data-role="fp-new">${escapeHTML(fpNew)}</div>
      </div>
      <div class="row" style="justify-content:flex-end;flex-wrap:wrap;gap:var(--sp-2);padding-top:var(--sp-2)">
        <button class="btn btn-ghost"  type="button" data-role="renewal-cancel">Cancel</button>
        <button class="btn btn-danger" type="button" data-role="renewal-accept">Accept the new key</button>
      </div>`;
    resultEl.querySelector('[data-role="renewal-accept"]').addEventListener('click', async (ev) => {
      ev.preventDefault();
      submitEl.disabled = true;
      try {
        const body = { host, port, trust_on_first_use: true, accept_changed: true };
        if (userEl.value) body.username = userEl.value;
        if (passEl.value) body.password = passEl.value;
        if (folderEl.value) body.folder = folderEl.value.trim();
        const proto = getProtocol();
        body.protocol = proto;
        if (proto === 'ftps') {
          body.tls_mode = getTLSMode();
          if (tlsSkipEl && tlsSkipEl.checked) body.tls_insecure_skip_verify = true;
          if (tlsServerEl && tlsServerEl.value.trim()) body.tls_server_name = tlsServerEl.value.trim();
        }
        const renewed = await apiPostJSON('/api/probe', body);
        if (renewed.ok) {
          setOk(renewed, host);
          rememberConn(host, port);
          pushToast(`New host key trusted for ${host}:${port}`, 'success');
        } else {
          setError(renewed.error || 'failed to accept new key', renewed.stage);
          pushToast('Failed to accept new host key', 'error');
        }
      } catch (e) {
        setError(e.message || String(e), null);
      } finally {
        submitEl.disabled = false;
      }
    });
    resultEl.querySelector('[data-role="renewal-cancel"]').addEventListener('click', (ev) => {
      ev.preventDefault();
      setIdle();
    });
  }
  function setOk(reply, host) {
    resultEl.dataset.state = 'ok';
    const stages = [
      ['tcp',     reply.tcp_ms,      'tcp'],
      ['sshsftp', reply.ssh_sftp_ms, 'ssh+sftp'],
      ['list',    reply.list_ms,     'list'],
    ];
    const stagesHTML = stages.map(([_, ms, label]) => {
      if (ms === undefined) return `<span class="probe-stage" data-status="pending">${label} —</span>`;
      return `<span class="probe-stage" data-status="ok">${label} ${ms} ms</span>`;
    }).join('');
    let fp = '';
    if (reply.captured_fingerprint) {
      const label = reply.tls_fingerprint ? 'TLS certificate fingerprint' : 'Captured new host key';
      fp = `<div class="probe-fingerprint" data-role="captured-fingerprint">${escapeHTML(label)} for <strong>${escapeHTML(reply.captured_for_host || host)}</strong> · <span class="mono">${escapeHTML(reply.captured_fingerprint)}</span></div>`;
    }
    resultEl.innerHTML = `
      <div class="probe-headline">${iconCheck()}<span>Connection OK${reply.note ? ' — ' + escapeHTML(reply.note) : ''}</span></div>
      <div class="probe-stages">${stagesHTML}</div>
      ${fp}`;
  }
  function setError(msg, stage) {
    resultEl.dataset.state = 'error';
    resultEl.innerHTML = `
      <div class="probe-headline">${iconAlert()}<span>${escapeHTML(stage ? 'Failed at ' + stage : 'Probe failed')}</span></div>
      <div class="help" style="color:var(--danger-fg-soft);white-space:pre-wrap">${escapeHTML(msg || 'unknown error')}</div>`;
  }

  // ---------- inline validation ----------
  function validateField(el) {
    if (!el) return true;
    const field = el.closest('.field');
    let err = '';
    if (el === hostEl) {
      if (!el.value.trim()) err = 'Host is required.';
    } else if (el === portEl) {
      const p = parseInt(el.value || '0', 10);
      if (!p) err = 'Port is required.';
      else if (p < 1 || p > 65535) err = 'Port must be 1–65535.';
    }
    if (field) {
      field.dataset.invalid = err ? 'true' : 'false';
      let errEl = field.querySelector('.field-error');
      if (err) {
        if (!errEl) {
          errEl = document.createElement('div');
          errEl.className = 'field-error';
          errEl.innerHTML = '<span class="icon icon-xs" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="12" cy="12" r="9"/><path d="M12 8v5"/><path d="M12 16.5h.01"/></svg></span><span></span>';
          field.appendChild(errEl);
        }
        errEl.querySelector('span:last-child').textContent = err;
        errEl.hidden = false;
      } else if (errEl) {
        errEl.hidden = true;
      }
    }
    return !err;
  }
  [hostEl, portEl].forEach((el) => {
    if (!el) return;
    el.addEventListener('blur', () => validateField(el));
    el.addEventListener('input', () => {
      // Clear error state once the user starts typing again.
      const f = el.closest('.field');
      if (f && f.dataset.invalid === 'true') validateField(el);
    });
  });

  // ---------- submit handler ----------
  // forceTOFU=true is used by the consent prompt's Accept button: the user
  // has seen the fingerprint and explicitly opted in.
  async function probe(forceTOFU) {
    const hostOK = validateField(hostEl);
    const portOK = validateField(portEl);
    if (!hostOK || !portOK) {
      const firstInvalid = root.querySelector('.field[data-invalid="true"] .input');
      if (firstInvalid) firstInvalid.focus();
      return;
    }
    const host = (hostEl.value || '').trim();
    const port = parseInt(portEl.value || '0', 10);
    submitEl.disabled = true;
    setTesting();
    try {
      const body = { host, port };
      if (userEl.value) body.username = userEl.value;
      if (passEl.value) body.password = passEl.value;
      if (folderEl.value) body.folder = folderEl.value.trim();
      if (forceTOFU || (tofuEl && tofuEl.checked)) body.trust_on_first_use = true;
      // Multi-protocol fields. Always send the picker value (defaults to
      // "sftp"), and only attach TLS knobs when FTPS is selected so the
      // probe handler doesn't see noise on SFTP requests.
      const proto = getProtocol();
      body.protocol = proto;
      if (proto === 'ftps') {
        body.tls_mode = getTLSMode();
        if (tlsSkipEl && tlsSkipEl.checked) body.tls_insecure_skip_verify = true;
        if (tlsServerEl && tlsServerEl.value.trim()) body.tls_server_name = tlsServerEl.value.trim();
      }
      // Public-key auth: only attach when the disclosure is OPEN and the
      // PEM is non-empty. A closed disclosure with stale text in it must
      // not silently switch the probe to key auth.
      if (keyDisclosureEl && keyDisclosureEl.open && privateKeyEl && privateKeyEl.value.trim()) {
        body.private_key = privateKeyEl.value;
        if (privateKeyPassEl && privateKeyPassEl.value) body.passphrase = privateKeyPassEl.value;
      }

      const reply = await apiPostJSON('/api/probe', body);
      if (reply.ok) {
        setOk(reply, host);
        rememberConn(host, port);
        pushToast(`Connected to ${host}:${port}${reply.captured_fingerprint ? ' — host key added to known_hosts' : ''}`, 'success');
      } else if (reply.requires_consent) {
        setConsent(reply, host, port);
      } else if (reply.requires_renewal) {
        // Host key CHANGED — surface a high-friction modal showing both
        // fingerprints. On Accept, re-probe with accept_changed=true so
        // the trust store overwrites the old entry. Never edit the file
        // from the backend without explicit operator opt-in.
        setRenewal(reply, host, port);
      } else {
        setError(reply.error || 'unknown error', reply.stage);
        pushToast(`Connection failed${reply.stage ? ' at ' + reply.stage : ''}`, 'error');
      }
    } catch (e) {
      setError(e.message || String(e), null);
      pushToast(`Probe error: ${e.message || e}`, 'error');
    } finally {
      submitEl.disabled = false;
    }
  }

  submitEl.addEventListener('click', (ev) => { ev.preventDefault(); probe(); });
  if (resetEl) resetEl.addEventListener('click', (ev) => { ev.preventDefault(); resetForm(); });

  // Folder presets — clickable chip-style buttons that fill the Folder field.
  root.querySelectorAll('[data-role="folder-preset"]').forEach((btn) => {
    btn.addEventListener('click', (ev) => {
      ev.preventDefault();
      if (!folderEl) return;
      folderEl.value = btn.dataset.value || '';
      folderEl.focus();
      folderEl.dispatchEvent(new Event('change', { bubbles: true }));
    });
  });
  // Cmd/Ctrl+Enter inside any field submits.
  root.addEventListener('keydown', (ev) => {
    if ((ev.metaKey || ev.ctrlKey) && ev.key === 'Enter') { ev.preventDefault(); probe(); }
  });

  function resetForm() {
    [userEl, passEl, folderEl].forEach((el) => { if (el) el.value = ''; });
    if (tofuEl) tofuEl.checked = false;
    setIdle();
    hostEl.focus();
  }

  // Sync host/port/folder from Quick Checks into the LEGACY hidden Connection
  // card inputs (#host/#port/#folder). The legacy buildRequestBody() reads
  // from those — without this sync, typing into Quick Checks and clicking
  // Start Run would silently send empty host/port. Bidirectional so an
  // imported config (which writes to the legacy ids) reflects back into QC.
  setupLegacySync(hostEl,   'host');
  setupLegacySync(portEl,   'port');
  setupLegacySync(folderEl, 'folder');

  setIdle();
}

function setupLegacySync(qc, legacyId) {
  if (!qc) return;
  const leg = document.getElementById(legacyId);
  if (!leg) return;
  // Initial: prefer existing legacy value (e.g. from saved config / import).
  if (leg.value && !qc.value) {
    qc.value = leg.value;
  } else if (qc.value && !leg.value) {
    leg.value = qc.value;
    leg.dispatchEvent(new Event('change', { bubbles: true }));
  }
  // QC → legacy on every keystroke.
  qc.addEventListener('input', () => {
    if (leg.value !== qc.value) {
      leg.value = qc.value;
      leg.dispatchEvent(new Event('change', { bubbles: true }));
    }
  });
  // Legacy → QC when the legacy value is changed programmatically (import).
  leg.addEventListener('change', () => {
    if (qc.value !== leg.value) qc.value = leg.value;
  });
}

// ---------- inline icons ----------
function iconCheck() {
  return `<span class="icon icon-md" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="M5 12.5l5 5 9-11"/></svg></span>`;
}
function iconAlert() {
  return `<span class="icon icon-md" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="M12 8v5"/><path d="M12 16.5h.01"/><path d="M10.6 3.5l-8 14a1.6 1.6 0 0 0 1.4 2.5h16a1.6 1.6 0 0 0 1.4-2.5l-8-14a1.6 1.6 0 0 0-2.8 0z"/></svg></span>`;
}
function iconShield() {
  return `<span class="icon icon-md" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="M12 3l8 3v6c0 5-3.5 8-8 9-4.5-1-8-4-8-9V6l8-3z"/><path d="M9 12l2 2 4-4"/></svg></span>`;
}

function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}
