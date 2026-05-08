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
    // v0.20.0 — when password lives in the encrypted vault, the
    // entry only carries the ref; the plaintext stays server-side.
    // has_password reflects EITHER kind of stored credential so
    // sidebar UI doesn't have to branch.
    vault_ref: entry.vaultRef || '',
    // Multi-protocol fields (v0.13.0). Optional — older entries without
    // these load identically to today as SFTP.
    protocol: entry.protocol || 'sftp',
    tls_mode: entry.tls_mode || '',
    tls_server_name: entry.tls_server_name || '',
    tls_insecure_skip_verify: !!entry.tls_insecure_skip_verify,
    saved_at: new Date().toISOString(),
  };
  if (stored.vault_ref) stored.has_password = true;
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
//
// v0.20.0 — when the saved entry has `vault_ref`, fetch the plaintext
// from the encrypted vault (auto-unlocks via UI prompt) and write it
// into the password field. Falls through to whatever is in
// entry.password when the vault fetch fails (locked + cancelled,
// ref deleted from vault since save, server unreachable) so the
// connection is still usable; the operator just re-types.
export async function applyEntry(entry) {
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

  let password = entry.password || '';
  if (!password && entry.vault_ref) {
    const { getSecret } = await import('./vault.js');
    const fromVault = await getSecret(entry.vault_ref);
    if (fromVault) password = fromVault;
  }
  set('conn-pass', password);
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
  // v0.20.0 — when a password is present, offer THREE options (was
  // a binary "store in browser yes/no" since v0.19.17):
  //   1. Save without password (safest; default)
  //   2. Store in encrypted vault (server-side, OS-independent)
  //   3. Store in browser localStorage (plaintext, legacy)
  // Branch order is deliberate: vault is the recommended path so
  // it gets the primary button. The localStorage option remains
  // for offline / no-server scenarios but is clearly labeled as
  // plaintext.
  let savePassword = false;
  let storeInVault = false;
  if (password) {
    const choice = await formModal({
      title: 'Where should this password live?',
      submitLabel: 'Save connection',
      fields: [
        {
          name: 'where',
          label: 'Password storage',
          type: 'select',
          options: [
            { value: 'none',  label: 'Save without password (you re-enter on each use)' },
            { value: 'vault', label: 'Encrypted vault (recommended — Argon2id + ChaCha20-Poly1305)' },
            { value: 'local', label: 'Browser localStorage (plaintext on this machine)' },
          ],
          value: 'none',
        },
      ],
    });
    if (choice) {
      if (choice.where === 'vault') storeInVault = true;
      else if (choice.where === 'local') savePassword = true;
    }
  }

  const entry = {
    name: out.name.trim() || `${host}:${port}`,
    host, port, username, password,
    savePassword, // localStorage path
    storeInVault, // server vault path
    // Multi-protocol fields (v0.13.0).
    protocol: (document.getElementById('protocol')?.value || 'sftp'),
    tls_mode: (document.getElementById('tls_mode')?.value || ''),
    tls_server_name: (document.getElementById('tls_server_name')?.value || '').trim(),
    tls_insecure_skip_verify: !!document.getElementById('tls_skip_verify')?.checked,
  };

  // Store in the server-side vault first (if elected). On vault
  // failure (locked + cancelled prompt, server unreachable, etc.)
  // fall back to saving the entry without a password reference so
  // the operator can complete the save and revisit the password
  // choice later.
  if (storeInVault) {
    const { storeSecret } = await import('./vault.js');
    const ref = `connection:${entry.name}/password`;
    const ok = await storeSecret(ref, password);
    if (ok) {
      entry.vaultRef = ref;
      entry.password = ''; // never persist plaintext alongside a vault ref
    } else {
      pushToast('Vault save failed; connection saved without password.', 'warn');
      entry.storeInVault = false;
    }
  }

  const stored = saveEntry(entry);
  if (stored) {
    const where = entry.storeInVault ? ' (password in vault)'
      : stored.has_password ? ' (password in browser)'
      : '';
    pushToast(`Saved "${stored.name}"${where}.`, 'success');
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
