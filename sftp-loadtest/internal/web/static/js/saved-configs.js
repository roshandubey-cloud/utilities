// saved-configs.js — α4 of the v0.9.0 redesign.
//
// Operators run dozens of distinct workloads (smoke / soak / prod / staging)
// and the existing UX requires re-typing the form on every switch. This
// module adds named presets backed by localStorage:
//
//   list()           → [{ id, name, savedAt, config }]
//   save(name)       → snapshot the current legacy form, store under name
//   load(id)         → replay the snapshot through the existing import path
//   remove(id)       → delete a preset
//   exportPreset(id) → download a single preset as JSON
//
// Used by the sidebar (α5) and the command palette. Storage key namespace
// matches the existing config-persistence prefix so a future migration is
// easy — these are independent blobs from the autosave config.

const KEY = 'sftp-loadtest-saved-configs-v1';

function readAll() {
  try {
    const raw = localStorage.getItem(KEY);
    if (!raw) return [];
    const arr = JSON.parse(raw);
    return Array.isArray(arr) ? arr : [];
  } catch {
    return [];
  }
}

function writeAll(arr) {
  try { localStorage.setItem(KEY, JSON.stringify(arr)); } catch {}
}

function newID() {
  return 'cfg-' + Math.random().toString(36).slice(2, 10) + '-' + Date.now().toString(36);
}

export function list() {
  return readAll().map((entry) => ({ ...entry })); // defensive copy
}

export function save(name) {
  if (!name || !name.trim()) return null;
  // Snapshot the current form by calling buildRequestBody(), the same
  // function /api/start uses. We read it via a global hook the legacy
  // module doesn't export — fall back to the saved-config blob in
  // localStorage which is kept in sync by saveConfig() on every change.
  let cfg;
  if (typeof window !== 'undefined' && typeof window.__sftplBuildRequestBody === 'function') {
    cfg = window.__sftplBuildRequestBody();
  } else {
    try {
      cfg = JSON.parse(localStorage.getItem('sftp-loadtest-config-v1') || '{}');
    } catch { cfg = {}; }
  }
  // Strip any password remnants — saved presets are never a password vault.
  cfg = stripPasswords(cfg);

  const arr = readAll();
  const existing = arr.findIndex((p) => p.name === name.trim());
  const entry = {
    id: existing >= 0 ? arr[existing].id : newID(),
    name: name.trim(),
    savedAt: new Date().toISOString(),
    config: cfg,
  };
  if (existing >= 0) arr[existing] = entry;
  else arr.push(entry);
  writeAll(arr);
  // Notify the sidebar (and any other live listeners) without waiting
  // for the 3 s heartbeat — same pattern the saved-connections module
  // uses. `storage` events don't auto-fire for same-window writes,
  // so synthesise one.
  try { window.dispatchEvent(new StorageEvent('storage', { key: KEY })); } catch { /* ignore */ }
  return entry;
}

export function load(id) {
  const entry = readAll().find((p) => p.id === id);
  if (!entry) return false;
  // Reuse the legacy importConfigPayload — same path the file-upload
  // import takes, so every existing field is restored consistently.
  if (typeof window !== 'undefined' && typeof window.__sftplImportConfigPayload === 'function') {
    window.__sftplImportConfigPayload(entry.config);
    return true;
  }
  return false;
}

export function remove(id) {
  const arr = readAll().filter((p) => p.id !== id);
  writeAll(arr);
  try { window.dispatchEvent(new StorageEvent('storage', { key: KEY })); } catch { /* ignore */ }
}

export function exportPreset(id) {
  const entry = readAll().find((p) => p.id === id);
  if (!entry) return;
  const blob = new Blob([JSON.stringify({
    version: 1,
    exported_at: new Date().toISOString(),
    passwords_included: false,
    config: entry.config,
  }, null, 2)], { type: 'application/json' });
  const a = document.createElement('a');
  a.href = URL.createObjectURL(blob);
  a.download = `preset-${entry.name.replace(/[^a-z0-9]+/gi, '-').toLowerCase()}.json`;
  a.click();
  setTimeout(() => URL.revokeObjectURL(a.href), 1000);
}

function stripPasswords(cfg) {
  const out = { ...cfg };
  for (const k of ['normal_users_csv', 'large_users_csv', 'download_users_csv']) {
    if (typeof out[k] === 'string') {
      out[k] = out[k].split('\n').map((line) => {
        if (!line.trim()) return line;
        const cols = line.split(',');
        if (cols.length >= 2) cols[1] = '';
        return cols.join(',');
      }).join('\n');
    }
  }
  return out;
}
