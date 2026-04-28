// users-editor.js — structured row editor for SFTP user CSVs.
//
// Layout per editor:
//   1. One sample row (pattern chips, password eye) — light-styled when
//      blank to read as optional. Most operators paste a CSV; the row is
//      a one-off entry path or a starting template.
//   2. Always-visible paste textarea — typing or pasting into it parses
//      every line on each keystroke and replaces the rows above. No
//      explicit "Append rows / Replace all" buttons.
//   3. + Add user / Show N more users / counter.
//
// The legacy `textarea.csv-users` stays hidden as the serialisation buffer
// — buildRequestBody() / saveConfig() / probe still read it — and the row
// form / paste textarea push their state into it on every change.

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
  textarea.style.display = 'none';
  textarea.setAttribute('aria-hidden', 'true');

  const host = document.createElement('div');
  host.className = 'users-editor';
  host.dataset.field = textarea.id;
  textarea.parentNode.insertBefore(host, textarea);

  let rows = parseCSV(textarea.dataset.raw || textarea.value || '');
  if (rows.length === 0) rows.push(blankRow());

  const visible = new Set();
  let compactMode = rows.length > 1;
  let suppressPasteSync = false; // guard against re-parse loops when we
                                 // populate the paste textarea ourselves

  function syncToTextarea() {
    const csv = rows
      .filter((r) => r.user.trim() || r.pass || r.patterns.some((p) => p.trim()))
      .map((r) => {
        const patterns = r.patterns.map((p) => p.trim()).filter(Boolean);
        return [r.user.trim(), r.pass, ...(patterns.length ? patterns : [PATTERN_DEFAULT])].join(',');
      })
      .join('\n');
    textarea.dataset.raw = csv;
    textarea.value = csv;
    textarea.dataset.editing = '0';
    textarea.dispatchEvent(new Event('change', { bubbles: true }));
  }

  function isSampleRow(r) {
    return !r.user.trim() && !r.pass && (
      r.patterns.length === 0 ||
      (r.patterns.length === 1 && r.patterns[0] === PATTERN_DEFAULT)
    );
  }

  function render() {
    const visibleRows = (compactMode && rows.length > 1) ? rows.slice(0, 1) : rows;
    const hiddenCount = rows.length - visibleRows.length;

    host.innerHTML = `
      <div class="users-editor-table" role="grid" aria-label="SFTP users">
        <div class="users-editor-head">
          <span>Username</span>
          <span>Password</span>
          <span>File patterns <span class="label-hint">— <code>*</code> matches anything; press Enter or comma to add another</span></span>
          <span class="sr-only">Remove</span>
        </div>
        <div class="users-editor-rows">
          ${visibleRows.map((r, i) => rowHTML(r, i, visible.has(i), rows.length === 1 && isSampleRow(r))).join('')}
        </div>
        ${hiddenCount > 0 ? `
          <button type="button" class="users-editor-expand" data-action="expand">
            Show ${hiddenCount} more user${hiddenCount === 1 ? '' : 's'}
          </button>` : ''}
      </div>

      <div class="users-editor-paste-zone">
        <label class="label users-editor-paste-label" for="${textarea.id}_paste">
          Or paste / bulk-edit CSV — one user per line: <span class="mono">user,pass,pattern1,pattern2,…</span>
        </label>
        <textarea id="${textarea.id}_paste" class="textarea users-editor-paste-textarea" rows="3" spellcheck="false" placeholder="up1,p,invoice*&#10;up2,p,quote*,statement*">${escapeHTML(currentCSV(rows))}</textarea>
      </div>

      <div class="users-editor-actions">
        <button type="button" class="btn btn-sm btn-secondary" data-action="add">+ Add user</button>
        <span class="users-editor-meta">${countLabel(rows)}</span>
      </div>`;
    wireEvents();
  }

  function wireEvents() {
    host.querySelectorAll('[data-row]').forEach((rowEl) => {
      const i = parseInt(rowEl.dataset.row, 10);

      rowEl.querySelector('[data-field="user"]').addEventListener('input', (e) => {
        rows[i].user = e.target.value;
        rowEl.classList.toggle('is-sample', rows.length === 1 && isSampleRow(rows[i]));
        syncToTextarea();
        updatePasteAndMeta();
      });
      rowEl.querySelector('[data-field="pass"]').addEventListener('input', (e) => {
        rows[i].pass = e.target.value;
        rowEl.classList.toggle('is-sample', rows.length === 1 && isSampleRow(rows[i]));
        syncToTextarea();
        updatePasteAndMeta();
      });
      rowEl.querySelector('[data-action="toggle-pass"]').addEventListener('click', (ev) => {
        ev.preventDefault();
        if (visible.has(i)) visible.delete(i); else visible.add(i);
        const passInput = rowEl.querySelector('[data-field="pass"]');
        const eyeBtn = rowEl.querySelector('[data-action="toggle-pass"]');
        const isVisible = visible.has(i);
        passInput.type = isVisible ? 'text' : 'password';
        eyeBtn.setAttribute('aria-pressed', String(isVisible));
        eyeBtn.title = isVisible ? 'Hide password' : 'Show password';
        eyeBtn.querySelector('.icon').innerHTML = isVisible ? eyeOffSVG() : eyeOnSVG();
      });
      rowEl.querySelector('[data-action="remove-row"]').addEventListener('click', (ev) => {
        ev.preventDefault();
        rows.splice(i, 1);
        if (rows.length === 0) rows.push(blankRow());
        visible.clear();
        syncToTextarea();
        render();
      });

      rowEl.querySelectorAll('[data-action="remove-pattern"]').forEach((btn) => {
        btn.addEventListener('click', (ev) => {
          ev.preventDefault();
          const idx = parseInt(btn.dataset.patternIdx, 10);
          rows[i].patterns.splice(idx, 1);
          if (rows[i].patterns.length === 0) rows[i].patterns.push(PATTERN_DEFAULT);
          syncToTextarea();
          render();
          host.querySelector(`[data-row="${i}"] [data-field="pattern-input"]`)?.focus();
        });
      });

      const patInput = rowEl.querySelector('[data-field="pattern-input"]');
      if (patInput) {
        patInput.addEventListener('keydown', (ev) => {
          if (ev.key === 'Enter' || ev.key === ',') {
            ev.preventDefault();
            commitPattern(i, patInput);
          } else if (ev.key === 'Backspace' && patInput.value === '') {
            if (rows[i].patterns.length > 1) {
              rows[i].patterns.pop();
              syncToTextarea();
              render();
              host.querySelector(`[data-row="${i}"] [data-field="pattern-input"]`)?.focus();
            }
          }
        });
        patInput.addEventListener('blur', () => {
          if (patInput.value.trim()) commitPattern(i, patInput);
        });
      }
    });

    host.querySelector('[data-action="add"]').addEventListener('click', (ev) => {
      ev.preventDefault();
      rows.push(blankRow());
      compactMode = false;
      render();
      host.querySelector('.users-editor-rows .users-editor-row:last-child [data-field="user"]')?.focus();
    });

    host.querySelector('[data-action="expand"]')?.addEventListener('click', (ev) => {
      ev.preventDefault();
      compactMode = false;
      render();
    });

    // Paste-zone textarea: typing / pasting parses every line on each
    // keystroke and replaces the row state. No explicit append/replace
    // button — what you see in the textarea is what's saved.
    const pasteEl = host.querySelector('.users-editor-paste-textarea');
    if (pasteEl) {
      pasteEl.addEventListener('input', () => {
        if (suppressPasteSync) return;
        const fresh = parseCSV(pasteEl.value || '');
        rows = fresh.length > 0 ? fresh : [blankRow()];
        compactMode = rows.length > 1;
        visible.clear();
        syncToTextarea();
        render();
        // Restore focus + caret on the paste textarea after re-render.
        const newPaste = host.querySelector('.users-editor-paste-textarea');
        if (newPaste) {
          newPaste.focus();
          newPaste.setSelectionRange(newPaste.value.length, newPaste.value.length);
        }
      });
    }
  }

  // Update the paste textarea + counter without a full re-render. Used
  // when the user is editing rows in the form: the paste textarea
  // mirrors the current CSV so both views stay coherent.
  function updatePasteAndMeta() {
    suppressPasteSync = true;
    const pasteEl = host.querySelector('.users-editor-paste-textarea');
    if (pasteEl) pasteEl.value = currentCSV(rows);
    const meta = host.querySelector('.users-editor-meta');
    if (meta) meta.textContent = countLabel(rows);
    suppressPasteSync = false;
  }

  function commitPattern(rowIdx, input) {
    const value = (input.value || '').trim();
    if (!value) return;
    const newOnes = value.split(',').map((s) => s.trim()).filter(Boolean);
    if (newOnes.length === 0) return;
    if (rows[rowIdx].patterns.length === 1 && rows[rowIdx].patterns[0] === PATTERN_DEFAULT) {
      rows[rowIdx].patterns = [];
    }
    rows[rowIdx].patterns.push(...newOnes);
    input.value = '';
    syncToTextarea();
    render();
    host.querySelector(`[data-row="${rowIdx}"] [data-field="pattern-input"]`)?.focus();
  }

  textarea.addEventListener('users-editor:reflect', () => {
    const incoming = textarea.dataset.raw || textarea.value || '';
    const incomingParsed = parseCSV(incoming);
    if (csvEqual(rows, incomingParsed)) return;
    rows = incomingParsed.length ? incomingParsed : [blankRow()];
    compactMode = rows.length > 1;
    visible.clear();
    render();
  });

  render();
  syncToTextarea();
}

// ---------- helpers ----------
function blankRow() { return { user: '', pass: '', patterns: [PATTERN_DEFAULT] }; }

function countLabel(rows) {
  const n = rows.filter((r) => r.user.trim()).length;
  return `${n} user${n === 1 ? '' : 's'}`;
}

function currentCSV(rows) {
  return rows
    .filter((r) => r.user.trim() || r.pass || r.patterns.some((p) => p.trim()))
    .map((r) => {
      const patterns = r.patterns.map((p) => p.trim()).filter(Boolean);
      return [r.user.trim(), r.pass, ...(patterns.length ? patterns : [PATTERN_DEFAULT])].join(',');
    })
    .join('\n');
}

function rowHTML(r, i, passVisible, isSample) {
  return `
    <div class="users-editor-row${isSample ? ' is-sample' : ''}" data-row="${i}" role="row">
      <input class="users-editor-input input" data-field="user" type="text" value="${escapeAttr(r.user)}" placeholder="username" autocomplete="username" spellcheck="false" />
      <div class="users-editor-pass-wrap">
        <input class="users-editor-input input users-editor-pass" data-field="pass" type="${passVisible ? 'text' : 'password'}" value="${escapeAttr(r.pass)}" placeholder="password" autocomplete="new-password" spellcheck="false" />
        <button type="button" class="users-editor-eye" data-action="toggle-pass" aria-pressed="${passVisible}" title="${passVisible ? 'Hide password' : 'Show password'}" aria-label="Toggle password visibility">
          <span class="icon icon-sm" aria-hidden="true">${passVisible ? eyeOffSVG() : eyeOnSVG()}</span>
        </button>
      </div>
      <div class="users-editor-patterns">
        ${r.patterns.map((p, idx) => `
          <span class="pattern-chip">
            <span class="pattern-chip-text mono">${escapeHTML(p)}</span>
            <button type="button" class="pattern-chip-remove" data-action="remove-pattern" data-pattern-idx="${idx}" aria-label="Remove pattern ${escapeAttr(p)}" title="Remove">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M6 6l12 12"/><path d="M18 6L6 18"/></svg>
            </button>
          </span>
        `).join('')}
        <input class="users-editor-input pattern-input" data-field="pattern-input" type="text" placeholder="${r.patterns.length === 0 ? '*' : '+ pattern'}" spellcheck="false" />
      </div>
      <button type="button" class="users-editor-remove" data-action="remove-row" aria-label="Remove user">
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
function escapeHTML(s) { return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c])); }
