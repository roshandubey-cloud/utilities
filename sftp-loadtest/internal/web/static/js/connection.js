// connection.js — Test Connection card behaviour.
//
// Replaces the inline probeConnection() handler. Models the probe as a staged
// pipeline (TCP → SSH+SFTP → folder list) with per-stage status, surfaces TOFU
// fingerprint capture, and ties into shared connection-history (host/port).

import { apiPostJSON, apiFetch } from './api.js';

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
      fp = `<div class="probe-fingerprint">Captured new host key for <strong>${escapeHTML(reply.captured_for_host || host)}</strong> · ${escapeHTML(reply.captured_fingerprint)}</div>`;
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

  // ---------- submit handler ----------
  async function probe() {
    const host = (hostEl.value || '').trim();
    const port = parseInt(portEl.value || '0', 10);
    if (!host || !port) {
      setError('Enter host and port first.', null);
      return;
    }
    submitEl.disabled = true;
    setTesting();
    try {
      const body = { host, port };
      if (userEl.value) body.username = userEl.value;
      if (passEl.value) body.password = passEl.value;
      if (folderEl.value) body.folder = folderEl.value.trim();
      if (tofuEl.checked) body.trust_on_first_use = true;

      const reply = await apiPostJSON('/api/probe', body);
      if (reply.ok) {
        setOk(reply, host);
        rememberConn(host, port);
      } else {
        setError(reply.error || 'unknown error', reply.stage);
      }
    } catch (e) {
      setError(e.message || String(e), null);
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

function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}
