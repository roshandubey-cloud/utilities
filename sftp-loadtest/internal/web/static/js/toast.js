// toast.js — global notification layer.
// Single host element auto-mounted on import. Use pushToast() from anywhere.

const HOST_ID = 'toast-host';
const DEFAULT_TIMEOUT_MS = 4000;

function host() {
  let el = document.getElementById(HOST_ID);
  if (el) return el;
  el = document.createElement('div');
  el.id = HOST_ID;
  el.className = 'toast-host';
  el.setAttribute('role', 'region');
  el.setAttribute('aria-label', 'Notifications');
  el.setAttribute('aria-live', 'polite');
  document.body.appendChild(el);
  return el;
}

const TYPE_ICONS = {
  success: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M5 12.5l5 5 9-11"/></svg>',
  error:   '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><path d="M12 8v5"/><path d="M12 16.5h.01"/></svg>',
  warn:    '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M10.6 3.5l-8 14a1.6 1.6 0 0 0 1.4 2.5h16a1.6 1.6 0 0 0 1.4-2.5l-8-14a1.6 1.6 0 0 0-2.8 0z"/><path d="M12 9v4"/><path d="M12 16.5h.01"/></svg>',
  info:    '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><path d="M12 11v5"/><path d="M12 7.5h.01"/></svg>',
};

export function pushToast(message, type = 'info', { timeout = DEFAULT_TIMEOUT_MS, action } = {}) {
  if (!message) return null;
  const t = TYPE_ICONS[type] ? type : 'info';
  const el = document.createElement('div');
  el.className = `toast toast-${t}`;
  el.setAttribute('role', t === 'error' ? 'alert' : 'status');
  el.innerHTML = `
    <span class="toast-icon" aria-hidden="true">${TYPE_ICONS[t]}</span>
    <span class="toast-message"></span>
    ${action ? `<button class="toast-action" type="button">${escapeHTML(action.label || 'OK')}</button>` : ''}
    <button class="toast-close" type="button" aria-label="Dismiss">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round"><path d="M6 6l12 12"/><path d="M18 6L6 18"/></svg>
    </button>`;
  el.querySelector('.toast-message').textContent = message;

  const dismiss = () => {
    if (!el.parentNode) return;
    el.dataset.leaving = '1';
    setTimeout(() => el.remove(), 200);
  };
  el.querySelector('.toast-close').addEventListener('click', dismiss);
  if (action) {
    el.querySelector('.toast-action').addEventListener('click', () => {
      try { action.onClick && action.onClick(); } finally { dismiss(); }
    });
  }
  if (timeout > 0) setTimeout(dismiss, timeout);

  host().appendChild(el);
  // Trigger CSS enter animation on next frame
  requestAnimationFrame(() => { el.dataset.entered = '1'; });
  return dismiss;
}

function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}
