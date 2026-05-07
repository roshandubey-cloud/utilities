// draggable.js — v0.19.26 shared draggable helper for floating pills.
//
// Two consumers today: the host-info pill (`.shell-statusbar`, mounted
// in shell.js) and the Configure summary pill (`.cfg-summary-bar`,
// mounted in configure-redesign.js). Each gets its own storage key,
// default origin, and (optionally) a DOM anchor whose left edge the
// pill snaps 8 px to the left of on first paint when no saved offset
// exists.
//
// Why a shared module: pre-fix each pill grew its own copy of the
// pointer-event drag + localStorage persistence + viewport clamping
// + dock-snap behaviour. Bug fixes (like the v0.19.25 dynamic-anchor
// measurement) then had to be re-applied to every pill, which is how
// the summary pill stayed sticky-bottom while the host pill became
// floating. Single helper, single bug-fix surface.
//
// Usage:
//   import { makeDraggable } from './draggable.js';
//   makeDraggable(barElement, {
//     storageKey: 'sftp-loadtest-foo-pos-v1',
//     defaultTop: 60,            // null => fall back to CSS top
//     defaultRight: 24,
//     anchorSelector: '[data-role="save-preset"]',  // optional
//     dockButton: bar.querySelector('[data-role="bar-dock"]'),  // optional
//   });
//
// The element must be (or be promoted to) `position: fixed` via CSS —
// this helper sets inline `top` / `right` styles. It does NOT change
// `position`.

const DEFAULT_PADDING = 8;

export function makeDraggable(el, opts) {
  if (!el || el.dataset.draggableMounted === '1') return;
  el.dataset.draggableMounted = '1';

  const storageKey = opts.storageKey;
  // null on either default means "leave the CSS default in place on
  // first paint" — used by .cfg-summary-bar which is bottom-centered
  // via CSS and shouldn't snap to a top-right corner just because a
  // generic helper got mounted.
  const defaultTop = typeof opts.defaultTop === 'number' ? opts.defaultTop : null;
  const defaultRight = typeof opts.defaultRight === 'number' ? opts.defaultRight : null;
  const anchorSelector = opts.anchorSelector || null;
  const dockButton = opts.dockButton || null;

  el.style.cursor = 'grab';
  el.style.userSelect = 'none';

  // First paint: prefer saved offset; else dynamic anchor; else
  // hard-coded default.
  applySavedOrDefault();

  el.addEventListener('pointerdown', (ev) => {
    if (ev.target.closest('button, input, a, [role="button"]')) return;
    ev.preventDefault();
    el.setPointerCapture(ev.pointerId);
    el.dataset.dragging = 'true';
    el.style.cursor = 'grabbing';
    const startX = ev.clientX;
    const startY = ev.clientY;
    const startRect = el.getBoundingClientRect();
    const startTop = startRect.top;
    const startRight = window.innerWidth - startRect.right;

    const onMove = (mv) => {
      const dx = mv.clientX - startX;
      const dy = mv.clientY - startY;
      const newTop = clamp(
        startTop + dy,
        DEFAULT_PADDING,
        Math.max(DEFAULT_PADDING, window.innerHeight - startRect.height - DEFAULT_PADDING),
      );
      const newRight = clamp(
        startRight - dx,
        DEFAULT_PADDING,
        Math.max(DEFAULT_PADDING, window.innerWidth - startRect.width - DEFAULT_PADDING),
      );
      el.style.top = newTop + 'px';
      el.style.right = newRight + 'px';
      el.style.left = ''; // Clear any centering left value when dragging.
      el.style.bottom = '';
      el.style.margin = '';
    };
    const onUp = () => {
      el.dataset.dragging = 'false';
      el.style.cursor = 'grab';
      el.releasePointerCapture(ev.pointerId);
      el.removeEventListener('pointermove', onMove);
      el.removeEventListener('pointerup', onUp);
      const r = el.getBoundingClientRect();
      writePos({ topPx: r.top, rightPx: window.innerWidth - r.right });
    };
    el.addEventListener('pointermove', onMove);
    el.addEventListener('pointerup', onUp);
  });

  if (dockButton) {
    dockButton.addEventListener('click', (ev) => {
      ev.preventDefault();
      ev.stopPropagation();
      try { localStorage.removeItem(storageKey); } catch {}
      el.style.top = '';
      el.style.right = '';
      el.style.left = '';
      el.style.bottom = '';
      el.style.margin = '';
      requestAnimationFrame(() => applySavedOrDefault());
    });
  }

  window.addEventListener('resize', () => {
    const pos = readPos();
    if (pos === null) return;
    const rect = el.getBoundingClientRect();
    const clamped = {
      topPx: pos.topPx === null ? null : clamp(pos.topPx, DEFAULT_PADDING, Math.max(DEFAULT_PADDING, window.innerHeight - rect.height - DEFAULT_PADDING)),
      rightPx: clamp(pos.rightPx, DEFAULT_PADDING, Math.max(DEFAULT_PADDING, window.innerWidth - rect.width - DEFAULT_PADDING)),
    };
    apply(clamped);
  });

  // ---------------- helpers ----------------

  function applySavedOrDefault() {
    const saved = readPos();
    if (saved !== null) {
      apply(saved);
      return;
    }
    // No saved offset → try anchor measurement; if no anchor and no
    // hard-coded default, leave the CSS layout alone (but keep drag
    // wired so the operator can move the pill at will).
    requestAnimationFrame(() => {
      const measuredRight = computeAnchorRight();
      const rightPx = measuredRight !== null ? measuredRight : defaultRight;
      if (defaultTop === null && rightPx === null) return; // CSS default wins
      apply({ topPx: defaultTop, rightPx });
    });
  }

  function computeAnchorRight() {
    if (!anchorSelector) return null;
    const anchor = document.querySelector(anchorSelector);
    if (!anchor) return null;
    const rect = anchor.getBoundingClientRect();
    if (!rect.width) return null;
    const right = Math.round(window.innerWidth - rect.left + DEFAULT_PADDING);
    const w = el.getBoundingClientRect().width;
    if (right + w > window.innerWidth - DEFAULT_PADDING) return null;
    return right;
  }

  function apply(pos) {
    if (pos.topPx === null) {
      el.style.top = '';
    } else {
      el.style.top = pos.topPx + 'px';
    }
    if (pos.rightPx === null) {
      el.style.right = '';
    } else {
      el.style.right = pos.rightPx + 'px';
    }
    // Clear any pre-existing layout so apply() always wins.
    el.style.left = '';
    el.style.bottom = '';
    el.style.margin = '';
  }

  function readPos() {
    try {
      const raw = localStorage.getItem(storageKey);
      if (!raw) return null;
      const parsed = JSON.parse(raw);
      return {
        topPx: typeof parsed.topPx === 'number' ? parsed.topPx : null,
        rightPx: typeof parsed.rightPx === 'number' ? parsed.rightPx : defaultRight,
      };
    } catch { return null; }
  }

  function writePos(pos) {
    try { localStorage.setItem(storageKey, JSON.stringify(pos)); } catch {}
  }
}

function clamp(v, lo, hi) { return v < lo ? lo : v > hi ? hi : v; }
