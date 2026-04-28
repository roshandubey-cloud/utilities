// theme.js — three-state theme controller (auto / light / dark).
//
// "auto" follows the OS preference; light/dark force a value via [data-theme]
// on <html>. Persisted to localStorage so the user's choice survives reloads
// and survives across SKUs (web-ui and desktop-app share the same storage if
// served from the same origin; desktop-app's wails AssetServer counts as one).

const KEY = 'sftp-loadtest-theme-v1';
const VALID = ['auto', 'light', 'dark'];

function read() {
  try {
    const v = localStorage.getItem(KEY);
    return VALID.includes(v) ? v : 'auto';
  } catch {
    return 'auto';
  }
}

function write(v) {
  try { localStorage.setItem(KEY, v); } catch {}
}

function apply(value) {
  const root = document.documentElement;
  if (value === 'auto') {
    root.removeAttribute('data-theme');
  } else {
    root.setAttribute('data-theme', value);
  }
}

export function initTheme() {
  // URL param override (mostly for screenshots/QA): ?theme=light|dark|auto.
  // Persists into localStorage so subsequent reloads keep the override.
  try {
    const params = new URLSearchParams(window.location.search);
    const t = params.get('theme');
    if (t && VALID.includes(t)) {
      write(t);
    }
  } catch {}
  apply(read());
}

export function setTheme(value) {
  if (!VALID.includes(value)) value = 'auto';
  write(value);
  apply(value);
  document.dispatchEvent(new CustomEvent('theme-change', { detail: { value } }));
}

export function getTheme() {
  return read();
}

// Reflect system-pref change while in "auto" so the UI tracks OS appearance toggles.
if (window.matchMedia) {
  const mql = window.matchMedia('(prefers-color-scheme: dark)');
  mql.addEventListener?.('change', () => {
    if (read() === 'auto') apply('auto');
  });
}
