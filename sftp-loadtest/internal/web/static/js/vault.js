// vault.js — UI surface for the OS-independent encrypted secret
// vault (internal/vault, /api/vault/*). Keeps the browser layer
// intentionally thin: the server owns plaintext, the browser only
// learns whether a vault exists, whether it's currently unlocked,
// and which refs are stored. Plaintext secrets never round-trip
// through the browser — `storeSecret` posts a value once and the
// server retains it; `getRefList` returns refs only.
//
// Public surface:
//   await vaultStatus()     → { exists, unlocked, count, updated, path }
//   await unlockVault({ allowCreate })  → boolean (true on success)
//   await lockVault()       → boolean
//   await storeSecret(ref, secret)  → boolean (auto-unlocks on demand)
//   await deleteSecret(ref) → boolean
//   await listRefs()        → string[]
//   await changeMasterPassphrase()  → boolean (prompts for new passphrase)
//
// Auto-unlock: storeSecret + listRefs use auto-unlock when status
// reports locked. Operator gets a single passphrase prompt; on
// success we proceed with the original action without forcing
// the operator to navigate to a separate "Vault" panel first.

import { apiFetch, apiJSON } from './api.js';
import { prompt as promptModal, confirm as confirmModal } from './modal.js';

const STATUS_URL = '/api/vault/status';

export async function vaultStatus() {
  try {
    return await apiJSON(STATUS_URL);
  } catch {
    // Server has no vault path configured (CLI run with no
    // -reports-dir, etc.) — treat as "vault unsupported" so the
    // UI hides every vault affordance instead of showing broken
    // buttons.
    return { exists: false, unlocked: false, path: '', count: 0, unsupported: true };
  }
}

// ensureUnlocked is the chokepoint for any action that needs the
// vault open. Order of operations:
//   1. Quick GET /status to learn current state.
//   2. If unlocked → return true.
//   3. If file missing AND allowCreate → prompt for "create new
//      vault" passphrase + confirmation.
//   4. If file present AND locked → prompt for the existing
//      passphrase.
// Returns true on success, false on cancel / error. The caller
// should surface a toast on false so the operator knows why their
// action stalled.
export async function ensureUnlocked({ allowCreate = false } = {}) {
  const s = await vaultStatus();
  if (s.unsupported) return false;
  if (s.unlocked) return true;

  if (!s.exists) {
    if (!allowCreate) return false;
    const pass = await promptModal({
      title: 'Create encrypted vault',
      label: 'Master passphrase (10+ characters recommended)',
      placeholder: 'A strong passphrase you will remember',
    });
    if (!pass) return false;
    if (pass.length < 8) {
      const ok = await confirmModal({
        title: 'Short passphrase',
        message: `${pass.length}-character passphrase is below the 8-character floor. Argon2id will hash it but a short passphrase is still guessable offline. Continue anyway?`,
        okLabel: 'Continue',
        cancelLabel: 'Pick a longer one',
        danger: true,
      });
      if (!ok) return false;
    }
    const confirm = await promptModal({
      title: 'Confirm master passphrase',
      label: 'Re-type to confirm — there is no recovery if you forget',
    });
    if (confirm !== pass) return false;
    const r = await apiFetch('/api/vault/unlock', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ passphrase: pass, create: true }),
    });
    return r.ok;
  }

  // File exists, locked — prompt for the existing passphrase.
  const pass = await promptModal({
    title: 'Unlock encrypted vault',
    label: 'Enter your master passphrase',
  });
  if (!pass) return false;
  const r = await apiFetch('/api/vault/unlock', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ passphrase: pass }),
  });
  if (r.status === 403) {
    // Wrong passphrase — let the caller decide whether to retry.
    await confirmModal({
      title: 'Wrong passphrase',
      message: 'The vault refused that passphrase. Try again?',
      okLabel: 'Try again',
      cancelLabel: 'Cancel',
    });
    return ensureUnlocked({ allowCreate });
  }
  return r.ok;
}

export async function unlockVault({ allowCreate = false } = {}) {
  return ensureUnlocked({ allowCreate });
}

export async function lockVault() {
  const r = await apiFetch('/api/vault/lock', { method: 'POST' });
  return r.ok;
}

export async function storeSecret(ref, secret) {
  if (!ref || !secret) return false;
  if (!(await ensureUnlocked({ allowCreate: true }))) return false;
  const r = await apiFetch('/api/vault/set', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ ref, secret }),
  });
  return r.ok;
}

// getSecret returns the plaintext for a stored ref, auto-unlocking
// the vault on demand. Used when the operator picks a saved
// connection / preset whose password lives in the vault and we
// need to populate the form input. Threat model matches the
// operator typing the password in directly — same loopback HTTP
// trust boundary inside Wails, same HTTPS one for remote workers.
export async function getSecret(ref) {
  if (!ref) return null;
  if (!(await ensureUnlocked())) return null;
  try {
    const r = await apiFetch('/api/vault/get', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ref }),
    });
    if (!r.ok) return null;
    const j = await r.json();
    return j.secret || null;
  } catch {
    return null;
  }
}

export async function deleteSecret(ref) {
  if (!ref) return false;
  if (!(await ensureUnlocked())) return false;
  const r = await apiFetch('/api/vault/delete', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ ref }),
  });
  return r.ok;
}

export async function listRefs() {
  if (!(await ensureUnlocked())) return [];
  try {
    const j = await apiJSON('/api/vault/list');
    return j.refs || [];
  } catch {
    return [];
  }
}

// scanMigrations / applyMigrations expose the bulk-move-plaintext-
// to-vault flow. The UI offers this on first unlock when the
// scan returns non-empty: "X plaintext credentials found in
// schedule files — move them to the encrypted vault?"
export async function scanMigrations() {
  try {
    const j = await apiJSON('/api/vault/migrate-scan');
    return j.candidates || [];
  } catch {
    return [];
  }
}

export async function applyMigrations() {
  if (!(await ensureUnlocked())) return { migrated: 0, failed: [] };
  try {
    const r = await apiFetch('/api/vault/migrate-apply', { method: 'POST' });
    if (!r.ok) return { migrated: 0, failed: [`HTTP ${r.status}`] };
    return await r.json();
  } catch (e) {
    return { migrated: 0, failed: [String(e)] };
  }
}

export async function changeMasterPassphrase() {
  if (!(await ensureUnlocked())) return false;
  const next = await promptModal({
    title: 'Rotate master passphrase',
    label: 'New passphrase',
  });
  if (!next) return false;
  const confirm = await promptModal({
    title: 'Confirm new passphrase',
    label: 'Re-type to confirm',
  });
  if (confirm !== next) return false;
  const r = await apiFetch('/api/vault/change-passphrase', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ new_passphrase: next }),
  });
  return r.ok;
}
