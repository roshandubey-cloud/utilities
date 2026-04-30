// modal.js — proper modal dialogs.
//
// window.prompt / window.confirm are unreliable in Wails desktop builds
// (the dialogs are blocked or replaced with a no-op). This module provides
// real DOM-rendered replacements that work identically in browser and Wails:
//
//   await prompt({ title, label, placeholder, value })   → string | null
//   await confirm({ title, message, danger })           → boolean
//   await form({ title, fields })                       → object | null
//
// All helpers return Promises that resolve when the user clicks the
// primary button and reject (via null/false) on Cancel/Esc.

const ESC = 'Escape';

function makeBackdrop() {
  const bd = document.createElement('div');
  bd.className = 'modal-backdrop';
  bd.dataset.component = 'modal';
  return bd;
}

function trap(panel, onClose) {
  // Focus first input or button.
  setTimeout(() => {
    const first = panel.querySelector('input, textarea, button[data-role="primary"]');
    if (first) first.focus();
  }, 0);
  // Esc closes.
  const onKey = (ev) => {
    if (ev.key === ESC) {
      ev.preventDefault();
      onClose();
    }
  };
  document.addEventListener('keydown', onKey, true);
  return () => document.removeEventListener('keydown', onKey, true);
}

export function prompt({ title = 'Input', label = '', placeholder = '', value = '' } = {}) {
  return new Promise((resolve) => {
    const bd = makeBackdrop();
    bd.innerHTML = `
      <div class="modal-panel" role="dialog" aria-modal="true" aria-label="${escapeAttr(title)}">
        <div class="modal-head">${escapeHTML(title)}</div>
        <div class="modal-body">
          <label class="modal-field-label">${escapeHTML(label)}</label>
          <input type="text" class="modal-field-input" data-role="value"
                 placeholder="${escapeAttr(placeholder)}" value="${escapeAttr(value)}" />
        </div>
        <div class="modal-foot">
          <button type="button" class="btn btn-ghost" data-role="cancel">Cancel</button>
          <button type="button" class="btn btn-primary" data-role="primary">OK</button>
        </div>
      </div>`;
    document.body.appendChild(bd);
    const input = bd.querySelector('[data-role="value"]');
    const close = (val) => {
      detach();
      bd.remove();
      resolve(val);
    };
    const detach = trap(bd, () => close(null));
    bd.querySelector('[data-role="cancel"]').addEventListener('click', () => close(null));
    bd.querySelector('[data-role="primary"]').addEventListener('click', () => close(input.value));
    bd.addEventListener('click', (ev) => { if (ev.target === bd) close(null); });
    input.addEventListener('keydown', (ev) => {
      if (ev.key === 'Enter') {
        ev.preventDefault();
        close(input.value);
      }
    });
  });
}

export function confirm({ title = 'Confirm', message = '', danger = false, okLabel = 'OK', cancelLabel = 'Cancel' } = {}) {
  return new Promise((resolve) => {
    const bd = makeBackdrop();
    bd.innerHTML = `
      <div class="modal-panel" role="dialog" aria-modal="true" aria-label="${escapeAttr(title)}">
        <div class="modal-head">${escapeHTML(title)}</div>
        <div class="modal-body"><p class="modal-message">${escapeHTML(message)}</p></div>
        <div class="modal-foot">
          <button type="button" class="btn btn-ghost" data-role="cancel">${escapeHTML(cancelLabel)}</button>
          <button type="button" class="btn ${danger ? 'btn-danger' : 'btn-primary'}" data-role="primary">${escapeHTML(okLabel)}</button>
        </div>
      </div>`;
    document.body.appendChild(bd);
    const close = (val) => { detach(); bd.remove(); resolve(val); };
    const detach = trap(bd, () => close(false));
    bd.querySelector('[data-role="cancel"]').addEventListener('click', () => close(false));
    bd.querySelector('[data-role="primary"]').addEventListener('click', () => close(true));
    bd.addEventListener('click', (ev) => { if (ev.target === bd) close(false); });
  });
}

// form() supports a multi-field input dialog. Each field is:
//   { name, label, type, placeholder, required, value, hint }
// Returns null on cancel, otherwise an object keyed by field.name.
export function form({ title = 'Form', fields = [], submitLabel = 'Save' } = {}) {
  return new Promise((resolve) => {
    const bd = makeBackdrop();
    const rows = fields.map((f) => `
      <div class="modal-field">
        <label class="modal-field-label" for="modal-${escapeAttr(f.name)}">${escapeHTML(f.label || f.name)}${f.required ? ' <span class="modal-field-req">*</span>' : ''}</label>
        <input class="modal-field-input"
               id="modal-${escapeAttr(f.name)}"
               name="${escapeAttr(f.name)}"
               type="${escapeAttr(f.type || 'text')}"
               placeholder="${escapeAttr(f.placeholder || '')}"
               value="${escapeAttr(f.value || '')}"
               ${f.required ? 'required' : ''} />
        ${f.hint ? `<div class="modal-field-hint">${escapeHTML(f.hint)}</div>` : ''}
      </div>`).join('');
    bd.innerHTML = `
      <div class="modal-panel" role="dialog" aria-modal="true" aria-label="${escapeAttr(title)}">
        <div class="modal-head">${escapeHTML(title)}</div>
        <form class="modal-body" data-role="form">${rows}</form>
        <div class="modal-foot">
          <button type="button" class="btn btn-ghost" data-role="cancel">Cancel</button>
          <button type="button" class="btn btn-primary" data-role="primary">${escapeHTML(submitLabel)}</button>
        </div>
      </div>`;
    document.body.appendChild(bd);
    const formEl = bd.querySelector('[data-role="form"]');
    const close = (val) => { detach(); bd.remove(); resolve(val); };
    const submit = () => {
      const out = {};
      let ok = true;
      for (const f of fields) {
        const el = formEl.querySelector(`[name="${f.name}"]`);
        const v = el ? el.value : '';
        if (f.required && !v.trim()) {
          ok = false;
          el?.classList.add('modal-field-invalid');
        }
        out[f.name] = v;
      }
      if (!ok) return;
      close(out);
    };
    const detach = trap(bd, () => close(null));
    bd.querySelector('[data-role="cancel"]').addEventListener('click', () => close(null));
    bd.querySelector('[data-role="primary"]').addEventListener('click', submit);
    bd.addEventListener('click', (ev) => { if (ev.target === bd) close(null); });
    formEl.addEventListener('keydown', (ev) => {
      if (ev.key === 'Enter' && ev.target.tagName !== 'TEXTAREA') {
        ev.preventDefault();
        submit();
      }
    });
    formEl.addEventListener('input', (ev) => {
      if (ev.target.classList) ev.target.classList.remove('modal-field-invalid');
    });
  });
}

// hostKeyConsent — purpose-built modal for "trust this server's host key?"
//
// Two modes selected by which fingerprint is supplied:
//   - newFingerprint only          → first-time-seen flow (low friction)
//   - newFingerprint + oldFingerprint → key-CHANGED flow (high friction,
//     red Accept button, MITM warning)
//
// Resolves to true (Accept), false (Cancel) — never throws.
export function hostKeyConsent({ host, port, newFingerprint, oldFingerprint = '' } = {}) {
  return new Promise((resolve) => {
    const changed = !!oldFingerprint;
    const title = changed ? 'Host key has CHANGED' : 'Trust this host key?';
    const okLabel = changed ? 'Accept the new key' : 'Trust and continue';
    const headline = changed
      ? `The SFTP server at <strong class="mono">${escapeHTML(host)}:${escapeHTML(String(port))}</strong> presented a host key
         <strong>different</strong> from the one previously trusted. This can mean a legitimate key
         rotation — or a man-in-the-middle attack. Verify the new fingerprint out-of-band before accepting.`
      : `The SFTP server at <strong class="mono">${escapeHTML(host)}:${escapeHTML(String(port))}</strong> presented a host
         key not yet trusted by this app. Verify the fingerprint matches one given to you out-of-band.`;
    const fpBlock = changed ? `
      <div class="modal-fp-grid">
        <div class="modal-fp-row">
          <div class="modal-fp-label">Previously trusted</div>
          <div class="modal-fp-value mono" data-role="fp-old">${escapeHTML(oldFingerprint)}</div>
        </div>
        <div class="modal-fp-row">
          <div class="modal-fp-label">Newly presented</div>
          <div class="modal-fp-value mono" data-role="fp-new">${escapeHTML(newFingerprint)}</div>
        </div>
      </div>` : `
      <div class="modal-fp-grid">
        <div class="modal-fp-row">
          <div class="modal-fp-label">SHA-256 fingerprint</div>
          <div class="modal-fp-value mono" data-role="fp-new">${escapeHTML(newFingerprint)}</div>
        </div>
      </div>`;

    const bd = makeBackdrop();
    bd.dataset.modal = 'host-key-consent';
    bd.dataset.danger = changed ? '1' : '0';
    bd.innerHTML = `
      <div class="modal-panel modal-panel-wide" role="dialog" aria-modal="true" aria-label="${escapeAttr(title)}">
        <div class="modal-head ${changed ? 'modal-head-danger' : ''}">${escapeHTML(title)}</div>
        <div class="modal-body">
          <p class="modal-message" style="white-space:normal">${headline}</p>
          ${fpBlock}
        </div>
        <div class="modal-foot">
          <button type="button" class="btn btn-ghost" data-role="cancel">Cancel</button>
          <button type="button" class="btn ${changed ? 'btn-danger' : 'btn-primary'}" data-role="primary">${escapeHTML(okLabel)}</button>
        </div>
      </div>`;
    document.body.appendChild(bd);
    const close = (val) => { detach(); bd.remove(); resolve(val); };
    const detach = trap(bd, () => close(false));
    bd.querySelector('[data-role="cancel"]').addEventListener('click', () => close(false));
    bd.querySelector('[data-role="primary"]').addEventListener('click', () => close(true));
    bd.addEventListener('click', (ev) => { if (ev.target === bd) close(false); });
  });
}

function escapeHTML(s) { return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c])); }
function escapeAttr(s) { return String(s).replace(/"/g, '&quot;'); }
