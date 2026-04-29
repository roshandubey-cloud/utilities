// command-palette.js — α4 of the v0.9.0 redesign.
//
// Cmd+K (Ctrl+K on non-Mac) opens a centred modal with a search input and
// a result list. Each entry is { id, label, hint, action(), section }. The
// palette is populated from:
//   * built-ins: Run, Stop, Test connection, Toggle theme, Toggle sidebar,
//     Export config, Import config, Save current as preset, …
//   * saved configs (each one becomes "Load → <name>")
//   * recent runs (each becomes "View run → <id>", later — α5/β1)
//
// Selection: ↑/↓ navigates, Enter fires, Esc closes. Plain string-includes
// scoring (case-insensitive) — no full Levenshtein because the corpus is
// tiny and operators want predictability.

import { list as listConfigs, save as saveConfig, load as loadConfig, remove as removeConfig } from './saved-configs.js';
import { setTheme, getTheme } from './theme.js';
import { pushToast } from './toast.js';

let backdrop = null;
let panel = null;
let input = null;
let resultsRoot = null;
let visibleResults = [];
let activeIndex = 0;

export function mountCommandPalette() {
  // Attach Cmd+K trigger globally.
  document.addEventListener('keydown', (ev) => {
    if ((ev.metaKey || ev.ctrlKey) && ev.key.toLowerCase() === 'k') {
      ev.preventDefault();
      open();
    } else if (ev.key === 'Escape' && backdrop) {
      ev.preventDefault();
      close();
    }
  });

  // The shell.js stub Cmd+K button delegates here too.
  const tbBtn = document.querySelector('[data-role="topbar-cmdk"]');
  if (tbBtn) {
    tbBtn.addEventListener('click', (ev) => {
      ev.preventDefault();
      open();
    });
  }
}

function open() {
  if (backdrop) return;
  backdrop = document.createElement('div');
  backdrop.className = 'cmdk-backdrop';
  backdrop.dataset.component = 'command-palette';
  backdrop.innerHTML = `
    <div class="cmdk-panel" role="dialog" aria-modal="true" aria-label="Command palette">
      <div class="cmdk-search">
        <span class="cmdk-search-icon" aria-hidden="true">
          <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor"
               stroke-width="1.5" stroke-linecap="round"><circle cx="7" cy="7" r="4.5"/><path d="M10.5 10.5L14 14"/></svg>
        </span>
        <input class="cmdk-input" type="text" placeholder="Type a command or search… (Esc to close)"
               autocomplete="off" spellcheck="false" data-role="input" />
      </div>
      <div class="cmdk-results" data-role="results"></div>
      <div class="cmdk-foot">
        <span><kbd>↑</kbd><kbd>↓</kbd> navigate</span>
        <span><kbd>↵</kbd> run</span>
        <span><kbd>Esc</kbd> close</span>
      </div>
    </div>`;
  backdrop.addEventListener('click', (ev) => {
    if (ev.target === backdrop) close();
  });
  document.body.appendChild(backdrop);
  panel = backdrop.querySelector('.cmdk-panel');
  input = backdrop.querySelector('[data-role="input"]');
  resultsRoot = backdrop.querySelector('[data-role="results"]');

  input.addEventListener('input', () => render(input.value));
  input.addEventListener('keydown', onInputKey);
  input.focus();
  render('');
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
  if (!r) return;
  close();
  try {
    r.action();
  } catch (e) {
    pushToast(`Command failed: ${e.message || e}`, 'error');
  }
}

function render(query) {
  const q = (query || '').toLowerCase().trim();
  const all = collectCommands();
  const filtered = q
    ? all.filter((c) => `${c.label} ${c.section || ''} ${c.hint || ''}`.toLowerCase().includes(q))
    : all;
  visibleResults = filtered;
  activeIndex = 0;
  if (filtered.length === 0) {
    resultsRoot.innerHTML = `<div class="cmdk-empty">No matches.</div>`;
    return;
  }
  // Group by section.
  const groups = new Map();
  for (const c of filtered) {
    const k = c.section || 'Actions';
    if (!groups.has(k)) groups.set(k, []);
    groups.get(k).push(c);
  }
  const html = [];
  let i = 0;
  for (const [section, cmds] of groups) {
    html.push(`<div class="cmdk-section">${escapeHTML(section)}</div>`);
    for (const c of cmds) {
      const idx = i++;
      html.push(`
        <div class="cmdk-result" data-idx="${idx}" data-active="${idx === 0}">
          <span class="cmdk-result-label">${escapeHTML(c.label)}</span>
          ${c.hint ? `<span class="cmdk-result-hint">${escapeHTML(c.hint)}</span>` : ''}
        </div>`);
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

function collectCommands() {
  const out = [];
  // Run controls
  out.push({
    label: 'Start run',
    hint: '⌘↵',
    section: 'Run controls',
    action: () => document.querySelector('[data-role="topbar-run"]')?.click() || document.getElementById('startBtn')?.click(),
  });
  out.push({
    label: 'Stop active run',
    hint: '⌘.',
    section: 'Run controls',
    action: () => document.querySelector('[data-role="topbar-stop"]')?.click() || document.getElementById('stopBtn')?.click(),
  });
  out.push({
    label: 'Test connection',
    hint: 'probe SSH/SFTP without starting a run',
    section: 'Run controls',
    action: () => {
      const btn = Array.from(document.querySelectorAll('button')).find((b) => /test\s*connection/i.test(b.textContent || ''));
      btn?.click();
    },
  });

  // Configuration
  out.push({
    label: 'Save current config as preset…',
    hint: 'localStorage; passwords stripped',
    section: 'Configuration',
    action: () => {
      const name = window.prompt('Name this preset:');
      if (!name || !name.trim()) return;
      const entry = saveConfig(name);
      if (entry) pushToast(`Saved preset “${entry.name}”`, 'success');
    },
  });
  for (const cfg of listConfigs()) {
    out.push({
      label: `Load preset → ${cfg.name}`,
      hint: 'restore form from saved config',
      section: 'Configuration',
      action: () => {
        if (loadConfig(cfg.id)) pushToast(`Loaded preset “${cfg.name}”`, 'info');
        else pushToast('Preset failed to load', 'error');
      },
    });
    out.push({
      label: `Delete preset → ${cfg.name}`,
      hint: 'irreversible',
      section: 'Configuration',
      action: () => {
        if (confirm(`Delete preset “${cfg.name}”?`)) {
          removeConfig(cfg.id);
          pushToast(`Deleted preset “${cfg.name}”`, 'info');
        }
      },
    });
  }
  out.push({
    label: 'Export config (JSON)',
    section: 'Configuration',
    action: () => document.getElementById('exportBtn')?.click(),
  });
  out.push({
    label: 'Import config (JSON)',
    section: 'Configuration',
    action: () => document.getElementById('importBtn')?.click(),
  });

  // Theme
  for (const theme of ['auto', 'light', 'dark']) {
    out.push({
      label: `Theme → ${theme}`,
      hint: getTheme() === theme ? 'currently active' : '',
      section: 'View',
      action: () => setTheme(theme),
    });
  }

  // Sidebar
  out.push({
    label: 'Toggle sidebar',
    section: 'View',
    action: () => document.querySelector('[data-role="sidebar-toggle"]')?.click(),
  });

  // Trust store quick links
  out.push({
    label: 'Manage trusted SSH host keys',
    section: 'Security',
    action: () => {
      const panel = document.querySelector('[data-component="trusted-hosts"]');
      panel?.scrollIntoView({ behavior: 'smooth', block: 'start' });
    },
  });

  return out;
}

function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}
