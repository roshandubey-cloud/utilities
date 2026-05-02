// masthead.js — top bar interactions. Renders the wordmark, the live status
// dot, the theme switcher, and the global "Run" CTA. Polls /healthz every 5 s
// for the active-run dot.

import { apiFetch } from './api.js';
import { getTheme, setTheme } from './theme.js';

const HEALTH_POLL_MS = 5000;

export function mountMasthead(rootSelector) {
  const root = document.querySelector(rootSelector);
  if (!root) return;

  // Wire theme segmented control.
  const seg = root.querySelector('[data-role="theme-switcher"]');
  if (seg) {
    const sync = () => {
      const cur = getTheme();
      seg.querySelectorAll('button[data-theme]').forEach((btn) => {
        btn.setAttribute('aria-pressed', String(btn.dataset.theme === cur));
      });
    };
    seg.addEventListener('click', (ev) => {
      const btn = ev.target.closest('button[data-theme]');
      if (!btn) return;
      setTheme(btn.dataset.theme);
      sync();
    });
    sync();
  }

  // Health-poll loop for the run status dot.
  const dot = root.querySelector('[data-role="status-dot"]');
  const text = root.querySelector('[data-role="status-text"]');
  if (dot && text) {
    pollHealth(dot, text);
  }

  // Surface the platform version next to the wordmark AND in the
  // bottom status-bar cell. /api/version is unauthenticated +
  // Cache-Control:no-store, so an upgraded binary shows its real
  // version on the very next page load — no stale WebKit cache,
  // no auth probe. One fetch updates both pills.
  const ver = root.querySelector('[data-role="brand-version"]');
  // Status-bar cell lives outside the masthead root, so query the
  // document. Hidden until the response arrives so we never flash
  // a placeholder.
  const statusVer = document.querySelector('[data-role="status-version"]');
  if (ver || statusVer) {
    apiFetch('/api/version', { cache: 'no-store' })
      .then((r) => r.ok ? r.json() : null)
      .then((j) => {
        if (!j || !j.version) return;
        const label = 'v' + j.version;
        const tooltip = `Platform v${j.version} — started ${j.started_at || ''}`;
        if (ver) {
          ver.textContent = label;
          ver.hidden = false;
          ver.title = tooltip;
        }
        if (statusVer) {
          statusVer.textContent = label;
          statusVer.hidden = false;
          statusVer.title = tooltip;
        }
      })
      .catch(() => {});
  }
}

async function pollHealth(dot, text) {
  try {
    const res = await apiFetch('/healthz');
    if (res.ok) {
      const j = await res.json();
      const active = !!j.active_run;
      dot.dataset.state = active ? 'running' : 'idle';
      text.textContent = active ? 'Run in progress' : 'Idle';
      text.title = j.uptime_sec ? `Server uptime ${formatUptime(j.uptime_sec)}` : '';
    } else {
      dot.dataset.state = 'error';
      text.textContent = `HTTP ${res.status}`;
    }
  } catch (e) {
    dot.dataset.state = 'error';
    text.textContent = 'Disconnected';
  } finally {
    setTimeout(() => pollHealth(dot, text), HEALTH_POLL_MS);
  }
}

function formatUptime(sec) {
  if (sec < 60) return `${sec}s`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m`;
  return `${Math.floor(sec / 3600)}h ${Math.floor((sec % 3600) / 60)}m`;
}
