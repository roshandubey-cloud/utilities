// users-editor.js — minimal "smart textarea" augment for SFTP user CSVs.
//
// Earlier versions of this file rendered a chips-and-rows structured editor on
// top of every `textarea.csv-users`. Operator feedback: it was visually noisy
// and made simple paste workflows confusing. This version goes back to a plain
// textarea (which already had focus/blur password masking — see index.html) and
// adds only what the textarea genuinely lacks:
//
//   * A live "N users · M patterns" counter below the field.
//   * Inline validation for lines that look broken (no comma, empty username).
//   * Soft auto-tidy on blur — strips trailing whitespace per line, drops
//     blank lines after the last non-blank one.
//
// dataset.raw stays the source of truth; index.html's mask-on-blur logic and
// getCsvRaw() / setCsvRaw() helpers keep working unchanged.

export function mountUsersEditors() {
  document.querySelectorAll('textarea.csv-users').forEach((textarea) => {
    if (textarea.dataset.smartHintMounted === '1') return;
    textarea.dataset.smartHintMounted = '1';
    initOne(textarea);
  });
}

function initOne(textarea) {
  // Hint element renders just below the textarea, inside the same parent.
  const hint = document.createElement('div');
  hint.className = 'csv-users-hint';
  hint.style.cssText = 'font-size: 11px; color: var(--text-muted, #6b7280); margin-top: 4px; line-height: 1.4;';
  textarea.insertAdjacentElement('afterend', hint);

  const refresh = () => render(hint, textarea);
  refresh();

  // Listen on both `input` (live typing) and `blur` (mask-on-blur fires too,
  // so we want to recompute against the masked-or-raw view consistently).
  textarea.addEventListener('input', refresh);
  textarea.addEventListener('blur', refresh);

  // Auto-tidy on blur: trim each line, drop trailing blank lines. Done before
  // mask-on-blur captures dataset.raw — so the cleaned form is what's stored.
  textarea.addEventListener('blur', () => {
    const raw = textarea.value;
    const cleaned = tidy(raw);
    if (cleaned !== raw) {
      textarea.value = cleaned;
      // Keep dataset.raw in sync; index.html captures it just after this in
      // its own blur handler, but if our reorder ever changes we won't lose
      // the user's edits.
      textarea.dataset.raw = cleaned;
    }
    refresh();
  });

  // When a config import or "Clear stored credentials" populates the textarea
  // programmatically, dataset.raw is set first and then the displayed value;
  // both paths fire neither input nor blur. Watch dataset.raw via a tiny
  // MutationObserver so the hint reflects imported state.
  new MutationObserver(refresh).observe(textarea, { attributes: true, attributeFilter: ['data-raw'] });
}

// render computes the live counts + validation messages from whichever copy of
// the CSV is currently authoritative (raw while editing, dataset.raw after
// blur), and writes them into the hint element.
function render(hint, textarea) {
  const raw = (textarea.dataset.editing === '1') ? textarea.value : (textarea.dataset.raw || textarea.value || '');
  const { users, patterns, errors } = parse(raw);

  const parts = [];
  if (users === 0) {
    parts.push('<span style="color: var(--text-muted, #6b7280)">no users yet — one per line: <code>user,password,pattern1,pattern2*</code></span>');
  } else {
    const u = users === 1 ? '1 user' : `${users} users`;
    const p = patterns === 1 ? '1 pattern' : `${patterns} patterns`;
    parts.push(`${u} · ${p}`);
  }
  if (errors.length > 0) {
    const errSummary = errors.length === 1
      ? errors[0]
      : `${errors.length} issues — line ${errors[0].split(' ')[1]}: ${errors[0].slice(errors[0].indexOf(':') + 2)}`;
    parts.push(`<span style="color: var(--err, #c2410c)">⚠ ${escapeHTML(errSummary)}</span>`);
  }
  hint.innerHTML = parts.join(' · ');
}

// parse counts users + patterns and flags lines that look malformed. Empty
// lines are silently ignored so the operator can space groups out without
// triggering warnings.
function parse(raw) {
  let users = 0;
  let patterns = 0;
  const errors = [];
  raw.split('\n').forEach((line, idx) => {
    const t = line.trim();
    if (!t) return;
    const cols = t.split(',').map((c) => c.trim());
    const user = cols[0];
    const pass = cols[1];
    const pats = cols.slice(2).filter((p) => p.length > 0);
    if (cols.length < 2) {
      errors.push(`line ${idx + 1}: missing comma — needs at least user,password`);
      return;
    }
    if (!user) {
      errors.push(`line ${idx + 1}: empty username`);
      return;
    }
    if (pats.length === 0) {
      errors.push(`line ${idx + 1}: no file pattern (add one like *)`);
    }
    users++;
    patterns += pats.length;
    void pass;
  });
  return { users, patterns, errors };
}

// tidy normalises whitespace so re-parsing on the server matches the visible
// state: strip trailing whitespace on each line, drop trailing blank lines.
function tidy(raw) {
  const lines = raw.split('\n').map((l) => l.replace(/[ \t]+$/g, ''));
  while (lines.length > 0 && lines[lines.length - 1] === '') lines.pop();
  return lines.join('\n');
}

function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  })[c]);
}
