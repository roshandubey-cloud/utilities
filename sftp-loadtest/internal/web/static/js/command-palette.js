// command-palette.js — guide-class search.
//
// Cmd+K (Ctrl+K) opens a centred modal that doubles as the app's command
// palette and inline help. The sidebar `Search…` input also feeds in here
// via the `sftpl:open-cmdk` custom event (see shell.js).
//
// Every entry follows a single rich shape so the row template can render
// label + description + shortcut + meta uniformly:
//
//   {
//     id, section, icon, label, description,
//     shortcut?, meta?, keywords?, weight, recency?,
//     action(), disabled?: { reason }
//   }
//
// Sections (with default sort weight):
//
//   First steps   (110)  — onboarding tasks, top of an empty query
//   Run controls  (100)  — Start, Stop, Test connection
//   Configuration (90)   — Save / load / import / export presets
//   Connections   (80)   — saved host:port:user entries
//   Recent runs   (70)   — last N completed runs
//   View          (60)   — theme, sidebar
//   Security      (50)   — trust stores
//   Help          (40)   — "what does this do?" cards
//
// Empty-query view shows a curated First-steps list + the most recent
// preset, connection, and run so the palette is always informative.
// Empty sections under a query render a helpful CTA instead of vanishing.

import { list as listConfigs, save as saveConfig, load as loadConfig, remove as removeConfig } from './saved-configs.js';
import { listSaved as listConns, applyEntry as applyConn, removeEntry as removeConn, promptSave as promptSaveConn } from './saved-connections.js';
import { setTheme, getTheme } from './theme.js';
import { pushToast } from './toast.js';
import { apiFetch } from './api.js';

const ONBOARD_KEY = 'sftpl-cmdk-onboard-v2';

let backdrop = null;
let panel = null;
let input = null;
let resultsRoot = null;
let visibleResults = [];
let activeIndex = 0;
// Recent runs are async; we cache the last successful list and refresh
// when the palette opens. Empty array is the safe default — the UI
// shows a "no runs yet" CTA when this is empty AND a query is active.
let recentRunsCache = [];

export function mountCommandPalette() {
  document.addEventListener('keydown', (ev) => {
    if ((ev.metaKey || ev.ctrlKey) && ev.key.toLowerCase() === 'k') {
      ev.preventDefault();
      open();
    } else if (ev.key === 'Escape' && backdrop) {
      ev.preventDefault();
      close();
    }
  });
  document.addEventListener('sftpl:open-cmdk', (ev) => {
    const initialQuery = ev.detail && ev.detail.query;
    open(initialQuery);
  });
}

function open(initialQuery) {
  if (backdrop) return;
  backdrop = document.createElement('div');
  backdrop.className = 'cmdk-backdrop';
  backdrop.dataset.component = 'command-palette';
  const showOnboard = !localStorage.getItem(ONBOARD_KEY);
  backdrop.innerHTML = `
    <div class="cmdk-panel" role="dialog" aria-modal="true" aria-label="Command palette">
      ${showOnboard ? `
        <div class="cmdk-banner" data-role="onboard">
          <span class="cmdk-banner-text">
            <strong>Tip:</strong> type to filter · <kbd>↑</kbd> <kbd>↓</kbd> navigate · <kbd>↵</kbd> run · <kbd>Esc</kbd> close.
            Searches commands, presets, saved connections, recent runs, and help.
          </span>
          <button type="button" class="cmdk-banner-dismiss" data-role="dismiss-onboard" aria-label="Dismiss tip">×</button>
        </div>` : ''}
      <div class="cmdk-search">
        <span class="cmdk-search-icon" aria-hidden="true">
          <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor"
               stroke-width="1.5" stroke-linecap="round"><circle cx="7" cy="7" r="4.5"/><path d="M10.5 10.5L14 14"/></svg>
        </span>
        <input class="cmdk-input" type="text" placeholder="Type a command, search presets / runs / connections, or just say what you want…"
               autocomplete="off" spellcheck="false" data-role="input" />
      </div>
      <div class="cmdk-results" data-role="results"></div>
      <div class="cmdk-foot">
        <span><kbd>↑</kbd><kbd>↓</kbd> navigate</span>
        <span><kbd>↵</kbd> run</span>
        <span><kbd>Esc</kbd> close</span>
        <span class="cmdk-foot-hint">⌘K to reopen</span>
      </div>
    </div>`;
  backdrop.addEventListener('click', (ev) => {
    if (ev.target === backdrop) close();
  });
  document.body.appendChild(backdrop);
  panel = backdrop.querySelector('.cmdk-panel');
  input = backdrop.querySelector('[data-role="input"]');
  resultsRoot = backdrop.querySelector('[data-role="results"]');

  const dismissBtn = backdrop.querySelector('[data-role="dismiss-onboard"]');
  if (dismissBtn) {
    dismissBtn.addEventListener('click', (ev) => {
      ev.preventDefault();
      ev.stopPropagation();
      localStorage.setItem(ONBOARD_KEY, '1');
      backdrop.querySelector('[data-role="onboard"]')?.remove();
      input.focus();
    });
  }

  input.addEventListener('input', () => render(input.value));
  input.addEventListener('keydown', onInputKey);
  input.focus();

  // Refresh recent runs (async, fire-and-forget). When it resolves we
  // re-render so the current view picks up the new data.
  refreshRecentRuns().then(() => render(input.value));

  if (initialQuery) {
    input.value = initialQuery;
    render(initialQuery);
  } else {
    render('');
  }
}

function close() {
  if (!backdrop) return;
  backdrop.remove();
  backdrop = panel = input = resultsRoot = null;
  visibleResults = [];
  activeIndex = 0;
}

function onInputKey(ev) {
  if (ev.key === 'ArrowDown') { ev.preventDefault(); move(1); }
  else if (ev.key === 'ArrowUp') { ev.preventDefault(); move(-1); }
  else if (ev.key === 'Enter')   { ev.preventDefault(); fire(); }
}

function move(delta) {
  if (visibleResults.length === 0) return;
  activeIndex = (activeIndex + delta + visibleResults.length) % visibleResults.length;
  syncActive();
}

function syncActive() {
  resultsRoot.querySelectorAll('.cmdk-result').forEach((el, i) => {
    el.dataset.active = String(i === activeIndex);
    if (i === activeIndex) el.scrollIntoView({ block: 'nearest' });
  });
}

function fire() {
  const r = visibleResults[activeIndex];
  if (!r || r.disabled) return;
  close();
  try {
    r.action();
  } catch (e) {
    pushToast(`Command failed: ${e.message || e}`, 'error');
  }
}

async function refreshRecentRuns() {
  try {
    const res = await apiFetch('/api/runs');
    if (!res.ok) return;
    const data = await res.json();
    recentRunsCache = Array.isArray(data.runs) ? data.runs.slice(0, 10) : [];
  } catch {
    // Network failure is fine — palette renders without recent runs.
  }
}

const SECTION_ORDER = [
  'First steps',
  'Run controls',
  'Configuration',
  'Connections',
  'Recent runs',
  'View',
  'Security',
  'Help',
];

const SECTION_WEIGHT = {
  'First steps': 110,
  'Run controls': 100,
  'Configuration': 90,
  'Connections': 80,
  'Recent runs': 70,
  'View': 60,
  'Security': 50,
  'Help': 40,
};

function render(query) {
  const q = (query || '').toLowerCase().trim();
  const all = collectCommands();
  const filtered = q
    ? all.filter((c) => matches(c, q))
    : all.filter((c) => isEmptyQueryEntry(c));
  visibleResults = filtered;
  activeIndex = 0;

  if (filtered.length === 0) {
    resultsRoot.innerHTML = `
      <div class="cmdk-empty">
        <strong>No matches for "${escapeHTML(q)}"</strong>
        <p>Try: <code>start</code>, <code>preset</code>, <code>theme</code>, <code>cluster</code>, <code>trust</code>, or a host name.</p>
      </div>`;
    return;
  }

  // Group + sort sections by SECTION_ORDER position.
  const groups = new Map();
  for (const c of filtered) {
    const k = c.section || 'Actions';
    if (!groups.has(k)) groups.set(k, []);
    groups.get(k).push(c);
  }
  const ordered = Array.from(groups.entries()).sort((a, b) => {
    const ia = SECTION_ORDER.indexOf(a[0]);
    const ib = SECTION_ORDER.indexOf(b[0]);
    return (ia === -1 ? 99 : ia) - (ib === -1 ? 99 : ib);
  });

  const html = [];
  let i = 0;
  for (const [section, cmds] of ordered) {
    html.push(`<div class="cmdk-section">${escapeHTML(section)}</div>`);
    for (const c of cmds) {
      const idx = i++;
      html.push(renderRow(c, idx, q));
    }
  }
  resultsRoot.innerHTML = html.join('');
  resultsRoot.querySelectorAll('.cmdk-result').forEach((el) => {
    el.addEventListener('mouseenter', () => {
      activeIndex = Number(el.dataset.idx);
      syncActive();
    });
    el.addEventListener('click', () => {
      activeIndex = Number(el.dataset.idx);
      fire();
    });
  });
}

function isEmptyQueryEntry(c) {
  // Empty-query view: First steps always shown, plus a curated subset of
  // every other section so the palette is informative without a query.
  if (c.section === 'First steps') return true;
  if (c.section === 'Run controls') return true; // Start/Stop/Test always visible
  // For preset / connection / run sections show the top 3 most recent.
  if (c.featured) return true;
  // Help cards always visible at the bottom.
  if (c.section === 'Help') return true;
  return false;
}

function matches(c, q) {
  const haystack = `${c.label} ${c.description || ''} ${c.section || ''} ${(c.keywords || []).join(' ')} ${c.meta || ''}`.toLowerCase();
  // Token-based subsequence: every whitespace-delimited token in the query
  // must appear somewhere. More forgiving than substring (lets "fpm fast"
  // match "Files-per-minute throttle"), tighter than fuzzy (won't match
  // unrelated entries on stray characters).
  const tokens = q.split(/\s+/).filter(Boolean);
  return tokens.every((t) => haystack.includes(t));
}

function renderRow(c, idx, query) {
  const icon = c.icon || '•';
  const desc = c.description ? `<span class="cmdk-result-desc">${highlight(c.description, query)}</span>` : '';
  const meta = c.meta ? `<span class="cmdk-result-meta">${escapeHTML(c.meta)}</span>` : '';
  const shortcut = c.shortcut ? `<span class="cmdk-result-shortcut">${escapeHTML(c.shortcut)}</span>` : '';
  const disabled = c.disabled ? `<span class="cmdk-result-disabled">${escapeHTML(c.disabled.reason)}</span>` : '';
  return `
    <div class="cmdk-result cmdk-result-rich" data-idx="${idx}" data-active="${idx === 0}" ${c.disabled ? 'data-disabled="true"' : ''}>
      <span class="cmdk-result-icon" aria-hidden="true">${icon}</span>
      <span class="cmdk-result-body">
        <span class="cmdk-result-label">${highlight(c.label, query)}</span>
        ${desc}
      </span>
      <span class="cmdk-result-trail">
        ${disabled || meta || ''}
        ${shortcut}
      </span>
    </div>`;
}

function highlight(text, query) {
  if (!query) return escapeHTML(text);
  const tokens = query.split(/\s+/).filter(Boolean).map(escapeRegex);
  if (tokens.length === 0) return escapeHTML(text);
  const escaped = escapeHTML(text);
  const re = new RegExp(`(${tokens.join('|')})`, 'gi');
  return escaped.replace(re, '<mark>$1</mark>');
}

function escapeRegex(s) {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

function collectCommands() {
  const out = [];

  // ── First steps ─────────────────────────────────────────────────
  out.push({
    id: 'first.configure',
    section: 'First steps',
    icon: '①',
    label: 'Set up a target',
    description: 'Open the Configure form to enter host, port, protocol, and credentials.',
    keywords: ['target', 'host', 'configure', 'setup', 'getting started'],
    action: () => clickSidebarRow('configure'),
  });
  out.push({
    id: 'first.import',
    section: 'First steps',
    icon: '②',
    label: 'Import an example config',
    description: 'Load a ready-made FTP / FTPS / SFTP config from a JSON file.',
    keywords: ['import', 'example', 'json', 'load'],
    action: () => document.getElementById('importBtn')?.click(),
  });
  out.push({
    id: 'first.run',
    section: 'First steps',
    icon: '③',
    label: 'Start a load run',
    description: 'Fire the Run button. Live throughput + latency stream into the Workbench.',
    shortcut: '⌘↵',
    keywords: ['run', 'start', 'load', 'test', 'fire'],
    action: () => document.querySelector('[data-role="topbar-run"]')?.click() || document.getElementById('startBtn')?.click(),
  });

  // ── Run controls ────────────────────────────────────────────────
  out.push({
    id: 'run.start',
    section: 'Run controls',
    icon: '▶',
    label: 'Start run',
    description: 'POST /api/start with the current Configure form. Replaces any active run.',
    shortcut: '⌘↵',
    keywords: ['start', 'run', 'go', 'fire', 'launch', 'begin'],
    action: () => document.querySelector('[data-role="topbar-run"]')?.click() || document.getElementById('startBtn')?.click(),
  });
  out.push({
    id: 'run.stop',
    section: 'Run controls',
    icon: '■',
    label: 'Stop active run',
    description: 'Cancel the running load test. Reports already on disk are preserved.',
    shortcut: '⌘.',
    keywords: ['stop', 'cancel', 'abort', 'kill', 'end'],
    action: () => document.querySelector('[data-role="topbar-stop"]')?.click() || document.getElementById('stopBtn')?.click(),
  });
  out.push({
    id: 'run.probe',
    section: 'Run controls',
    icon: '◎',
    label: 'Test connection',
    description: 'Probe the target without starting a run. Captures host key / TLS cert for trust.',
    keywords: ['probe', 'test', 'connection', 'verify', 'reach', 'check'],
    action: () => {
      const btn = Array.from(document.querySelectorAll('button')).find((b) => /test\s*connection/i.test(b.textContent || ''));
      btn?.click();
    },
  });

  // ── Configuration ───────────────────────────────────────────────
  out.push({
    id: 'cfg.save-preset',
    section: 'Configuration',
    icon: '⊕',
    label: 'Save current config as preset…',
    description: 'Snapshot the form to localStorage. Passwords are stripped before save.',
    shortcut: '⌘S',
    keywords: ['save', 'preset', 'snapshot', 'store'],
    action: async () => {
      const { promptSavePreset } = await import('./save-preset.js');
      promptSavePreset();
    },
  });
  out.push({
    id: 'cfg.export',
    section: 'Configuration',
    icon: '⬇',
    label: 'Export config (JSON)',
    description: 'Download the current Configure form as JSON. Safe to share — passwords stripped.',
    keywords: ['export', 'download', 'json', 'share'],
    action: () => document.getElementById('exportBtn')?.click(),
  });
  out.push({
    id: 'cfg.import',
    section: 'Configuration',
    icon: '⬆',
    label: 'Import config (JSON)',
    description: 'Load a config file into the Configure form. Wraps in `{config: …}` accepted too.',
    keywords: ['import', 'load', 'upload', 'json'],
    action: () => document.getElementById('importBtn')?.click(),
  });

  const presets = listConfigs();
  if (presets.length === 0) {
    out.push({
      id: 'cfg.no-presets',
      section: 'Configuration',
      icon: 'ℹ',
      label: 'No presets saved yet',
      description: 'Use ⌘S or "Save current config as preset…" to capture the form for reuse.',
      keywords: ['preset', 'empty', 'help'],
      action: async () => {
        const { promptSavePreset } = await import('./save-preset.js');
        promptSavePreset();
      },
    });
  } else {
    presets.slice(0, 8).forEach((cfg, idx) => {
      out.push({
        id: `preset.load.${cfg.id}`,
        section: 'Configuration',
        icon: '◆',
        label: `Load preset → ${cfg.name}`,
        description: presetSummary(cfg),
        meta: cfg.savedAt ? relativeTime(cfg.savedAt) : '',
        keywords: ['load', 'preset', cfg.name],
        featured: idx < 2,
        action: () => {
          if (loadConfig(cfg.id)) pushToast(`Loaded preset “${cfg.name}”`, 'info');
          else pushToast('Preset failed to load', 'error');
        },
      });
    });
    presets.slice(0, 8).forEach((cfg) => {
      out.push({
        id: `preset.delete.${cfg.id}`,
        section: 'Configuration',
        icon: '✕',
        label: `Delete preset → ${cfg.name}`,
        description: 'Remove this preset from localStorage. Cannot be undone.',
        keywords: ['delete', 'remove', 'preset', cfg.name],
        action: () => {
          if (confirm(`Delete preset “${cfg.name}”?`)) {
            removeConfig(cfg.id);
            pushToast(`Deleted preset “${cfg.name}”`, 'info');
          }
        },
      });
    });
  }

  // ── Saved Connections ──────────────────────────────────────────
  let conns = [];
  try { conns = listConns(); } catch {}
  if (conns.length === 0) {
    out.push({
      id: 'conn.empty',
      section: 'Connections',
      icon: 'ℹ',
      label: 'No saved connections yet',
      description: 'Save a host:port:user combo from the Connection panel for quick reuse.',
      keywords: ['connection', 'empty', 'help'],
      action: () => promptSaveConn(),
    });
  } else {
    conns.slice(0, 8).forEach((c, idx) => {
      out.push({
        id: `conn.apply.${c.id}`,
        section: 'Connections',
        icon: '⌥',
        label: `${c.name || c.host} (${c.host}:${c.port})`,
        description: `${c.protocol?.toUpperCase() || 'SFTP'} · user ${c.username || '—'}${c.tls_mode ? ' · TLS ' + c.tls_mode : ''}`,
        meta: c.username || '',
        keywords: ['connection', 'host', c.host, c.username || ''],
        featured: idx < 2,
        action: () => {
          applyConn(c);
          pushToast(`Applied connection ${c.name || c.host}`, 'info');
        },
      });
    });
    conns.slice(0, 8).forEach((c) => {
      out.push({
        id: `conn.delete.${c.id}`,
        section: 'Connections',
        icon: '✕',
        label: `Forget connection → ${c.name || c.host}`,
        description: 'Remove this saved host. The trust store entry stays.',
        keywords: ['delete', 'remove', 'forget', c.host, c.name],
        action: () => {
          if (confirm(`Forget saved connection “${c.name || c.host}”?`)) {
            removeConn(c.id);
            pushToast('Connection forgotten', 'info');
          }
        },
      });
    });
  }

  // ── Recent runs ─────────────────────────────────────────────────
  if (recentRunsCache.length === 0) {
    out.push({
      id: 'runs.empty',
      section: 'Recent runs',
      icon: 'ℹ',
      label: 'No runs yet',
      description: 'Press Start to launch one. Reports persist under reports/ in your data dir.',
      keywords: ['runs', 'empty', 'help', 'history'],
      action: () => document.querySelector('[data-role="topbar-run"]')?.click(),
    });
  } else {
    recentRunsCache.slice(0, 6).forEach((run, idx) => {
      const id = run.id || run.run_id || '';
      const files = run.total_files ?? 0;
      const fail = run.failed_files ?? 0;
      const mbps = run.overall_mbps ?? 0;
      const startedAt = run.started_at || run.startedAt;
      out.push({
        id: `run.view.${id}`,
        section: 'Recent runs',
        icon: fail > 0 ? '⚠' : '✓',
        label: id || 'run',
        description: `${files} file${files === 1 ? '' : 's'} · ${mbps.toFixed(2)} MB/s${fail > 0 ? ` · ${fail} failed` : ''}`,
        meta: startedAt ? relativeTime(startedAt) : '',
        keywords: ['run', 'history', id],
        featured: idx < 2,
        action: () => {
          // Scroll the runs panel into view; the runs-history widget
          // will highlight the matching row on hover/click.
          document.querySelector('[data-component="runs-history"]')?.scrollIntoView({ behavior: 'smooth', block: 'start' });
          pushToast(`Run ${id} — see Runs panel`, 'info');
        },
      });
    });
  }

  // ── View ────────────────────────────────────────────────────────
  for (const theme of ['auto', 'light', 'dark']) {
    out.push({
      id: `view.theme.${theme}`,
      section: 'View',
      icon: theme === 'dark' ? '◐' : theme === 'light' ? '☀' : '◓',
      label: `Theme → ${theme}`,
      description: theme === 'auto' ? 'Follow OS dark/light preference.' : `Pin the UI to the ${theme} theme.`,
      meta: getTheme() === theme ? 'active' : '',
      keywords: ['theme', 'dark', 'light', 'auto', 'appearance'],
      action: () => setTheme(theme),
    });
  }
  out.push({
    id: 'view.sidebar',
    section: 'View',
    icon: '◧',
    label: 'Toggle sidebar',
    description: 'Collapse or expand the left navigation panel.',
    keywords: ['sidebar', 'collapse', 'expand', 'toggle', 'nav'],
    action: () => document.querySelector('[data-role="sidebar-toggle"]')?.click(),
  });

  // ── Security ────────────────────────────────────────────────────
  out.push({
    id: 'sec.trust-panel',
    section: 'Security',
    icon: '🔒',
    label: 'Manage trusted hosts',
    description: 'Open the Trust panel to view + revoke pinned SSH host keys and FTPS leaf certs.',
    keywords: ['trust', 'host', 'key', 'cert', 'fingerprint', 'tofu', 'security'],
    action: () => clickSidebarRow('trust'),
  });
  out.push({
    id: 'sec.cluster',
    section: 'Security',
    icon: '⚙',
    label: 'Open Cluster panel',
    description: 'Manage spawned worker tunnels — fan-out runs across N remote machines via SSH.',
    keywords: ['cluster', 'worker', 'fan-out', 'ssh', 'distributed'],
    action: () => clickSidebarRow('cluster'),
  });

  // ── Help ────────────────────────────────────────────────────────
  out.push({
    id: 'help.tofu',
    section: 'Help',
    icon: '?',
    label: 'How does Trust on First Use work?',
    description: 'First connect captures the server fingerprint to a local store. Future runs verify against it; a *changed* cert/key always refuses (MITM signal).',
    keywords: ['tofu', 'trust', 'first', 'use', 'cert', 'fingerprint', 'help', 'how'],
    action: () => clickSidebarRow('trust'),
  });
  out.push({
    id: 'help.cluster',
    section: 'Help',
    icon: '?',
    label: 'What is cluster mode?',
    description: 'Master process fans out a single run across N worker machines via SSH. Aggregates metrics + reports back to the master UI.',
    keywords: ['cluster', 'fanout', 'distributed', 'ssh', 'worker', 'help'],
    action: () => clickSidebarRow('cluster'),
  });
  out.push({
    id: 'help.fpm',
    section: 'Help',
    icon: '?',
    label: 'What is files-per-minute?',
    description: 'Target rate of new uploads kicked off every second. Effective rate is throttled by parallel_streams + actual upload latency.',
    keywords: ['fpm', 'rate', 'throughput', 'help', 'how'],
    action: () => pushToast('See Configure → Normal load → files_per_minute', 'info'),
  });
  out.push({
    id: 'help.protocol',
    section: 'Help',
    icon: '?',
    label: 'Which protocol should I pick?',
    description: 'SFTP for SSH-tunneled file transfer (most common). FTP for unencrypted. FTPS for TLS — implicit (port 990) for connect-time TLS, explicit (port 21) for AUTH-TLS upgrade.',
    keywords: ['sftp', 'ftp', 'ftps', 'protocol', 'help', 'pick'],
    action: () => pushToast('See Configure → Protocol picker', 'info'),
  });
  out.push({
    id: 'help.report',
    section: 'Help',
    icon: '?',
    label: 'Where are run reports?',
    description: 'CSV per-file + JSON meta land in the reports/ directory. The Runs panel surfaces them; download via /api/report.csv?id=…',
    keywords: ['report', 'csv', 'reports', 'help', 'where'],
    action: () => clickSidebarRow('runs'),
  });

  return out;
}

// ────────────────── helpers ──────────────────

function presetSummary(cfg) {
  const c = cfg.config || cfg;
  const parts = [];
  if (c.protocol) parts.push(c.protocol.toUpperCase());
  if (c.host) parts.push(`${c.host}${c.port ? ':' + c.port : ''}`);
  if (c.files_per_minute) parts.push(`${c.files_per_minute} fpm`);
  return parts.length ? parts.join(' · ') : 'Saved configuration snapshot.';
}

function relativeTime(iso) {
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return '';
  const delta = Date.now() - t;
  const s = Math.floor(delta / 1000);
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  const d = Math.floor(h / 24);
  return `${d}d ago`;
}

function clickSidebarRow(name) {
  const candidates = Array.from(document.querySelectorAll('.shell-sidebar-row'));
  const target = candidates.find((el) => (el.textContent || '').trim().toLowerCase().startsWith(name.toLowerCase()));
  if (target) {
    target.click();
    target.scrollIntoView({ behavior: 'smooth', block: 'start' });
  }
}

function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}
