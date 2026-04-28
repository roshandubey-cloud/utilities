// users-editor.js — structured row editor for SFTP user CSVs.
//
// The legacy UI used a single multi-line textarea per user list, with the
// format: `username,password,pattern1*,pattern2*` per line. That's a power-
// user trap (no password masking, no validation, paste-induced typos).
//
// This editor mounts in front of each existing `textarea.csv-users`, hides
// the textarea via inline display:none, and renders a row-based form (one
// row per user). Every edit serialises back into the legacy textarea so the
// existing buildRequestBody() / saveConfig() / probe code keeps working
// without changes — the textarea is the source of truth for downstream code,
// the editor is the source of truth for the user.

const PATTERN_DEFAULT = '*';

export function mountUsersEditors() {
  document.querySelectorAll('textarea.csv-users').forEach((textarea) => {
    if (!textarea.id) return;
    if (textarea.dataset.userEditorMounted) return;
    textarea.dataset.userEditorMounted = '1';
    initOne(textarea);
  });
}

function initOne(textarea) {
  // Suppress the legacy focus/blur masking on this textarea (we own the value now).
  textarea.style.display = 'none';
  textarea.setAttribute('aria-hidden', 'true');

  const host = document.createElement('div');
  host.className = 'users-editor';
  host.dataset.field = textarea.id;
  textarea.parentNode.insertBefore(host, textarea);

  // Initial state: prefer dataset.raw (legacy mask state) if present, else textarea.value.
  let rows = parseCSV(textarea.dataset.raw || textarea.value || '');
  if (rows.length === 0) rows.push(blankRow());

  // Track which passwords are currently visible (per-row, by index).
  const visible = new Set();

  let pasteMode = false;

  function syncToTextarea() {
    const csv = rows
      .filter((r) => r.user.trim() || r.pass || r.patterns.some((p) => p.trim()))
      .map((r) => {
        const patterns = r.patterns.filter((p) => p.trim());
        return [r.user.trim(), r.pass, ...(patterns.length ? patterns : [PATTERN_DEFAULT])].join(',');
      })
      .join('\n');
    textarea.dataset.raw = csv;
    textarea.value = csv;
    textarea.dataset.editing = '0'; // signal to legacy getCsvRaw it's a non-editing snapshot
    // Trigger 'change' so legacy saveConfig() picks it up.
    textarea.dispatchEvent(new Event('change', { bubbles: true }));
  }

  function render() {
    host.innerHTML = `
      <div class="users-editor-table" role="grid" aria-label="SFTP users">
        <div class="users-editor-head">
          <span>Username</span>
          <span>Password</span>
          <span>File patterns <span class="label-hint">(comma-sep, * = any)</span></span>
          <span class="sr-only">Remove</span>
        </div>
        <div class="users-editor-rows">
          ${rows.map((r, i) => rowHTML(r, i, visible.has(i))).join('')}
        </div>
      </div>
      <div class="users-editor-actions">
        <button type="button" class="btn btn-sm btn-secondary" data-action="add">+ Add user</button>
        <button type="button" class="btn btn-sm btn-ghost" data-action="paste-toggle" aria-expanded="${pasteMode}">${pasteMode ? 'Hide CSV paste' : 'Paste CSV…'}</button>
        <span class="users-editor-meta">${rows.filter(r => r.user.trim()).length} user${rows.filter(r => r.user.trim()).length === 1 ? '' : 's'}</span>
      </div>
      <div class="users-editor-paste" data-role="paste" ${pasteMode ? '' : 'hidden'}>
        <label class="label" for="${textarea.id}_paste">Paste CSV — one user per line: <span class="mono">user,pass,pattern1,pattern2</span></label>
        <textarea id="${textarea.id}_paste" class="textarea" rows="4" placeholder="up1,p,invoice*&#10;up2,p,order*"></textarea>
        <div class="row-tight" style="margin-top:var(--sp-2)">
          <button type="button" class="btn btn-sm btn-secondary" data-action="paste-append">Append rows</button>
          <button type="button" class="btn btn-sm btn-ghost" data-action="paste-replace">Replace all</button>
        </div>
      </div>`;
    wireEvents();
  }

  function wireEvents() {
    // Per-row input changes.
    host.querySelectorAll('[data-row]').forEach((rowEl) => {
      const i = parseInt(rowEl.dataset.row, 10);
      rowEl.querySelector('[data-field="user"]').addEventListener('input', (e) => {
        rows[i].user = e.target.value;
        syncToTextarea();
        updateMeta();
      });
      rowEl.querySelector('[data-field="pass"]').addEventListener('input', (e) => {
        rows[i].pass = e.target.value;
        syncToTextarea();
      });
      rowEl.querySelector('[data-field="patterns"]').addEventListener('input', (e) => {
        rows[i].patterns = e.target.value.split(',').map((s) => s.trim()).filter(Boolean);
        if (rows[i].patterns.length === 0) rows[i].patterns = [PATTERN_DEFAULT];
        syncToTextarea();
      });
      rowEl.querySelector('[data-action="toggle-pass"]').addEventListener('click', (ev) => {
        ev.preventDefault();
        if (visible.has(i)) visible.delete(i);
        else visible.add(i);
        const passInput = rowEl.querySelector('[data-field="pass"]');
        const eyeBtn = rowEl.querySelector('[data-action="toggle-pass"]');
        const isVisible = visible.has(i);
        passInput.type = isVisible ? 'text' : 'password';
        eyeBtn.setAttribute('aria-pressed', String(isVisible));
        eyeBtn.title = isVisible ? 'Hide password' : 'Show password';
      });
      rowEl.querySelector('[data-action="remove"]').addEventListener('click', (ev) => {
        ev.preventDefault();
        rows.splice(i, 1);
        if (rows.length === 0) rows.push(blankRow());
        visible.clear();
        syncToTextarea();
        render();
      });
    });

    host.querySelector('[data-action="add"]').addEventListener('click', (ev) => {
      ev.preventDefault();
      rows.push(blankRow());
      render();
      // Focus the newly-added user field.
      const last = host.querySelector('.users-editor-rows .users-editor-row:last-child [data-field="user"]');
      if (last) last.focus();
    });

    host.querySelector('[data-action="paste-toggle"]').addEventListener('click', (ev) => {
      ev.preventDefault();
      pasteMode = !pasteMode;
      render();
      if (pasteMode) {
        const ta = host.querySelector(`#${textarea.id}_paste`);
        if (ta) ta.focus();
      }
    });

    const appendBtn = host.querySelector('[data-action="paste-append"]');
    const replaceBtn = host.querySelector('[data-action="paste-replace"]');
    if (appendBtn) appendBtn.addEventListener('click', (ev) => {
      ev.preventDefault();
      const ta = host.querySelector(`#${textarea.id}_paste`);
      const fresh = parseCSV(ta.value || '');
      if (fresh.length === 0) return;
      // Drop any all-empty trailing row before appending.
      while (rows.length && !rows[rows.length - 1].user && !rows[rows.length - 1].pass) rows.pop();
      rows.push(...fresh);
      pasteMode = false;
      syncToTextarea();
      render();
    });
    if (replaceBtn) replaceBtn.addEventListener('click', (ev) => {
      ev.preventDefault();
      const ta = host.querySelector(`#${textarea.id}_paste`);
      const fresh = parseCSV(ta.value || '');
      if (fresh.length === 0) return;
      rows = fresh;
      visible.clear();
      pasteMode = false;
      syncToTextarea();
      render();
    });
  }

  function updateMeta() {
    const meta = host.querySelector('.users-editor-meta');
    if (!meta) return;
    const n = rows.filter((r) => r.user.trim()).length;
    meta.textContent = `${n} user${n === 1 ? '' : 's'}`;
  }

  // External callers (e.g. import-config) may rewrite textarea.value programmatically.
  // Watch the textarea for external value changes so the editor stays in sync.
  const reflect = () => {
    const incoming = textarea.dataset.raw || textarea.value || '';
    const incomingParsed = parseCSV(incoming);
    if (csvEqual(rows, incomingParsed)) return;
    rows = incomingParsed.length ? incomingParsed : [blankRow()];
    visible.clear();
    render();
  };
  // Listen for the events the legacy code dispatches when it programmatically writes.
  textarea.addEventListener('users-editor:reflect', reflect);

  render();
  syncToTextarea();
}

// ---------- helpers ----------
function blankRow() { return { user: '', pass: '', patterns: [PATTERN_DEFAULT] }; }

function rowHTML(r, i, passVisible) {
  return `
    <div class="users-editor-row" data-row="${i}" role="row">
      <input class="users-editor-input input" data-field="user" type="text" value="${escapeAttr(r.user)}" placeholder="username" autocomplete="username" spellcheck="false" />
      <div class="users-editor-pass-wrap">
        <input class="users-editor-input input users-editor-pass" data-field="pass" type="${passVisible ? 'text' : 'password'}" value="${escapeAttr(r.pass)}" placeholder="password" autocomplete="new-password" spellcheck="false" />
        <button type="button" class="users-editor-eye" data-action="toggle-pass" aria-pressed="${passVisible}" title="${passVisible ? 'Hide password' : 'Show password'}" aria-label="Toggle password visibility">
          <span class="icon icon-sm" aria-hidden="true">${passVisible ? eyeOffSVG() : eyeOnSVG()}</span>
        </button>
      </div>
      <input class="users-editor-input input" data-field="patterns" type="text" value="${escapeAttr(r.patterns.join(', '))}" placeholder="*" spellcheck="false" />
      <button type="button" class="users-editor-remove" data-action="remove" aria-label="Remove user">
        <span class="icon icon-sm" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round"><path d="M6 6l12 12"/><path d="M18 6L6 18"/></svg></span>
      </button>
    </div>`;
}

function eyeOnSVG()  { return '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round"><path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12z"/><circle cx="12" cy="12" r="3"/></svg>'; }
function eyeOffSVG() { return '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round"><path d="M3 3l18 18"/><path d="M10.5 10.7a2 2 0 002.8 2.8"/><path d="M9.9 5.1A10.5 10.5 0 0112 5c6.5 0 10 7 10 7a18.3 18.3 0 01-2.7 3.6"/><path d="M6.6 6.6A18.3 18.3 0 002 12s3.5 7 10 7c1.5 0 2.8-.3 4-.8"/></svg>'; }

function parseCSV(raw) {
  if (!raw) return [];
  return raw.split(/\r?\n/).map((line) => {
    const trimmed = line.trim();
    if (!trimmed) return null;
    const parts = line.split(',');
    if (parts.length < 2) return null;
    const user = (parts[0] || '').trim();
    const pass = parts[1] || '';
    const patterns = parts.slice(2).map((s) => s.trim()).filter(Boolean);
    return { user, pass, patterns: patterns.length ? patterns : [PATTERN_DEFAULT] };
  }).filter(Boolean);
}

function csvEqual(a, b) {
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) {
    if (a[i].user !== b[i].user) return false;
    if (a[i].pass !== b[i].pass) return false;
    if (a[i].patterns.join(',') !== b[i].patterns.join(',')) return false;
  }
  return true;
}

function escapeAttr(s) { return String(s ?? '').replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;').replace(/>/g, '&gt;'); }
