// shell.js — v0.9.1 Apple-TV-class workbench shell.
//
// Builds the four-zone grid (topbar / sidebar / main / statusbar) and a
// view-switcher inside the main pane. The sidebar's primary nav rows
// trigger view changes — only one view is visible at a time:
//
//   workbench    Live throughput + latency charts, live records table.
//   configure    Workload + Download + Schedule + Review (legacy form).
//   history      Previous runs (the runs-history card list).
//   cluster      Workers + cluster-distribute toggle (legacy + new).
//   trust        Trusted SSH host keys panel.
//
// The legacy markup is reorganised at runtime: each existing
// [data-component] is moved into the correct view container, preserving
// every id and behaviour. Tests still find what they assert on; the
// layout just chooses which subset is visible at any moment.

import { apiFetch } from './api.js';
import { getTheme, setTheme } from './theme.js';
import { makeDraggable } from './draggable.js';

// Read the server-rendered platform version from the meta tag. The Go
// middleware substitutes __SFTPL_VERSION__ at serve time, so this is
// already the correct value the moment the page parses. No async fetch
// needed. Returns "v0.14.19" or empty string if the meta tag is
// missing (which would only happen on a build serving stale HTML).
function serverVersionLabel() {
  const meta = document.querySelector('meta[name="sftpl-version"]');
  const raw = meta?.getAttribute('content') || '';
  if (!raw || raw.includes('__SFTPL_VERSION__')) return '';
  return 'v' + raw;
}

// Minimal HTML-escape for content interpolated into the shell template.
// Version strings are tightly controlled (set by Go const) but we still
// escape defensively so a future change to platformVersion can't inject.
function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  }[c]));
}

// SVG icons used in the sidebar primary nav.
const ICONS = {
  workbench: '<svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"><path d="M2 13l4-7 3 5 5-9"/><path d="M2 13h12"/></svg>',
  configure: '<svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"><circle cx="8" cy="8" r="2.5"/><path d="M8 1v2M8 13v2M1 8h2M13 8h2M3.5 3.5l1.4 1.4M11.1 11.1l1.4 1.4M3.5 12.5l1.4-1.4M11.1 4.9l1.4-1.4"/></svg>',
  schedule:  '<svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="3" width="12" height="11" rx="1.5"/><path d="M2 6h12"/><path d="M5 1.5v3M11 1.5v3"/></svg>',
  runs:      '<svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"><path d="M3 2.5h10v11H3z"/><path d="M5.5 5.5h5M5.5 8h5M5.5 10.5h3"/><circle cx="13" cy="13" r="2.2" fill="var(--accent)" stroke="none" opacity="0.0"/></svg>',
  cluster:   '<svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="2.5" width="5" height="5" rx="0.5"/><rect x="9" y="2.5" width="5" height="5" rx="0.5"/><rect x="2" y="8.5" width="5" height="5" rx="0.5"/><rect x="9" y="8.5" width="5" height="5" rx="0.5"/></svg>',
  trust:     '<svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"><path d="M8 2l5 2v4c0 3-2 5-5 6-3-1-5-3-5-6V4z"/><path d="M6 8l1.5 1.5L10 7"/></svg>',
};

const VIEWS = [
  { id: 'workbench', label: 'Workbench', icon: ICONS.workbench, hint: 'Live metrics, charts, slowdowns, records' },
  { id: 'configure', label: 'Configure', icon: ICONS.configure, hint: 'Workload + connection settings' },
  { id: 'schedule',  label: 'Schedule',  icon: ICONS.schedule,  hint: 'Run later, save / load configs' },
  { id: 'runs',      label: 'Runs',      icon: ICONS.runs,      hint: 'About to run + past runs' },
  { id: 'cluster',   label: 'Cluster',   icon: ICONS.cluster,   hint: 'Distribute load across workers' },
  { id: 'trust',     label: 'Trust',     icon: ICONS.trust,     hint: 'SSH host keys' },
];

const VIEW_KEY = 'sftp-loadtest-active-view-v1';

export function mountShell() {
  if (document.querySelector('.app-shell')) return;

  // Wails desktop detection (CSS hooks for native-window padding/drag).
  if (typeof window !== 'undefined' && window.runtime) {
    document.body.classList.add('wails-desktop');
    if (/Mac/i.test(navigator.platform || '')) document.body.classList.add('wails-mac');
    else if (/Win/i.test(navigator.platform || '')) document.body.classList.add('wails-windows');
    else document.body.classList.add('wails-linux');
  }
  // OS detection — runs on BOTH Wails and plain browsers so the
  // Windows polish ruleset (font cascade + dark-tier hierarchy +
  // ambient + glass overrides) applies wherever the user is on
  // Windows, not only inside the Wails native window.
  if (typeof navigator !== 'undefined') {
    const ua = navigator.userAgent || '';
    const platform = navigator.platform || '';
    if (/Win/i.test(platform) || /Windows/i.test(ua)) {
      document.body.classList.add('is-windows');
    } else if (/Mac/i.test(platform) || /Mac OS X/i.test(ua)) {
      document.body.classList.add('is-macos');
    } else if (/Linux/i.test(platform) || /Linux/i.test(ua)) {
      document.body.classList.add('is-linux');
    }
  }

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
                data-role="topbar-run" title="Start a load test (⌘↵)">
          <svg viewBox="0 0 16 16" width="12" height="12" fill="currentColor"><path d="M5 3l8 5-8 5V3z"/></svg>
          <span>Run</span>
        </button>
        <button type="button" class="btn-icon" data-variant="danger"
                data-role="topbar-stop" disabled title="Stop the active run (⌘.)">
          <svg viewBox="0 0 16 16" width="12" height="12" fill="currentColor"><rect x="4" y="4" width="8" height="8" rx="1"/></svg>
          <span>Stop</span>
        </button>
      </span>
      <span class="shell-topbar-actions">
        <button type="button" class="btn-icon" data-role="topbar-cmdk"
                title="Command palette (⌘K)">
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
        <!-- v0.19.17 — in-app window controls. Always visible regardless
             of OS chrome state, so a Wails fullscreen (which hides the
             native traffic lights on macOS and the OS triplet on Windows)
             still gives the operator min/max/close affordances. Hidden
             via CSS in plain-browser mode where the OS already supplies
             window chrome. -->
        <span class="shell-window-controls" data-role="window-controls" aria-label="Window">
          <button type="button" class="btn-icon" data-role="win-min" title="Minimise">
            <svg viewBox="0 0 16 16" width="12" height="12" stroke="currentColor" stroke-width="1.5" fill="none" stroke-linecap="round"><path d="M3 8h10"/></svg>
          </button>
          <button type="button" class="btn-icon" data-role="win-max" title="Maximise / restore">
            <svg viewBox="0 0 16 16" width="12" height="12" stroke="currentColor" stroke-width="1.5" fill="none"><rect x="3.5" y="3.5" width="9" height="9" rx="1"/></svg>
          </button>
          <button type="button" class="btn-icon" data-role="win-close" data-variant="danger" title="Close window">
            <svg viewBox="0 0 16 16" width="12" height="12" stroke="currentColor" stroke-width="1.5" fill="none" stroke-linecap="round"><path d="M4 4l8 8M12 4l-8 8"/></svg>
          </button>
        </span>
      </span>
    </header>

    <aside class="shell-sidebar" aria-label="Navigation">
      <div class="shell-sidebar-search">
        <span class="shell-sidebar-search-icon" aria-hidden="true">
          <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor"
               stroke-width="1.5" stroke-linecap="round"><circle cx="7" cy="7" r="4.5"/><path d="M10.5 10.5L14 14"/></svg>
        </span>
        <input type="text" class="shell-sidebar-search-input" placeholder="Search…"
               data-role="sidebar-search" aria-label="Search">
        <span class="shell-sidebar-search-shortcut">⌘K</span>
      </div>

      <nav class="shell-sidebar-section" data-role="primary-nav" aria-label="Views">
        ${VIEWS.map((v) => `
          <div class="shell-sidebar-row" role="button" tabindex="0"
               data-action="view" data-view="${v.id}" aria-selected="false"
               title="${v.hint}">
            <span class="row-icon" aria-hidden="true">${v.icon}</span>
            <span class="row-label">${v.label}</span>
          </div>`).join('')}
      </nav>

      <!-- v0.19.17 — collapsible sections. Default = collapsed so the
           sidebar stays visually quiet on first load (only the primary
           nav with Trust at the bottom is visible without effort). The
           operator clicks the header to expand any group. State per
           section persists in localStorage. -->
      <div class="shell-sidebar-section" data-collapsible data-section="connections">
        <button type="button" class="shell-sidebar-section-header" data-role="section-toggle" aria-expanded="false">
          <span>Connections</span>
          <span class="shell-sidebar-section-chevron" aria-hidden="true">▸</span>
        </button>
        <div data-role="sidebar-connections" class="shell-sidebar-section-body">
          <div class="shell-sidebar-empty">No saved connections yet.</div>
        </div>
      </div>
      <div class="shell-sidebar-section" data-collapsible data-section="configs">
        <button type="button" class="shell-sidebar-section-header" data-role="section-toggle" aria-expanded="false">
          <span>Saved configs</span>
          <span class="shell-sidebar-section-chevron" aria-hidden="true">▸</span>
        </button>
        <div data-role="sidebar-configs" class="shell-sidebar-section-body">
          <div class="shell-sidebar-empty">Save the current form via ⌘K → “Save current config…”.</div>
        </div>
      </div>
      <div class="shell-sidebar-section" data-collapsible data-section="runs">
        <button type="button" class="shell-sidebar-section-header" data-role="section-toggle" aria-expanded="false">
          <span>Recent runs</span>
          <span class="shell-sidebar-section-chevron" aria-hidden="true">▸</span>
        </button>
        <div data-role="sidebar-runs" class="shell-sidebar-section-body">
          <div class="shell-sidebar-empty">Finished runs appear here.</div>
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
      <span class="shell-statusbar-spacer"></span>
      <span class="shell-statusbar-cell" data-role="status-runid" title="Active run id">—</span>
      <span class="shell-statusbar-cell" title="Platform version" data-role="status-version">${escapeHtml(serverVersionLabel())}</span>
      <!-- v0.19.17 — dock toggle. Operators wanted the live status row
           pinned at the TOP of the workspace (next to the topbar) rather
           than the bottom; default is now top, with a one-click flip. -->
      <button type="button" class="shell-statusbar-dock" data-role="statusbar-dock"
              title="Move status bar to top / bottom" aria-label="Toggle status bar position">
        <svg viewBox="0 0 16 16" width="11" height="11" stroke="currentColor" stroke-width="1.4" fill="none" stroke-linecap="round" stroke-linejoin="round">
          <path d="M3 8h10M8 3l-3 3M8 3l3 3M8 13l-3-3M8 13l3-3"/>
        </svg>
      </button>
    </footer>`;

  // Adopt every existing body child into .shell-main (preserves IDs).
  const main = shell.querySelector('[data-role="main"]');
  while (body.firstChild) main.appendChild(body.firstChild);
  body.appendChild(shell);
  body.classList.add('shell-mounted');

  // Build the view containers and migrate components into them.
  buildViews(main);

  // Wire run controls.
  wireRunControls(shell);
  // Sidebar toggle.
  shell.querySelector('[data-role="sidebar-toggle"]').addEventListener('click', () => {
    shell.dataset.sidebar = (shell.dataset.sidebar === 'open') ? 'collapsed' : 'open';
  });
  // Status polling.
  pollStatus(shell);
  fetchHost(shell);
  // Theme switcher binding.
  wireTheme(shell);
  // View switching from primary-nav rows.
  wireViews(shell, main);
  // Sidebar search filters within visible view's text.
  wireSidebarSearch(shell, main);
  // Cmd+K stub (real palette in command-palette.js).
  shell.querySelector('[data-role="topbar-cmdk"]').addEventListener('click', () => {
    document.dispatchEvent(new CustomEvent('sftpl:open-cmdk'));
  });

  // v0.19.17 — wire collapsible sidebar sections, in-app window
  // controls, and the statusbar dock toggle. Each helper is no-op safe
  // if its DOM nodes aren't present, so partial template renders won't
  // crash the shell.
  wireSidebarSections(shell);
  wireWindowControls(shell);
  wireStatusbarDock(shell);

  // Restore last view (default workbench).
  const initial = readView();
  setView(initial, shell, main);
}

// Sidebar collapsible sections — Connections / Saved configs / Recent
// runs. Default-collapsed; remembered per-section in localStorage so
// the operator's choice survives reloads.
const SIDEBAR_KEY = 'sftp-loadtest-sidebar-sections-v1';
function wireSidebarSections(shell) {
  const state = (() => {
    try {
      return JSON.parse(localStorage.getItem(SIDEBAR_KEY) || '{}') || {};
    } catch { return {}; }
  })();
  const sections = shell.querySelectorAll('.shell-sidebar-section[data-collapsible]');
  sections.forEach((sec) => {
    const id = sec.dataset.section;
    const expanded = state[id] === true; // default = collapsed
    sec.dataset.collapsed = expanded ? 'false' : 'true';
    const btn = sec.querySelector('[data-role="section-toggle"]');
    if (btn) btn.setAttribute('aria-expanded', expanded ? 'true' : 'false');
    btn?.addEventListener('click', () => {
      const next = sec.dataset.collapsed !== 'true'; // currently expanded → collapse
      sec.dataset.collapsed = next ? 'true' : 'false';
      btn.setAttribute('aria-expanded', next ? 'false' : 'true');
      state[id] = !next;
      try { localStorage.setItem(SIDEBAR_KEY, JSON.stringify(state)); } catch {}
    });
  });
}

// In-app window controls — Min / Max / Close. Wired to Wails runtime
// when running inside the desktop app; CSS hides the cluster in plain
// browser mode. Always visible inside Wails so a fullscreen window
// (which hides macOS traffic lights / Windows triplet) still has
// reachable affordances.
function wireWindowControls(shell) {
  const wrap = shell.querySelector('[data-role="window-controls"]');
  if (!wrap) return;
  const isWails = !!(typeof window !== 'undefined' && window.runtime);
  if (!isWails) {
    wrap.style.display = 'none';
    return;
  }
  const r = window.runtime;
  const min = wrap.querySelector('[data-role="win-min"]');
  const max = wrap.querySelector('[data-role="win-max"]');
  const close = wrap.querySelector('[data-role="win-close"]');
  min?.addEventListener('click', () => { try { r.WindowMinimise?.(); } catch {} });
  max?.addEventListener('click', () => { try { r.WindowToggleMaximise?.(); } catch {} });
  close?.addEventListener('click', () => { try { r.Quit?.(); } catch {} });
}

// Statusbar drag — v0.19.26 delegates to the shared draggable helper
// so the Configure summary pill (cfg-summary-bar) gets the same
// behaviour without duplicating the pointer + persistence code.
function wireStatusbarDock(shell) {
  const bar = shell.querySelector('.shell-statusbar');
  if (!bar) return;
  makeDraggable(bar, {
    storageKey: 'sftp-loadtest-statusbar-pos-v2',
    defaultTop: null, // null → CSS default (topbar + 8 px)
    defaultRight: 240,
    anchorSelector: '[data-role="save-preset"]',
    dockButton: bar.querySelector('[data-role="statusbar-dock"]'),
  });
}

// buildViews creates [data-view="<id>"] containers in the main pane and
// reparents existing legacy components into the right one. Anything not
// claimed stays in workbench (records + charts default home).
function buildViews(main) {
  const containers = {};
  for (const v of VIEWS) {
    const c = document.createElement('div');
    c.dataset.view = v.id;
    c.dataset.viewActive = 'false';
    main.appendChild(c);
    containers[v.id] = c;
  }

  // The legacy .app contains every legacy component. We reparent
  // selected sub-trees into the new view containers.
  const legacy = main.querySelector('.app');
  if (!legacy) return;

  // Workbench: live charts + records panel + run-header.
  const workbench = containers.workbench;
  for (const sel of ['[data-component="run-header"]', '[data-component="records"]']) {
    const el = legacy.querySelector(sel);
    if (el) workbench.appendChild(el);
  }

  // The legacy .grid has TWO children — left = config cards, right =
  // results cards (Live metrics, Previous runs, Slowdown events, Recent
  // uploads). Move the right column into Workbench BEFORE the rest of
  // .grid heads to Configure. Identified by the live-metrics anchor
  // (#m_elapsed) which only exists in the right column.
  const liveAnchor = legacy.querySelector('#m_elapsed');
  if (liveAnchor) {
    const liveCol = liveAnchor.closest('.grid > div');
    if (liveCol) {
      liveCol.dataset.role = 'workbench-results';
      workbench.appendChild(liveCol);
    }
  }

  // Configure: Quick Checks + workload card (host/port/folder/users/files/sizes/parallel/duration).
  const configure = containers.configure;
  const quickChecks = legacy.querySelector('[data-component="connection"]');
  if (quickChecks) configure.appendChild(quickChecks);

  // Find the legacy schedule-and-config card (it has a header label
  // "Schedule & config") and Pin it to the SCHEDULE view BEFORE moving
  // .grid wholesale. We identify by the sched_at field's owning .card.
  const schedAt = legacy.querySelector('#sched_at');
  const scheduleCard = schedAt ? schedAt.closest('.card') : null;
  const schedule = containers.schedule;
  if (scheduleCard) {
    schedule.appendChild(scheduleCard);
  } else {
    schedule.innerHTML = '<div class="empty-pane">Schedule card not found.</div>';
  }

  // Now put the rest of .grid (left column: workload, large, download,
  // etc.) into Configure. Schedule card and live-metrics column are
  // gone by this point.
  const grid = legacy.querySelector('.grid');
  if (grid) configure.appendChild(grid);
  const wizard = legacy.querySelector('[data-component="wizard"]');
  if (wizard && wizard.parentElement === legacy) configure.appendChild(wizard);

  // Runs view = the merged Review + History timeline. mountReview fills
  // [data-role="runs-plan"] at the top; runs-history renders below it
  // as the past-runs list. Single nav entry, two stacked sections.
  const runs = containers.runs;
  runs.dataset.role = 'runs-view';
  const planSection = document.createElement('div');
  planSection.dataset.role = 'runs-plan';
  runs.appendChild(planSection);
  const runsHistory = legacy.querySelector('[data-component="runs-history"]');
  if (runsHistory) runs.appendChild(runsHistory);

  // Trust: trusted-hosts component.
  const trust = containers.trust;
  const trusted = legacy.querySelector('[data-component="trusted-hosts"]');
  if (trusted) trust.appendChild(trusted);

  // Cluster: gets a placeholder; cluster-ui.js fills it on mount.
  const cluster = containers.cluster;
  cluster.dataset.role = 'cluster-view';

  // Whatever remains in legacy .app gets placed in workbench (slowdown
  // table, ceiling banner, toast container, etc.) UNLESS it is one of
  // the redundant decorative chrome elements the shell now owns
  // (newspaper masthead, the legacy host-strip, the proc-badge).
  //
  // Critically, we HIDE rather than REMOVE: legacy.js's poll() reads
  // from $('p_cpu') / $('h_net') / $('sched_banner') / $('h_fdlimit')
  // which live INSIDE host-strip + proc-badge. If we deleted those
  // elements, the writes throw TypeError, the swallowing catch in
  // poll() eats the error, and every tile (including the live-metrics
  // grid below) silently freezes at its initial value while the run
  // is active. Hiding keeps the IDs reachable.
  const REDUNDANT = [
    'header.masthead',  // legacy newspaper masthead (serif title + dek)
    '.host-strip',      // legacy host-strip (statusbar replaces it)
    '.proc-badge',      // legacy floating proc badge
  ];
  for (const sel of REDUNDANT) {
    legacy.querySelectorAll(sel).forEach((el) => {
      el.dataset.shellHidden = '1';
      el.style.display = 'none';
    });
  }
  while (legacy.firstChild) workbench.appendChild(legacy.firstChild);
  // Remove the now-empty legacy.app shell.
  legacy.remove();
}

function wireRunControls(shell) {
  const legacyStart = document.getElementById('startBtn');
  const legacyStop  = document.getElementById('stopBtn');
  const tbRun = shell.querySelector('[data-role="topbar-run"]');
  const tbStop = shell.querySelector('[data-role="topbar-stop"]');
  if (legacyStart) {
    tbRun.addEventListener('click', (e) => {
      e.preventDefault();
      // Optimistic disable: a click against a disabled button should
      // be a no-op anyway, but guard against a double-fire from the
      // capture-phase pre-flight wrapper. The poll will re-enable
      // within 2 s if the run never actually started.
      if (tbRun.disabled) return;
      tbRun.disabled = true;
      tbStop.disabled = false;
      legacyStart.click();
    });
  } else {
    tbRun.disabled = true;
  }
  if (legacyStop) {
    tbStop.addEventListener('click', (e) => {
      e.preventDefault();
      if (tbStop.disabled) return;
      // Optimistic disable so the operator can't double-click Stop
      // while POST /api/stop is in flight (or in the up-to-2-second
      // window before the status poll confirms idle). The next tick
      // reconciles either way.
      tbStop.disabled = true;
      tbRun.disabled = false;
      legacyStop.click();
    });
  }
  document.addEventListener('keydown', (ev) => {
    if ((ev.metaKey || ev.ctrlKey) && ev.key === 'Enter') { ev.preventDefault(); tbRun.click(); }
    else if ((ev.metaKey || ev.ctrlKey) && ev.key === '.') { ev.preventDefault(); tbStop.click(); }
  });
}

async function pollStatus(shell) {
  const tbStatus = shell.querySelector('[data-role="status"]');
  const tbStatusText = shell.querySelector('[data-role="status-text"]');
  const tbRun = shell.querySelector('[data-role="topbar-run"]');
  const tbStop = shell.querySelector('[data-role="topbar-stop"]');
  const sbRunID = shell.querySelector('[data-role="status-runid"]');
  async function tick() {
    try {
      const r = await apiFetch('/api/status');
      if (!r.ok) throw new Error();
      const j = await r.json();
      if (j.active) {
        tbStatus.dataset.state = 'active';
        tbStatusText.textContent = 'Running';
        tbRun.disabled = true;
        tbStop.disabled = false;
        sbRunID.textContent = j.id || '—';
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
      setTimeout(tick, 2000);
    }
  }
  tick();
}

async function fetchHost(shell) {
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

function wireTheme(shell) {
  const themeSeg = shell.querySelector('[data-role="theme-switcher"]');
  if (!themeSeg) return;
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

function wireViews(shell, main) {
  const rows = shell.querySelectorAll('[data-action="view"]');
  rows.forEach((row) => {
    const change = () => setView(row.dataset.view, shell, main);
    row.addEventListener('click', change);
    row.addEventListener('keydown', (ev) => {
      if (ev.key === 'Enter' || ev.key === ' ') { ev.preventDefault(); change(); }
    });
  });
}

function setView(id, shell, main) {
  if (!VIEWS.find((v) => v.id === id)) id = 'workbench';
  // Restore the main container — run-detail.js hides .shell-main when
  // it opens a detail pane. Without this, a click on any sidebar nav
  // row would silently no-op (view containers toggle inside an
  // invisible main). Always force-restore + dismiss any sibling
  // detail pane so primary nav is the universal escape hatch.
  if (main.dataset.hidden === '1' || main.style.display === 'none') {
    main.dataset.hidden = '0';
    main.style.display = '';
  }
  document.querySelectorAll('.run-detail-view').forEach((el) => {
    el.style.display = 'none';
  });
  // Toggle aria-selected on rows.
  shell.querySelectorAll('[data-action="view"]').forEach((row) => {
    row.setAttribute('aria-selected', String(row.dataset.view === id));
  });
  // Toggle visibility on view containers.
  main.querySelectorAll('[data-view]').forEach((c) => {
    c.dataset.viewActive = String(c.dataset.view === id);
  });
  // v0.19.28 — surface the active view as a body data attribute so
  // CSS rules outside the .shell-main subtree (e.g. body-level fixed
  // pills like .cfg-summary-bar) can scope themselves to the right
  // view without listening to the JS event.
  document.body.dataset.activeView = id;
  try { localStorage.setItem(VIEW_KEY, id); } catch {}
  document.dispatchEvent(new CustomEvent('sftpl:view-changed', { detail: { view: id } }));
}

function readView() {
  // Default to Configure on first run — the form is what a new operator
  // wants to fill. Workbench is more useful when a run is already active.
  try { return localStorage.getItem(VIEW_KEY) || 'configure'; } catch { return 'configure'; }
}

function wireSidebarSearch(shell, main) {
  const input = shell.querySelector('[data-role="sidebar-search"]');
  if (!input) return;
  // Make the sidebar Search read like a real search box: typing a single
  // character immediately opens the Cmd+K palette pre-filled with that
  // value, the same way Spotlight or VS Code's quick-open works. Until
  // v0.13.30 it only fired on Enter — clicking the box and typing
  // looked completely broken because nothing happened until the
  // operator hit Enter, and there was no hint they needed to.
  input.placeholder = 'Search commands, presets, runs…';
  let opening = false;
  function openPalette(q) {
    if (opening) return;
    opening = true;
    document.dispatchEvent(new CustomEvent('sftpl:open-cmdk', { detail: { query: q } }));
    // Clear the local input on the next tick — the palette has its
    // own input and steals focus; we don't want a stale value left
    // here when the operator closes the palette and clicks back.
    setTimeout(() => { input.value = ''; opening = false; }, 0);
  }
  input.addEventListener('input', (ev) => {
    const q = ev.target.value;
    if (q.length === 0) return;
    openPalette(q);
  });
  // Enter is the keyboard-only path (some users tab into the box and
  // press Enter without typing). Open the palette empty-handed if so.
  input.addEventListener('keydown', (ev) => {
    if (ev.key === 'Enter') {
      ev.preventDefault();
      openPalette(input.value || '');
    }
  });
  // v0.19.24 — focusing or clicking the search box opens the palette
  // immediately, the same way Spotlight / VS Code quick-open behave.
  // Pre-fix the operator had to type a character before anything
  // showed up; clicking the input made it look broken because the
  // expected suggestion list never appeared. We use mousedown so the
  // dispatch happens before focus moves to the palette's own input.
  input.addEventListener('mousedown', (ev) => {
    ev.preventDefault();
    openPalette(input.value || '');
  });
  // Keyboard tab-into still wants the palette without an Enter press.
  input.addEventListener('focus', () => {
    if (!opening) openPalette(input.value || '');
  });
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
