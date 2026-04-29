// shell.js — α2 of the v0.9.0 redesign.
//
// Builds the workbench grid (topbar / sidebar / main / statusbar) at runtime
// by wrapping the existing <body> content. The legacy markup, IDs, and
// data-component attributes are preserved verbatim — only the surrounding
// chrome changes. Tests that assert on #startBtn, [data-component=...] etc.
// keep passing.
//
// Topbar:    LED, brand, run controls (proxy to #startBtn / #stopBtn),
//            theme toggle (re-uses the existing data-role="theme-switcher"),
//            Cmd+K affordance (palette wiring lands in α4).
// Sidebar:   placeholder sections — Connections, Saved configs, Recent runs,
//            Trusted hosts. Filled in α5 from /api/runs, /api/hostkeys, and
//            localStorage.
// Statusbar: hostname/OS/cores/RAM/FD pulled from /api/host (same endpoint
//            the host-bar component already uses), plus active run id.

import { apiFetch } from './api.js';
import { getTheme, setTheme } from './theme.js';

export function mountShell() {
  if (document.querySelector('.app-shell')) return; // already mounted

  // Wails desktop sets window.runtime; flag the body so shell.css can
  // reserve room on the topbar for macOS traffic-lights and apply
  // any other native-window adjustments.
  if (typeof window !== 'undefined' && window.runtime) {
    document.body.classList.add('wails-desktop');
    if (/Mac/i.test(navigator.platform || '')) {
      document.body.classList.add('wails-mac');
    } else if (/Win/i.test(navigator.platform || '')) {
      document.body.classList.add('wails-windows');
    } else {
      document.body.classList.add('wails-linux');
    }
  }

  // ---- 1. Build the shell scaffold and adopt existing body content ----
  const body = document.body;
  const shell = document.createElement('div');
  shell.className = 'app-shell';
  shell.dataset.sidebar = 'open';

  shell.innerHTML = `
    <header class="shell-topbar" role="banner">
      <button type="button" class="shell-sidebar-toggle" aria-label="Toggle sidebar"
              data-role="sidebar-toggle" title="Toggle sidebar">
        <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor"
             stroke-width="1.5" stroke-linecap="round"><path d="M2 4h12M2 8h12M2 12h12"/></svg>
      </button>
      <a href="#" class="shell-topbar-brand" data-role="brand">
        <span class="shell-topbar-brand-mark" aria-hidden="true">
          <svg viewBox="0 0 24 24" fill="currentColor"><path d="M12 4.5L20.5 19.5H3.5z"/></svg>
        </span>
        <span>SFTP Load Test</span>
      </a>
      <span class="shell-topbar-status" data-role="status" data-state="idle"
            title="Idle: no run is active">
        <span class="shell-topbar-status-led" aria-hidden="true"></span>
        <span data-role="status-text">Idle</span>
      </span>
      <span class="shell-topbar-spacer"></span>
      <span class="shell-topbar-run-controls">
        <button type="button" class="btn-icon" data-variant="primary"
                data-role="topbar-run" title="Start a load test (Cmd+R)">
          <svg viewBox="0 0 16 16" width="12" height="12" fill="currentColor"><path d="M5 3l8 5-8 5V3z"/></svg>
          <span>Run</span>
        </button>
        <button type="button" class="btn-icon" data-variant="danger"
                data-role="topbar-stop" disabled
                title="Stop the active run (Cmd+.)">
          <svg viewBox="0 0 16 16" width="12" height="12" fill="currentColor"><rect x="4" y="4" width="8" height="8" rx="1"/></svg>
          <span>Stop</span>
        </button>
      </span>
      <span class="shell-topbar-actions">
        <button type="button" class="btn-icon" data-role="topbar-cmdk"
                title="Command palette">
          <svg viewBox="0 0 16 16" width="12" height="12" fill="none" stroke="currentColor"
               stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
            <path d="M5 5h6v6H5z"/><path d="M3 8h2M11 8h2M8 3v2M8 11v2"/>
          </svg>
          <span class="kbd">⌘K</span>
        </button>
        <div class="segmented" data-role="theme-switcher" role="group" aria-label="Theme">
          <button type="button" data-theme="auto"  aria-pressed="true"  title="Match system">Auto</button>
          <button type="button" data-theme="light" aria-pressed="false" title="Light">Light</button>
          <button type="button" data-theme="dark"  aria-pressed="false" title="Dark">Dark</button>
        </div>
      </span>
    </header>

    <aside class="shell-sidebar" aria-label="Tools">
      <div class="shell-sidebar-section">
        <div class="shell-sidebar-section-header">
          <span>Connections</span>
        </div>
        <div data-role="sidebar-connections">
          <div class="shell-sidebar-empty">No saved connections yet.</div>
        </div>
      </div>
      <div class="shell-sidebar-section">
        <div class="shell-sidebar-section-header">
          <span>Saved configs</span>
        </div>
        <div data-role="sidebar-configs">
          <div class="shell-sidebar-empty">Save the current form as a preset (α4).</div>
        </div>
      </div>
      <div class="shell-sidebar-section">
        <div class="shell-sidebar-section-header">
          <span>Recent runs</span>
        </div>
        <div data-role="sidebar-runs">
          <div class="shell-sidebar-empty">Finished runs appear here.</div>
        </div>
      </div>
      <div class="shell-sidebar-section">
        <div class="shell-sidebar-section-header">
          <span>Trusted hosts</span>
        </div>
        <div data-role="sidebar-trust">
          <div class="shell-sidebar-empty">No trusted hosts yet.</div>
        </div>
      </div>
    </aside>

    <main class="shell-main" data-role="main"></main>

    <footer class="shell-statusbar" role="contentinfo">
      <span class="shell-statusbar-cell" title="Host">
        <span class="label">host</span>
        <span class="value" data-role="status-host">—</span>
      </span>
      <span class="shell-statusbar-cell" title="OS / arch">
        <span class="value" data-role="status-os">—</span>
      </span>
      <span class="shell-statusbar-cell" title="CPU cores">
        <span class="value" data-role="status-cpu">—</span>
      </span>
      <span class="shell-statusbar-cell" title="RAM">
        <span class="value" data-role="status-ram">—</span>
      </span>
      <span class="shell-statusbar-cell" title="FD soft / hard">
        <span class="label">FD</span>
        <span class="value" data-role="status-fd">—</span>
      </span>
      <span class="shell-statusbar-cell" title="Active run id">
        <span class="value" data-role="status-runid">—</span>
      </span>
      <span class="shell-statusbar-spacer"></span>
      <span class="shell-statusbar-cell" data-role="status-version" title="Version">
        v0.9.0-dev
      </span>
    </footer>`;

  // Move every existing body child INTO the main pane (preserving order).
  // The legacy <div class="app"> is one of these — it now lives inside
  // .shell-main and the existing layout (masthead, host-bar, .grid, etc.)
  // continues to work as-is.
  const main = shell.querySelector('[data-role="main"]');
  while (body.firstChild) main.appendChild(body.firstChild);
  body.appendChild(shell);

  // Mark the body so shell.css's `.shell-mounted` rules can hide redundant
  // chrome (legacy masthead + dek, host-strip, proc-badge, the new
  // [data-component="masthead"] header that the topbar replaces).
  body.classList.add('shell-mounted');

  // ---- 2. Wire topbar controls to existing legacy buttons ----
  const legacyStart = document.getElementById('startBtn');
  const legacyStop  = document.getElementById('stopBtn');
  const tbRun = shell.querySelector('[data-role="topbar-run"]');
  const tbStop = shell.querySelector('[data-role="topbar-stop"]');
  if (legacyStart) {
    tbRun.addEventListener('click', (ev) => { ev.preventDefault(); legacyStart.click(); });
  } else {
    tbRun.disabled = true;
  }
  if (legacyStop) {
    tbStop.addEventListener('click', (ev) => { ev.preventDefault(); legacyStop.click(); });
  }

  // Cmd+R / Ctrl+R = run, Cmd+. / Ctrl+. = stop. We don't intercept the
  // browser refresh shortcut — using KeyR with no modifier conflict via
  // metaKey || ctrlKey check.
  document.addEventListener('keydown', (ev) => {
    if ((ev.metaKey || ev.ctrlKey) && ev.key === 'Enter') {
      ev.preventDefault();
      tbRun.click();
    } else if ((ev.metaKey || ev.ctrlKey) && ev.key === '.') {
      ev.preventDefault();
      tbStop.click();
    }
  });

  // ---- 3. Sidebar collapse toggle ----
  const toggle = shell.querySelector('[data-role="sidebar-toggle"]');
  toggle.addEventListener('click', (ev) => {
    ev.preventDefault();
    shell.dataset.sidebar = (shell.dataset.sidebar === 'open') ? 'collapsed' : 'open';
  });

  // ---- 4. Status (LED + active run polling) ----
  // Polls /api/status to flip the topbar LED + Stop button enabled state +
  // statusbar's run-id cell. Cheap; reuses the same data the records panel
  // is already polling for.
  const tbStatus = shell.querySelector('[data-role="status"]');
  const tbStatusText = shell.querySelector('[data-role="status-text"]');
  const sbRunID = shell.querySelector('[data-role="status-runid"]');
  async function pollStatus() {
    try {
      const r = await apiFetch('/api/status');
      if (!r.ok) throw new Error();
      const j = await r.json();
      if (j.active) {
        tbStatus.dataset.state = 'active';
        tbStatusText.textContent = 'Running';
        tbRun.disabled = true;
        tbStop.disabled = false;
        sbRunID.textContent = j.id || j.run_id || '—';
      } else {
        tbStatus.dataset.state = 'idle';
        tbStatusText.textContent = 'Idle';
        tbRun.disabled = false;
        tbStop.disabled = true;
        sbRunID.textContent = '—';
      }
    } catch {
      tbStatus.dataset.state = 'error';
      tbStatusText.textContent = 'Disconnected';
    } finally {
      setTimeout(pollStatus, 2000);
    }
  }
  pollStatus();

  // ---- 5. Status bar host capacity ----
  // Pulled from /api/host once at boot; matches what the legacy host.js
  // module shows in the host-bar (now hidden).
  async function fetchHost() {
    try {
      const r = await apiFetch('/api/host');
      if (!r.ok) return;
      const j = await r.json();
      shell.querySelector('[data-role="status-host"]').textContent = j.hostname || '—';
      shell.querySelector('[data-role="status-os"]').textContent = `${j.os || '?'}/${j.arch || '?'}`;
      shell.querySelector('[data-role="status-cpu"]').textContent = `${j.num_cpu || '?'} cores`;
      shell.querySelector('[data-role="status-ram"]').textContent = formatRam(j.ram_mb);
      shell.querySelector('[data-role="status-fd"]').textContent = formatFD(j.fd_limit_soft, j.fd_limit_hard);
    } catch { /* leave placeholders */ }
  }
  fetchHost();

  // ---- 6. Cmd+K placeholder — α4 swaps this with the real palette ----
  const cmdkBtn = shell.querySelector('[data-role="topbar-cmdk"]');
  cmdkBtn.addEventListener('click', (ev) => {
    ev.preventDefault();
    // Stub: nothing to do yet. α4 wires this to the command palette.
    cmdkBtn.animate([
      { transform: 'scale(1)' }, { transform: 'scale(0.94)' }, { transform: 'scale(1)' },
    ], { duration: 180, easing: 'ease-out' });
  });
  document.addEventListener('keydown', (ev) => {
    if ((ev.metaKey || ev.ctrlKey) && ev.key === 'k') {
      ev.preventDefault();
      cmdkBtn.click();
    }
  });

  // ---- 7. Theme switcher binding ----
  // masthead.js binds the LEGACY masthead's segmented; the shell's topbar
  // segmented is a separate DOM element. We bind it here so changes flow
  // to setTheme() and the press-state stays in sync with the rest of the
  // app (including any other switcher mounted later).
  const themeSeg = shell.querySelector('[data-role="theme-switcher"]');
  if (themeSeg) {
    const sync = () => {
      const cur = getTheme();
      themeSeg.querySelectorAll('button[data-theme]').forEach((b) => {
        b.setAttribute('aria-pressed', String(b.dataset.theme === cur));
      });
    };
    themeSeg.addEventListener('click', (ev) => {
      const btn = ev.target.closest('button[data-theme]');
      if (!btn) return;
      setTheme(btn.dataset.theme);
      sync();
    });
    sync();
  }
}

function formatRam(mb) {
  if (!mb) return '—';
  return mb >= 1024 ? `${(mb / 1024).toFixed(1)} GB` : `${mb} MB`;
}
const FD_UNLIMITED_THRESHOLD = 1_000_000_000;
function formatFD(soft, hard) {
  if (!soft) return '—';
  const fmt = (n) => (n >= FD_UNLIMITED_THRESHOLD ? '∞' : Number(n).toLocaleString());
  if (hard && hard !== soft) return `${fmt(soft)}/${fmt(hard)}`;
  return fmt(soft);
}
