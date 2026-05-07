// saved-connections.js — user-curated named connection entries.
//
// Stores up to MAX_ENTRIES { id, name, host, port, username, password? }
// in localStorage under SAVED_KEY. Exposes a Save… button on the Quick
// Checks card that prompts for a name + opt-in to save the password
// (default OFF — explicit consent because localStorage is plain text).
//
// The sidebar's "Connections" section already renders the recent-probe
// list (host:port only). When saved entries exist they take priority
// in that section: clicking one fills host / port / username / password
// in the Quick Checks card. Recents are a fallback for the never-saved
// case.

import { form as formModal, confirm as confirmModal } from './modal.js';
import { pushToast } from './toast.js';

export const SAVED_KEY = 'sftp-loadtest-saved-conns-v1';
const MAX_ENTRIES = 50;

export function listSaved() {
  try { return JSON.parse(localStorage.getItem(SAVED_KEY) || '[]'); }
  catch { return []; }
}

export function saveEntry(entry) {
  if (!entry || !entry.name || !entry.host || !entry.port) return null;
  const list = listSaved();
  // Same name → update in place. Different name with same host:port:user
  // is allowed (some setups have multiple aliases for the same target).
  const idx = list.findIndex((e) => e.name === entry.name);
  const id = idx >= 0 ? list[idx].id : `c-${Date.now().toString(36)}`;
  const stored = {
    id,
    name: entry.name,
    host: entry.host,
    port: Number(entry.port),
    username: entry.username || '',
    password: entry.savePassword ? (entry.password || '') : '',
    has_password: !!entry.savePassword && !!entry.password,
    // Multi-protocol fields (v0.13.0). Optional — older entries without
    // these load identically to today as SFTP.
    protocol: entry.protocol || 'sftp',
    tls_mode: entry.tls_mode || '',
    tls_server_name: entry.tls_server_name || '',
    tls_insecure_skip_verify: !!entry.tls_insecure_skip_verify,
    saved_at: new Date().toISOString(),
  };
  if (idx >= 0) list[idx] = stored;
  else list.unshift(stored);
  while (list.length > MAX_ENTRIES) list.pop();
  try { localStorage.setItem(SAVED_KEY, JSON.stringify(list)); }
  catch (e) { console.warn('saved-connections: localStorage write failed', e); return null; }
  // Notify other tabs / live listeners.
  try { window.dispatchEvent(new StorageEvent('storage', { key: SAVED_KEY })); } catch { /* synthesised events not always allowed */ }
  return stored;
}

export function removeEntry(id) {
  const list = listSaved().filter((e) => e.id !== id);
  try { localStorage.setItem(SAVED_KEY, JSON.stringify(list)); }
  catch (e) { console.warn('saved-connections: localStorage write failed', e); }
  try { window.dispatchEvent(new StorageEvent('storage', { key: SAVED_KEY })); } catch { /* ignore */ }
}

// applyEntry fills the Quick Checks card inputs (host / port / user / pass)
// with the saved entry. Dispatches input + change events so dependent UI
// (validation, run-summary chip, recent-history) re-renders.
export function applyEntry(entry) {
  if (!entry) return;
  const set = (id, val) => {
    const el = document.getElementById(id);
    if (!el) return;
    el.value = val == null ? '' : String(val);
    el.dispatchEvent(new Event('input', { bubbles: true }));
    el.dispatchEvent(new Event('change', { bubbles: true }));
  };
  // Multi-protocol restore (v0.13.0). Apply BEFORE host/port so the port
  // default the picker would write doesn't overwrite the saved port.
  if (typeof window !== 'undefined' && typeof window.__sftplSetProtocol === 'function') {
    window.__sftplSetProtocol(entry.protocol || 'sftp');
  }
  if (entry.tls_mode && typeof window !== 'undefined' && typeof window.__sftplSetTLSMode === 'function') {
    window.__sftplSetTLSMode(entry.tls_mode);
  }
  const tlsSkip = document.getElementById('tls_skip_verify');
  if (tlsSkip) tlsSkip.checked = !!entry.tls_insecure_skip_verify;
  const tlsServer = document.getElementById('tls_server_name');
  if (tlsServer) tlsServer.value = entry.tls_server_name || '';
  set('conn-host', entry.host);
  set('conn-port', entry.port);
  set('conn-user', entry.username || '');
  set('conn-pass', entry.password || '');
  // Mirror to the legacy hidden inputs that legacy.js / runner read.
  set('host', entry.host);
  set('port', entry.port);
}

// Opens a modal to capture name + passwords-included opt-in. Reads the
// current connection card values, persists, and pushes a toast.
export async function promptSave() {
  const host = (document.getElementById('conn-host')?.value || '').trim();
  const port = (document.getElementById('conn-port')?.value || '').trim();
  const username = (document.getElementById('conn-user')?.value || '').trim();
  const password = document.getElementById('conn-pass')?.value || '';
  if (!host || !port) {
    pushToast('Fill host + port before saving.', 'warn');
    return null;
  }
  const fields = [
    { name: 'name',     label: 'Connection name', placeholder: `${host}:${port}`, value: '', required: true,
      hint: 'A friendly label for the sidebar.' },
  ];
  const out = await formModal({ title: 'Save connection', fields, submitLabel: 'Save' });
  if (!out) return null;
  // v0.19.17 — explicit confirm dialog when a password is present, instead
  // of asking the operator to type "yes" into a free-form text field.
  // Same intent (default = no save), clearer affordance, and matches the
  // export-with-passwords flow.
  let savePassword = false;
  if (password) {
    savePassword = await confirmModal({
      title: 'Save the password too?',
      message: `Storing this password keeps it in browser localStorage in plaintext on this machine.\n\nKeep "${out.name.trim() || `${host}:${port}`}" without the password unless you really want it saved here.`,
      okLabel: 'Save with password',
      cancelLabel: 'Save without password',
      danger: true,
    });
  }
  const entry = {
    name: out.name.trim() || `${host}:${port}`,
    host, port, username, password,
    savePassword,
    // Multi-protocol fields (v0.13.0).
    protocol: (document.getElementById('protocol')?.value || 'sftp'),
    tls_mode: (document.getElementById('tls_mode')?.value || ''),
    tls_server_name: (document.getElementById('tls_server_name')?.value || '').trim(),
    tls_insecure_skip_verify: !!document.getElementById('tls_skip_verify')?.checked,
  };
  const stored = saveEntry(entry);
  if (stored) {
    pushToast(stored.has_password
      ? `Saved "${stored.name}" with password.`
      : `Saved "${stored.name}".`, 'success');
  }
  return stored;
}

// promptDelete asks for confirmation before removing. Used by the
// sidebar's hover-x affordance.
export async function promptDelete(entry) {
  if (!entry) return false;
  const ok = await confirmModal({
    title: 'Forget this connection?',
    message: `Remove "${entry.name}" (${entry.host}:${entry.port}) from saved connections?`,
    okLabel: 'Forget',
    danger: true,
  });
  if (ok) {
    removeEntry(entry.id);
    pushToast(`Forgot "${entry.name}".`, 'success');
  }
  return ok;
}

// mountSavedConnections wires the Save… button on the Quick Checks card.
export function mountSavedConnections() {
  const btn = document.querySelector('[data-component="connection"] [data-role="save-conn"]');
  if (!btn || btn.dataset.savedConnsMounted) return;
  btn.dataset.savedConnsMounted = '1';
  btn.addEventListener('click', (ev) => {
    ev.preventDefault();
    promptSave();
  });
}
