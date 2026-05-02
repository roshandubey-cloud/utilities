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
let detailRoot = null;
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
      <div class="cmdk-body">
        <div class="cmdk-results" data-role="results"></div>
        <aside class="cmdk-detail" data-role="detail" aria-label="Details for selected entry"></aside>
      </div>
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
  detailRoot = backdrop.querySelector('[data-role="detail"]');

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
  backdrop = panel = input = resultsRoot = detailRoot = null;
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
  renderDetail(visibleResults[activeIndex]);
}

function renderDetail(entry) {
  if (!detailRoot) return;
  if (!entry) {
    detailRoot.innerHTML = '';
    detailRoot.dataset.empty = 'true';
    return;
  }
  if (!entry.detail) {
    // Fall back to a minimal card for non-help entries: icon + label +
    // description + section. Keeps the right pane informative even
    // when the entry is just an action (Start run, Theme→dark, etc.).
    detailRoot.dataset.empty = 'false';
    detailRoot.innerHTML = `
      <div class="cmdk-detail-card cmdk-detail-card--mini">
        <div class="cmdk-detail-icon">${entry.icon || '•'}</div>
        <div class="cmdk-detail-title">${escapeHTML(entry.label)}</div>
        <div class="cmdk-detail-section">${escapeHTML(entry.section || '')}</div>
        <p class="cmdk-detail-lede">${escapeHTML(entry.description || '')}</p>
        ${entry.shortcut ? `<div class="cmdk-detail-shortcut">Shortcut: <kbd>${escapeHTML(entry.shortcut)}</kbd></div>` : ''}
        ${entry.meta ? `<div class="cmdk-detail-meta">${escapeHTML(entry.meta)}</div>` : ''}
      </div>`;
    return;
  }
  // Full guide card: title, lede, body sections (heading + bullet list
  // or paragraph), code blocks, links. Each detail.body[] item is one
  // of: { kind: "p", text }, { kind: "h", text }, { kind: "list", items[] },
  // { kind: "code", lang?, text }, { kind: "kv", rows: [[k,v], …] }.
  detailRoot.dataset.empty = 'false';
  const sections = (entry.detail.body || []).map(renderDetailNode).join('');
  const links = (entry.detail.links || []).map((l) =>
    `<a href="${escapeHTML(l.href)}" target="_blank" rel="noopener noreferrer">${escapeHTML(l.label)}</a>`
  ).join(' · ');
  // "Open related panel" CTA — only when the entry has a meaningful
  // navigation target. Without this the operator might Enter a help
  // row expecting it to take them somewhere; with the detail pane the
  // primary value is reading, so the button makes the secondary
  // navigation explicit and labelled.
  const cta = entry.detail.cta
    ? `<button type="button" class="btn btn-secondary cmdk-detail-cta" data-role="detail-cta">${escapeHTML(entry.detail.cta.label)}</button>`
    : '';
  detailRoot.innerHTML = `
    <article class="cmdk-detail-card">
      <header class="cmdk-detail-head">
        <div class="cmdk-detail-icon">${entry.icon || '?'}</div>
        <div class="cmdk-detail-headtext">
          <div class="cmdk-detail-title">${escapeHTML(entry.detail.title || entry.label)}</div>
          <div class="cmdk-detail-section">${escapeHTML(entry.section || 'Help')}</div>
        </div>
        ${cta}
      </header>
      ${entry.detail.lede ? `<p class="cmdk-detail-lede">${escapeHTML(entry.detail.lede)}</p>` : ''}
      ${sections}
      ${links ? `<footer class="cmdk-detail-links">${links}</footer>` : ''}
    </article>`;
  if (entry.detail.cta) {
    detailRoot.querySelector('[data-role="detail-cta"]').addEventListener('click', (ev) => {
      ev.preventDefault();
      ev.stopPropagation();
      close();
      try { entry.detail.cta.action(); } catch (e) { pushToast(`Action failed: ${e.message || e}`, 'error'); }
    });
  }
}

function renderDetailNode(node) {
  switch (node.kind) {
    case 'h':
      return `<h4 class="cmdk-detail-h">${escapeHTML(node.text)}</h4>`;
    case 'p':
      return `<p class="cmdk-detail-p">${escapeHTML(node.text)}</p>`;
    case 'list':
      return `<ul class="cmdk-detail-list">${(node.items || []).map((i) => `<li>${escapeHTML(i)}</li>`).join('')}</ul>`;
    case 'code':
      return `<pre class="cmdk-detail-code"><code>${escapeHTML(node.text)}</code></pre>`;
    case 'kv':
      return `<dl class="cmdk-detail-kv">${(node.rows || []).map(
        ([k, v]) => `<dt>${escapeHTML(k)}</dt><dd>${escapeHTML(v)}</dd>`
      ).join('')}</dl>`;
    default:
      return '';
  }
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
  renderDetail(visibleResults[activeIndex]);
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
      // Click the Quick-Checks submit button by its stable data-role.
      // The previous textContent regex would happily match any future
      // button whose label contained "test … connection" — fragile.
      const btn = document.querySelector('[data-component="connection"] [data-role="submit"]')
        || document.querySelector('[data-role="submit"]');
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

  // ── Help (deep guides) ─────────────────────────────────────────
  // Each card renders a full right-pane guide via entry.detail. The
  // surface description stays short so the row template doesn't blow
  // out; the right pane carries the long-form content.
  out.push(...helpEntries());

  return out;
}

// helpEntries returns the deep Help guides shown in the right pane.
// Each entry has a short surface description (one line) AND a full
// `detail` block: { title, lede, body: [{kind:..., ...}, ...], links }.
// Body kinds: "h" (heading), "p" (paragraph), "list" (bullet list),
// "kv" (key-value pairs), "code" (preformatted block).
function helpEntries() {
  return [
    {
      id: 'help.tofu',
      section: 'Help',
      icon: '?',
      label: 'Trust on First Use (TOFU) — full reference',
      description: 'Pin server identity on first connect; refuse changed identity afterwards. SSH host keys + FTPS leaf certs.',
      keywords: ['tofu', 'trust', 'first', 'use', 'cert', 'fingerprint', 'host', 'key', 'mitm', 'security'],
      action: () => {},
      detail: {
        title: 'Trust on First Use (TOFU)',
        lede: 'How sftp-loadtest decides whether a remote server is who it claims to be — without a CA, without manual fingerprint paste, but still safer than blind-trust.',
        body: [
          { kind: 'h', text: 'What it is' },
          { kind: 'p', text: 'On the first successful connection to a (host, port) pair, the server\'s fingerprint is recorded in a local trust store. Every subsequent connection verifies against the stored fingerprint. A matching fingerprint is accepted; a changed fingerprint is refused outright — that\'s the MITM-detection signal.' },

          { kind: 'h', text: 'Two stores, two protocols' },
          { kind: 'kv', rows: [
            ['SSH host keys', '<dataDir>/hosts.json — Ed25519 / RSA / ECDSA public keys hashed SHA-256'],
            ['FTPS leaf certs', '<dataDir>/tls-hosts.json — DER-encoded leaf cert hashed SHA-256'],
            ['Data dir (macOS)', '~/Library/Application Support/sftp-loadtest/'],
            ['Data dir (Linux)', '~/.config/sftp-loadtest/ (XDG_CONFIG_HOME)'],
            ['Data dir (Windows)', '%AppData%\\sftp-loadtest\\'],
          ]},

          { kind: 'h', text: 'When TOFU fires' },
          { kind: 'list', items: [
            'Test connection with the TOFU toggle on — pins on success.',
            'Run with tls_trust_on_first_use=true (FTPS) — pins on first dial.',
            'CLI: sftp-loadtest with -known-hosts unset uses the JSON store + TOFU by default.',
            'When the fingerprint already matches the stored one, no UI prompt — just verifies silently.',
          ]},

          { kind: 'h', text: 'When it refuses' },
          { kind: 'list', items: [
            'Stored fingerprint differs from what the server presents → ErrTLSCertChanged / "host key has changed". Run aborts. Operator must explicitly accept the new identity through the UI (Trust panel → Remove + reconnect, OR Probe with accept_changed=true).',
            'No store entry AND TOFU not enabled → refuses with requires_consent so the UI can prompt before pinning.',
          ]},

          { kind: 'h', text: 'When NOT to use TOFU' },
          { kind: 'list', items: [
            'Production deployments where you can pre-distribute fingerprints out-of-band. Pre-pin via the UI Trust panel with a known-good fingerprint, then turn TOFU off.',
            'Targets behind a load balancer that rotates certs frequently. Use a CA chain instead, or pin the LB\'s public key.',
            'Lab environments where any cert is fine — tls_insecure_skip_verify:true is faster (skips the store entirely). DO NOT use against credentials you care about.',
          ]},

          { kind: 'h', text: 'How to manage trust' },
          { kind: 'list', items: [
            'Trust panel (sidebar) lists every stored entry with Add / Remove buttons.',
            'Removing an entry forces the next connection to re-prompt (or re-TOFU if enabled).',
            'A changed fingerprint surfaces a renewal modal showing OLD + NEW side-by-side; only Accept after verifying out-of-band (vendor email, runbook, etc.).',
          ]},

          { kind: 'h', text: 'Threat model' },
          { kind: 'list', items: [
            'TOFU does NOT defend against an attacker who is in-path on the very first connection — they\'ll be the "trusted" identity going forward.',
            'TOFU DOES defend against attackers who appear after first contact. Identity rotation triggers an explicit consent flow.',
            'Combine TOFU with out-of-band fingerprint verification on the first connect for highest assurance — the panel surfaces the SHA-256 in clear text for paste-into-runbook.',
          ]},
        ],
        links: [
          { label: 'OpenSSH known_hosts spec', href: 'https://man.openbsd.org/sshd.8#SSH_KNOWN_HOSTS_FILE_FORMAT' },
          { label: 'docs/security.md', href: '/docs/security.md' },
        ],
        cta: { label: 'Open Trust panel', action: () => clickSidebarRow('trust') },
      },
    },

    {
      id: 'help.cluster',
      section: 'Help',
      icon: '?',
      label: 'Cluster mode — full reference',
      description: 'Master fans out a run across N worker machines via SSH. Aggregates metrics, handles partial failures, version-negotiates.',
      keywords: ['cluster', 'fanout', 'distributed', 'ssh', 'worker', 'spawn', 'master', 'parallel'],
      action: () => {},
      detail: {
        title: 'Cluster mode',
        lede: 'Run the same load test from N machines simultaneously to break past single-host bandwidth or fd-limit ceilings. The master fan-outs config, polls workers, sums metrics; the workers are stock sftp-loadtest binaries spawned on demand.',
        body: [
          { kind: 'h', text: 'Architecture' },
          { kind: 'list', items: [
            'Master = the process you\'re running right now. Owns the UI, the run config, the aggregated metrics.',
            'Workers = unmodified sftp-loadtest binaries running on remote hosts, bound to 127.0.0.1:18081 (loopback only — no public surface).',
            'Reverse SSH tunnel = master accepts on a random local port; every accepted conn forwards through SSH to the worker\'s loopback.',
            'No special build for workers — same binary the user installs locally.',
          ]},

          { kind: 'h', text: 'Spawn protocol (8 steps, surfaced live in the UI)' },
          { kind: 'list', items: [
            '1. ssh-dial — TCP + SSH handshake + auth (password or key).',
            '2. arch-detect — uname -s -m to pick the right release asset.',
            '3. pkill-orphans — defensive reap of any stale worker on the bind port.',
            '4. install — either curl-from-GitHub-release (default) or upload-via-SFTP (offline).',
            '5. smoke — runs <bin> -version to confirm install.',
            '6. spawn-process — nohup <bin> -addr 127.0.0.1:18081 -insecure-host-key.',
            '7. wait-ready — direct-tcpip probe loop until worker accepts.',
            '8. tunnel-listener — opens local 127.0.0.1:0 and starts the accept loop.',
          ]},

          { kind: 'h', text: 'Reliability features' },
          { kind: 'kv', rows: [
            ['SSH KeepAlive', '30 s ping; tunnel auto-closes after 3 fails (~90 s detection window).'],
            ['Version negotiation', 'master /healthz?detail=1 reports its version; cluster status surfaces master_version + per-worker version + version_mismatch flag.'],
            ['Partial failure', 'Stop fans out /api/stop to every worker even if one is unreachable. Sum-metrics tolerate missing workers (Reachable:false).'],
            ['Rollback on Start', 'If any worker rejects /api/start, the master /api/stop\'s the ones that succeeded so you never end up half-running.'],
            ['Worker version mismatch', 'Surfaced as a status flag, NOT a refusal — so a v0.13.10 master + v0.13.7 worker still works for compatible features.'],
          ]},

          { kind: 'h', text: 'Files-per-minute split' },
          { kind: 'p', text: 'When you set fpm=600 for a 4-worker cluster, each worker runs at fpm=150. Other knobs (parallel_streams, sizes, user CSV) replicate verbatim — cluster mode multiplies parallelism, not user diversity.' },

          { kind: 'h', text: 'Add a worker (UI flow)' },
          { kind: 'list', items: [
            'Cluster panel → Add worker → host, SSH port, user, password OR private key PEM.',
            'Preflight runs first: TCP reachability, SSH handshake, arch detect. Surfaces structured errors (auth failed, host unreachable, install path not writable).',
            'After Spawn, the live NDJSON stream shows each step\'s status; failures roll back via Tunnel.Close which kills the remote nohup\'d worker too.',
          ]},

          { kind: 'h', text: 'When NOT to use cluster mode' },
          { kind: 'list', items: [
            'Single-host runs that fit in your local fd limit + bandwidth — adds operational complexity for no win.',
            'Targets that hate concurrent connections from many source IPs (legacy mainframes, some banks). Test single-host first.',
          ]},
        ],
        links: [
          { label: 'docs/security.md (cluster section)', href: '/docs/security.md#cluster' },
        ],
        cta: { label: 'Open Cluster panel', action: () => clickSidebarRow('cluster') },
      },
    },

    {
      id: 'help.fpm',
      section: 'Help',
      icon: '?',
      label: 'Files-per-minute — sizing guide',
      description: 'How fpm interacts with parallel_streams + upload latency. How to size for your target.',
      keywords: ['fpm', 'rate', 'throughput', 'parallel', 'sizing', 'tuning'],
      action: () => {}, // detail pane carries everything; closing the palette is the action
      detail: {
        title: 'files-per-minute (fpm)',
        lede: 'The dispatcher\'s target rate of new uploads kicked off per minute. Whether that target is hit depends on how fast the target absorbs uploads and how many parallel streams you allow per user.',
        body: [
          { kind: 'h', text: 'The relationship' },
          { kind: 'kv', rows: [
            ['fpm', 'Target NEW uploads per minute. Dispatcher fires every 60/fpm seconds.'],
            ['parallel_streams', 'Concurrent uploads per user. Caps how many fpm can land at once.'],
            ['upload_latency_p50', 'How long the average upload takes. fpm × p50 / 60 ≈ steady-state in-flight count.'],
            ['Effective fpm', 'min(fpm, users × parallel_streams × 60 / p50_seconds).'],
          ]},

          { kind: 'h', text: 'Sizing rule of thumb' },
          { kind: 'list', items: [
            'Start with fpm = expected_production_rate × 1.5 (headroom).',
            'Set parallel_streams ≥ ceil(fpm × p50_seconds / 60 / users).',
            'If dispatch_skips climb, you\'re over-driving — either raise parallel_streams or add users.',
            'If overall_mbps stalls below baseline, the bottleneck is server-side (pool exhaustion, fd cap, max-startups, etc.) — not client throughput.',
          ]},

          { kind: 'h', text: 'How dispatcher actually works' },
          { kind: 'p', text: 'Each second, the dispatcher tries to fire ceil(fpm/60) new uploads. If every user\'s pool slots are busy, it skips that tick (counted as dispatch_skips). Skipping doesn\'t fail the run — it just means the requested rate can\'t be sustained against the current target.' },

          { kind: 'h', text: 'Common pitfalls' },
          { kind: 'list', items: [
            'fpm=10000 with 2 users × 4 parallel streams — capped to ~1000 fpm regardless of fpm setting.',
            'fpm=1 with parallel_streams=64 — wasted slots; pool builds up but never drains.',
            'Setting fpm to match average prod load — production has bursts. Use 2–3× peak for stress.',
          ]},
        ],
      },
    },

    {
      id: 'help.protocol',
      section: 'Help',
      icon: '?',
      label: 'Protocol picker — SFTP / FTP / FTPS',
      description: 'When to use each, default ports, security trade-offs, common gotchas.',
      keywords: ['sftp', 'ftp', 'ftps', 'protocol', 'tls', 'implicit', 'explicit', 'auth', 'ssh'],
      action: () => {},
      detail: {
        title: 'Picking the right protocol',
        lede: 'Three wire protocols, three security/performance/portability trade-offs. Pick based on what your target server actually exposes — not what you wish it exposed.',
        body: [
          { kind: 'h', text: 'SFTP — SSH-tunneled file transfer (default, most common)' },
          { kind: 'kv', rows: [
            ['Default port', '22'],
            ['Auth', 'Password or SSH public-key (Ed25519, RSA, ECDSA).'],
            ['Encryption', 'Always — runs over SSH.'],
            ['Identity', 'SSH host key, pinned via TOFU.'],
            ['Use when', 'Your target speaks SSH (vast majority of UNIX-y SFTP servers).'],
          ]},

          { kind: 'h', text: 'FTP — plain, unencrypted' },
          { kind: 'kv', rows: [
            ['Default port', '21'],
            ['Auth', 'Username + password, sent in clear text.'],
            ['Encryption', 'None.'],
            ['Use when', 'Lab tests, internal networks where TLS is enforced upstream, or legacy mainframes that only speak FTP.'],
            ['DO NOT', 'Use against credentials you care about over an untrusted network.'],
          ]},

          { kind: 'h', text: 'FTPS — FTP over TLS' },
          { kind: 'kv', rows: [
            ['Implicit TLS', 'TLS from byte 0. Default port 990. Fastest handshake.'],
            ['Explicit TLS', 'AUTH TLS upgrade on the plain-FTP port (21). Backwards-compatible with FTP-only clients but slower handshake.'],
            ['Identity', 'TLS leaf certificate, pinned via TOFU (tls_trust_on_first_use=true).'],
            ['Use when', 'Your target is on an FTP server that requires TLS — SAP/ EDI systems, banking, HIPAA-bound workloads.'],
          ]},

          { kind: 'h', text: 'Common confusion' },
          { kind: 'list', items: [
            '"SFTP" and "FTPS" sound similar but are unrelated protocols. SFTP = SSH-based; FTPS = FTP+TLS.',
            'Implicit FTPS on port 21 — almost never works. Implicit assumes TLS from connection start; port 21 expects plain FTP. Use 990.',
            'AUTH TLS rejection from server — switch to implicit or check server config (server may ban explicit upgrade for compliance).',
            'TLS handshake failure on FTPS — usually the cert isn\'t trusted. Enable TOFU or use tls_insecure_skip_verify in lab.',
          ]},

          { kind: 'h', text: 'Performance order' },
          { kind: 'p', text: 'Plain FTP > FTPS implicit > FTPS explicit > SFTP. SFTP carries the largest per-file overhead (channel + extension + stat); FTP is barely above raw TCP. For raw-throughput tests, FTP wins; for security-realistic tests, SFTP/FTPS reflect actual production cost.' },
        ],
      },
    },

    {
      id: 'help.report',
      section: 'Help',
      icon: '?',
      label: 'Run reports — schema + retention + access',
      description: 'CSV-per-file + JSON meta layout. Where they live, how to query, how crash-resume works.',
      keywords: ['report', 'csv', 'reports', 'json', 'meta', 'retention', 'history'],
      action: () => {},
      detail: {
        title: 'Run reports',
        lede: 'Two artifacts per run, written atomically to the reports directory. Survive crashes, queryable from the UI or any CSV tool, mode 0600 by default.',
        body: [
          { kind: 'h', text: 'Files per run' },
          { kind: 'kv', rows: [
            ['<reports>/run-<id>.csv', 'One row per finalized upload — track ID, user, size, durations, error code if any.'],
            ['<reports>/run-<id>.json', 'Run metadata — config snapshot, latency histograms, slowdown events, host info, end status.'],
            ['File mode', '0600 (owner read/write only).'],
            ['Reports dir (CLI)', 'Set via -reports-dir; default ./reports.'],
            ['Reports dir (desktop)', '<dataDir>/reports — under your OS user-config dir.'],
          ]},

          { kind: 'h', text: 'CSV columns' },
          { kind: 'list', items: [
            'track_id — uploader-supplied UUID; matches the round-trip back into download.',
            'user — username from CSV.',
            'pattern — filename pattern that produced this upload.',
            'size_bytes — actual file size.',
            'started_at, ended_at — ISO 8601 with timezone.',
            'upload_ms, dial_ms, list_ms — per-stage durations.',
            'error_code — one of the stable ErrorCode values (auth, refused, timeout, ...) or empty on success.',
          ]},

          { kind: 'h', text: 'JSON meta highlights' },
          { kind: 'list', items: [
            'metrics.start_at / end_at — wall-clock bracket.',
            'metrics.total_files / total_bytes — aggregates.',
            'metrics.overall_mbps / per_minute[] — throughput timeseries.',
            'latency.upload / upload_cor — count + p50/p95/p99/p99.9/max/mean (corrected variant accounts for queue wait).',
            'errors_by_code — failure breakdown.',
            'disabled_users — auto-disabled users (consecutive-failure circuit).',
            'host_info — go_version, num_cpu, total_ram_mb, fd_limit.',
          ]},

          { kind: 'h', text: 'Crash resume' },
          { kind: 'p', text: 'The CSV writer streams rows to disk as uploads finalize (RAM stays flat on long runs). If the process dies mid-run, the CSV survives but the JSON meta won\'t exist. On next launch the runner detects "interrupted" runs and synthesizes stub metas (interrupted:true) so the Runs panel doesn\'t lose the history.' },

          { kind: 'h', text: 'Access from outside the UI' },
          { kind: 'list', items: [
            'GET /api/runs — JSON list of all runs.',
            'GET /api/report.csv?id=<run-id> — full CSV download.',
            'Files are plain CSV/JSON — open in Excel, jq, pandas, anything.',
          ]},

          { kind: 'h', text: 'Cleaning up' },
          { kind: 'p', text: 'Reports never auto-delete — disk usage is on the operator. Safe to remove ad-hoc with `rm reports/run-*` between runs. The /api/runs list rebuilds from the directory on each call.' },
        ],
        cta: { label: 'Open Runs panel', action: () => clickSidebarRow('runs') },
      },
    },

    {
      id: 'help.latency',
      section: 'Help',
      icon: '?',
      label: 'Reading latency percentiles (p50 / p95 / p99 / cor)',
      description: 'What each percentile means, why corrected latency exists, and how to spot bimodal distributions.',
      keywords: ['latency', 'percentile', 'p50', 'p95', 'p99', 'p99.9', 'cor', 'tail', 'distribution'],
      action: () => {},
      detail: {
        title: 'Latency percentiles',
        lede: 'Mean is a lie. Tail latency is what your users feel. Read p99 first, p50 second, mean almost never.',
        body: [
          { kind: 'h', text: 'Percentile definitions' },
          { kind: 'kv', rows: [
            ['p50', 'Median — half the uploads finished in ≤ this time. Useful baseline; ignore for SLA.'],
            ['p95', 'Common SLA target. 5% of uploads were slower than this.'],
            ['p99', 'Tail. The 1-in-100 slowest upload. Shows server pool exhaustion, GC pauses, network spikes.'],
            ['p99.9', 'Long tail. The 1-in-1000. Often hides bimodal distributions and JVM stop-the-world pauses.'],
            ['mean', 'Distorted by tail. NEVER use as the sole indicator.'],
            ['max', 'Worst single upload. Use to bound tail-of-tail.'],
          ]},

          { kind: 'h', text: 'Two latency views' },
          { kind: 'kv', rows: [
            ['upload', 'end - actual_start. Pure transfer time as observed by the client. What the SFTP server "saw".'],
            ['upload_cor', 'end - intended_start. Includes queue-wait when the dispatcher had to skip. Closes the coordinated-omission gap — what your USERS would feel.'],
          ]},
          { kind: 'p', text: 'When upload_cor is much higher than upload, your dispatch is queuing — fpm is set higher than the system can sustain. Either raise parallelism or lower fpm.' },

          { kind: 'h', text: 'Bimodal distribution detection' },
          { kind: 'list', items: [
            'p50 stable but p95/p99 spiking → tail cliff. Look at slowdowns metric; check server-side pool / fd limits.',
            'p50 and p99 both rising → systemic slowdown. Network or whole-server contention.',
            'p99.9 >> p99 (10×+) → hot stalls. JVM GC, disk-cache miss, lock contention. Often invisible in p99.',
          ]},

          { kind: 'h', text: 'Reasonable SFTP latency baselines (1 MB file, LAN)' },
          { kind: 'kv', rows: [
            ['p50', '5 – 25 ms'],
            ['p95', '50 – 150 ms'],
            ['p99', '100 – 500 ms'],
            ['p99.9', '500 ms – 5 s'],
          ]},
        ],
      },
    },

    {
      id: 'help.schedule',
      section: 'Help',
      icon: '?',
      label: 'Scheduling runs — when + how it survives restarts',
      description: 'Wall-clock-scheduled runs. Persists across process restarts. Crashes resume to the next slot.',
      keywords: ['schedule', 'cron', 'wall-clock', 'persist', 'restart', 'timer'],
      action: () => {},
      detail: {
        title: 'Scheduled runs',
        lede: 'Pick a wall-clock time, the run kicks off then. Survives process restarts (the schedule store is on-disk JSON). Useful for off-hours stress tests, partner-window EDI flows, and overnight soaks.',
        body: [
          { kind: 'h', text: 'How it works' },
          { kind: 'list', items: [
            'POST /api/schedule with { run_at: ISO8601, config: {...} } persists a JSON file under <schedules>.',
            'A 5-second ticker wakes up, checks for due schedules, atomically marks them "running" before firing.',
            'Firing = same code path as /api/start, started_by="schedule" badge on the run.',
            'Survives crashes — restart picks up where the ticker left off; schedules in the past fire immediately.',
          ]},

          { kind: 'h', text: 'Where they live' },
          { kind: 'kv', rows: [
            ['Schedules dir (CLI)', 'Set via -schedules-dir. Empty string disables scheduling entirely.'],
            ['Schedules dir (desktop)', '<dataDir>/schedules.'],
            ['File format', 'One JSON file per schedule, content is the same shape /api/start consumes.'],
          ]},

          { kind: 'h', text: 'Cancel + list' },
          { kind: 'list', items: [
            'GET /api/schedules — list pending.',
            'POST /api/schedule/cancel?id=<id> — mark as cancelled (file stays for audit).',
            'Schedule panel in the sidebar shows everything — pending, fired, cancelled, missed.',
          ]},

          { kind: 'h', text: 'Limitations (intentional)' },
          { kind: 'list', items: [
            'Wall-clock only — no cron expressions. Use external cron + curl /api/start for repeating schedules.',
            'No timezone field on the wire — convert to UTC ISO before posting (the UI does this for you).',
            'Single concurrent run — a scheduled fire at 02:00 will fail if a run is already active. The ticker logs the conflict; the schedule is marked "missed".',
          ]},
        ],
        cta: { label: 'Open Schedule panel', action: () => clickSidebarRow('schedule') },
      },
    },

    {
      id: 'help.api',
      section: 'Help',
      icon: '?',
      label: 'HTTP API — every endpoint, what it does',
      description: 'Full reference for the /api surface. Useful for headless / CI integration.',
      keywords: ['api', 'http', 'endpoint', 'curl', 'json', 'integration', 'headless'],
      action: () => {},
      detail: {
        title: 'HTTP API reference',
        lede: 'Every UI button is a thin wrapper over this surface. JSON in, JSON out. CSRF-guarded via X-Requested-With:sftp-loadtest header.',
        body: [
          { kind: 'h', text: 'Run lifecycle' },
          { kind: 'kv', rows: [
            ['POST /api/start', 'Start a run. Body = run config JSON. Returns { run_id }.'],
            ['POST /api/stop', 'Cancel the active run. Reports already on disk are kept.'],
            ['GET /api/status', 'Live metrics for the active or last run — counters, latency, throughput, disabled users.'],
            ['POST /api/probe', 'Pre-flight host-key/cert-trust + auth. Body = { host, port, protocol, username, password, trust_on_first_use? }.'],
          ]},

          { kind: 'h', text: 'History + reports' },
          { kind: 'kv', rows: [
            ['GET /api/runs', 'JSON list of historical runs.'],
            ['GET /api/report.csv?id=<run-id>', 'Per-file CSV.'],
          ]},

          { kind: 'h', text: 'Trust stores' },
          { kind: 'kv', rows: [
            ['GET /api/hostkeys', 'List pinned SSH host keys.'],
            ['POST /api/hostkeys/remove', 'Forget a SSH host-key entry.'],
            ['(FTPS cert store)', 'Same shape via the trust handlers — surfaced in the Trust panel UI.'],
          ]},

          { kind: 'h', text: 'Cluster' },
          { kind: 'kv', rows: [
            ['POST /api/cluster/start', 'Fan-out the unified config to N workers. Body = { workers: [...], config: {...} }.'],
            ['GET /api/cluster/status', 'Aggregated metrics + per-worker reachability + master_version + version_mismatch flag.'],
            ['POST /api/cluster/stop', 'Stop every worker.'],
            ['POST /api/worker/spawn', 'SSH-bootstrap a fresh worker on a remote host.'],
            ['POST /api/worker/preflight', 'Read-only reachability + arch detect.'],
            ['POST /api/worker/despawn', 'Tear down a previously spawned worker.'],
            ['GET  /api/worker/spawned', 'List currently-tunneled workers.'],
            ['POST /api/worker/probe', 'Master-side proxy probe of a worker URL.'],
          ]},

          { kind: 'h', text: 'Schedules' },
          { kind: 'kv', rows: [
            ['POST /api/schedule', 'Body = { run_at, note, config }. Returns id.'],
            ['GET  /api/schedules', 'List pending + fired + cancelled.'],
            ['POST /api/schedule/cancel?id=<id>', 'Mark a schedule cancelled.'],
          ]},

          { kind: 'h', text: 'Liveness + host info' },
          { kind: 'kv', rows: [
            ['GET /healthz', '{ status: "ok" } — for k8s / monitor probes.'],
            ['GET /healthz?detail=1', 'Adds version, uptime, active-run flag. AUTH-gated.'],
            ['GET /api/host', 'Hostname, OS, arch, Go version, CPU count, RAM, FD limits, network interfaces.'],
          ]},

          { kind: 'h', text: 'Headers + auth' },
          { kind: 'list', items: [
            'CSRF guard requires X-Requested-With: sftp-loadtest on every mutating call.',
            'Basic auth optional via -auth-user + -auth-pass at startup.',
            'Body size hard-capped per endpoint (configurable via internal/web/security.go).',
            'Rate-limited via token-bucket per IP — configurable via -trust-proxy CIDRs to honour X-Forwarded-For.',
          ]},
        ],
        links: [
          { label: 'README §HTTP API', href: '/README.md#http-api' },
          { label: 'docs/security.md', href: '/docs/security.md' },
        ],
      },
    },

    {
      id: 'help.tuning',
      section: 'Help',
      icon: '?',
      label: 'Performance tuning — fd limits, parallel streams, memory',
      description: 'OS knobs (ulimit -n, ephemeral ports), tool knobs (parallel_streams, fpm), memory bounds.',
      keywords: ['tune', 'tuning', 'performance', 'ulimit', 'fd', 'memory', 'optimize'],
      action: () => {},
      detail: {
        title: 'Performance tuning',
        lede: 'sftp-loadtest is bottlenecked by file descriptors first, network second, CPU third. Tune in that order.',
        body: [
          { kind: 'h', text: 'File-descriptor limits' },
          { kind: 'list', items: [
            'Each SFTP stream = 1 fd. parallel_streams × users × 2 (read + write loops) is a good rule.',
            'macOS soft default: 256. Linux soft default: 1024. Both are too low for serious load.',
            'sftp-loadtest auto-raises to 4096 at startup; surface in /api/host as fd_limit_soft.',
            'For 10k+ concurrent: ulimit -n 65536 in the launching shell.',
            'launchd / systemd users: set LimitNOFILE in the unit/plist.',
          ]},

          { kind: 'h', text: 'Ephemeral ports' },
          { kind: 'list', items: [
            'Each upload = 1 outbound TCP. Default Linux range = 32768–60999 (~28k ports).',
            'High-concurrency runs hit "cannot assign requested address" before fd limits.',
            'sysctl net.ipv4.ip_local_port_range = "10000 65535" widens it.',
            'TIME_WAIT recycling: net.ipv4.tcp_tw_reuse=1 helps.',
          ]},

          { kind: 'h', text: 'Tool-side knobs' },
          { kind: 'kv', rows: [
            ['parallel_streams', 'Concurrent uploads per user. Raise until dispatch_skips drops; stop when latency tail rises.'],
            ['files_per_minute', 'Target rate. Cap at min(server_capacity, parallel_streams × users × 60 / p50_seconds).'],
            ['poll_seconds', 'Track-ID watcher poll cadence. Lower = faster round-trip detection at cost of fd churn.'],
            ['max_consecutive_failures', 'Per-user circuit breaker. 3 is a reasonable default; 0 disables.'],
            ['parallel_streams (download)', 'Separate from upload — usually equal or 1× uploads.'],
          ]},

          { kind: 'h', text: 'Memory' },
          { kind: 'list', items: [
            'Idle: ~8 MB RSS.',
            'High concurrency: target <350 MB RSS.',
            'Streaming CSV writer flushes finalized rows to disk — RAM stays flat regardless of run length.',
            'Latency histograms are fixed-memory log-bucket accumulators (no per-sample allocation).',
            'If you see RAM growth: check disabled_users — auto-disable circuit prevents fd leaks from runaway pools.',
          ]},

          { kind: 'h', text: 'Server-side' },
          { kind: 'list', items: [
            'OpenSSH default MaxStartups = 10:30:100 — load tests trigger throttling fast. Tune for your workload.',
            'OpenSSH ClientAliveInterval / ServerAliveInterval — drop to 60 from default 300 to keep SSH KeepAlive responsive.',
            'For SFTP-only: switch sshd_config from sftp-server to internal-sftp for ~30% lower per-file overhead.',
          ]},
        ],
      },
    },

    {
      id: 'help.security',
      section: 'Help',
      icon: '?',
      label: 'Security posture — what protects this tool',
      description: 'CSRF, rate-limit, body cap, OWASP headers, host-key TOFU, FTPS cert TOFU.',
      keywords: ['security', 'csrf', 'rate', 'limit', 'headers', 'csp', 'auth', 'audit'],
      action: () => {},
      detail: {
        title: 'Security posture',
        lede: 'sftp-loadtest is a load-testing tool; its threat model is "operator runs against their own infra" with optional "shared lab box". Defenses tilt toward defaults that fail safe.',
        body: [
          { kind: 'h', text: 'Defaults' },
          { kind: 'list', items: [
            'Bind: 127.0.0.1:8080 — loopback only. -addr 0.0.0.0 must be set explicitly.',
            'Auth: off by default. -auth-user + -auth-pass enables HTTP Basic across every endpoint.',
            'CSRF guard: every mutating endpoint requires X-Requested-With: sftp-loadtest header.',
            'Rate limit: token-bucket per IP on expensive endpoints (probe, start, schedule).',
            'Body size cap: per-endpoint hard limit; oversized POSTs return 413.',
          ]},

          { kind: 'h', text: 'Headers (set on every response)' },
          { kind: 'list', items: [
            'Content-Security-Policy: default-src \'self\'; script-src \'self\'; …',
            'X-Frame-Options: DENY.',
            'X-Content-Type-Options: nosniff.',
            'Referrer-Policy: no-referrer.',
            'Cache-Control: no-store on static assets — forces WKWebView to refetch after a binary upgrade.',
          ]},

          { kind: 'h', text: 'Identity stores' },
          { kind: 'list', items: [
            'SSH host keys: <dataDir>/hosts.json. JSON, mode 0600, atomic writes.',
            'FTPS leaf certs: <dataDir>/tls-hosts.json. Same shape, parallel store.',
            'Both stores are the source of truth in store mode (default for desktop + CLI without -known-hosts).',
            '-insecure-host-key flag bypasses host-key verification entirely. Logs a warning on every startup.',
          ]},

          { kind: 'h', text: 'Pprof' },
          { kind: 'p', text: '-debug exposes /debug/pprof. The flag refuses to mount on non-loopback bind addresses — a heap dump includes plaintext credentials from the in-memory RunConfig, so a public pprof endpoint would be a credential disclosure.' },

          { kind: 'h', text: 'Reports' },
          { kind: 'list', items: [
            'Mode 0600 (owner only).',
            'Passwords are stripped from exported configs but PRESENT in the in-memory RunConfig (used for live dials). Don\'t share the binary heap with untrusted parties.',
          ]},
        ],
        links: [
          { label: 'docs/security.md', href: '/docs/security.md' },
          { label: 'SECURITY.md (disclosure)', href: '/SECURITY.md' },
        ],
        cta: { label: 'Open Trust panel', action: () => clickSidebarRow('trust') },
      },
    },

    {
      id: 'help.cluster-spawn',
      section: 'Help',
      icon: '?',
      label: 'Spawning workers via SSH — what each step does',
      description: '8-step protocol: dial → arch → reap → install → smoke → spawn → wait → tunnel.',
      keywords: ['spawn', 'cluster', 'worker', 'ssh', 'install', 'protocol', 'steps'],
      action: () => {},
      detail: {
        title: 'Worker spawn protocol',
        lede: 'How the master takes a (host, user, password) tuple and ends up with a sftp-loadtest worker on the other side, ready for fan-out.',
        body: [
          { kind: 'h', text: 'Step 1 — ssh-dial' },
          { kind: 'p', text: 'TCP connect, SSH handshake, auth (password OR public-key). 15 s timeout. Failures here are usually firewall, wrong port, wrong creds, or sshd not running.' },

          { kind: 'h', text: 'Step 2 — arch-detect' },
          { kind: 'p', text: 'Runs `uname -s -m` to map the remote to a release asset suffix (linux-amd64, linux-arm64, darwin-arm64, darwin-amd64). Unknown arch aborts with a clear message.' },

          { kind: 'h', text: 'Step 3 — pkill-orphans' },
          { kind: 'p', text: 'Defensive `pkill -f "sftp-loadtest -addr 127.0.0.1:18081"` to clean up any worker left behind by a previous master that didn\'t shut down cleanly. Non-zero exit is normal (no matches).' },

          { kind: 'h', text: 'Step 4 — install (download or upload)' },
          { kind: 'list', items: [
            'download: curl + unzip from GitHub releases. Needs egress on the remote.',
            'upload: SFTP-stream the local binary. No egress needed; bandwidth-heavy on the SSH session.',
            'macOS: install path is <home>/sftp-loadtest (Gatekeeper sometimes SIGKILLs binaries written to /tmp). All other Unix: /tmp/sftp-loadtest.',
          ]},

          { kind: 'h', text: 'Step 5 — smoke' },
          { kind: 'p', text: 'Runs `<bin> -version`. Falls back to `<bin> -h` if -version isn\'t supported (older binaries). A clean exit + "sftp-loadtest" mention in output is enough.' },

          { kind: 'h', text: 'Step 6 — spawn-process' },
          { kind: 'p', text: '`nohup <bin> -addr 127.0.0.1:18081 -insecure-host-key > /tmp/sftp-loadtest.log 2>&1 &`. Detaches via nohup; worker only ever binds loopback (no external surface). Stdout + stderr go to /tmp/sftp-loadtest.log so the master can fetch on demand.' },

          { kind: 'h', text: 'Step 7 — wait-ready' },
          { kind: 'p', text: 'Direct-tcpip dial loop against 127.0.0.1:18081 through the SSH session. 5 s budget. Fails when the worker crashed during startup — operator should fetch /tmp/sftp-loadtest.log over SSH for the real error.' },

          { kind: 'h', text: 'Step 8 — tunnel-listener' },
          { kind: 'p', text: 'Master opens 127.0.0.1:0 locally, accepts on it, forwards every conn through the SSH session\'s direct-tcpip channel to the worker\'s loopback. The cluster coordinator now treats the local URL as a normal worker URL.' },

          { kind: 'h', text: 'Cleanup' },
          { kind: 'p', text: 'Tunnel.Close stops accepting, runs the same pkill the spawn step did, closes SSH. Idempotent. Master shutdown calls closeAllSpawned which iterates every active tunnel.' },
        ],
        cta: { label: 'Open Cluster panel', action: () => clickSidebarRow('cluster') },
      },
    },
  ];
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
  // Match by stable data-view attribute, not textContent prefix. The
  // textContent matcher would silently match a future row whose label
  // happens to start with the same word ("Trust this cert…" vs the
  // "Trust" panel) and route the click to the wrong panel.
  const target = document.querySelector(`.shell-sidebar-row[data-view="${name.toLowerCase()}"]`);
  if (target) {
    target.click();
    target.scrollIntoView({ behavior: 'smooth', block: 'start' });
  }
}

function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}
