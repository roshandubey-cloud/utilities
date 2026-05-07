// draggable.js — v0.19.26 shared draggable helper for floating pills.
// v0.19.29 adds collision avoidance: the operator can drag freely
// around the workspace but the bar refuses to enter screen real-
// estate occupied by any "content" element (cards, buttons, inputs,
// the other pill). Pre-fix, the host pill could be parked over the
// Save preset button or the form fields; operators flagged any
// overlap as a layout bug. Now the proposed move is rejected
// (cursor flips to not-allowed for visual feedback) when it would
// occlude another tracked element.
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

// Selectors a floating pill must NOT overlap. Each is a "content"
// element the operator interacts with (or another pill). Picked to
// be specific enough that empty layout containers are excluded but
// generous enough that any actionable surface is covered. Tested on
// the Configure view today; extend as new view-specific affordances
// land.
const AVOID_SELECTORS = [
  '.shell-topbar',
  '.shell-sidebar',
  '.cfg-prelude',
  '.cfg-section',
  '.cfg-actionzone',
  '.cfg-summary-bar',
  '.shell-statusbar',
  '[data-component="connection"]',
  '[data-role="save-preset"]',
  '#importBtn',
  '#importRunBtn',
  '#startBtn',
  '#stopBtn',
];

// gatherAvoidRects returns the bounding boxes of every visible
// "content" element on the page, excluding the bar that's being
// dragged itself (so a pill never collides with its own rect).
// Filters out hidden / zero-area elements.
function gatherAvoidRects(self) {
  const out = [];
  for (const sel of AVOID_SELECTORS) {
    document.querySelectorAll(sel).forEach((el) => {
      if (el === self || el.contains(self) || self.contains(el)) return;
      const r = el.getBoundingClientRect();
      if (r.width === 0 || r.height === 0) return;
      // Skip elements that are display:none or off-screen (e.g.,
      // hidden views' contents in a SPA).
      const style = window.getComputedStyle(el);
      if (style.visibility === 'hidden' || style.display === 'none') return;
      out.push({ top: r.top, right: r.right, bottom: r.bottom, left: r.left });
    });
  }
  return out;
}

// rectsOverlap is the standard AABB intersection with a small margin
// so the pill doesn't end up flush-against an avoid target — the
// margin gives the operator a visible gutter around every other
// element.
function rectsOverlap(a, b, margin = 6) {
  return !(
    a.right + margin <= b.left ||
    a.left >= b.right + margin ||
    a.bottom + margin <= b.top ||
    a.top >= b.bottom + margin
  );
}

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

    // Collision avoidance — gather every content rect once at the
    // start of the drag (cheap; ~20 elements) so move events are
    // O(N) instead of O(DOM) per tick.
    const avoid = gatherAvoidRects(el);
    let lastValidTop = startTop;
    let lastValidRight = startRight;

    const onMove = (mv) => {
      const dx = mv.clientX - startX;
      const dy = mv.clientY - startY;
      const proposedTop = clamp(
        startTop + dy,
        DEFAULT_PADDING,
        Math.max(DEFAULT_PADDING, window.innerHeight - startRect.height - DEFAULT_PADDING),
      );
      const proposedRight = clamp(
        startRight - dx,
        DEFAULT_PADDING,
        Math.max(DEFAULT_PADDING, window.innerWidth - startRect.width - DEFAULT_PADDING),
      );
      // Compute the rect the pill WOULD occupy at the proposed
      // position, then test against every avoid rect.
      const proposedLeft = window.innerWidth - proposedRight - startRect.width;
      const proposedRect = {
        top: proposedTop,
        bottom: proposedTop + startRect.height,
        left: proposedLeft,
        right: proposedLeft + startRect.width,
      };
      const collision = avoid.some((r) => rectsOverlap(proposedRect, r));
      if (collision) {
        // Visual feedback — flag the dragging state so CSS can swap
        // the cursor and dim the pill while the operator is being
        // "guided" away. Don't update top/right; the pill stays at
        // the last valid position until the operator moves toward a
        // free zone.
        el.dataset.dragBlocked = 'true';
        return;
      }
      el.dataset.dragBlocked = 'false';
      lastValidTop = proposedTop;
      lastValidRight = proposedRight;
      el.style.top = proposedTop + 'px';
      el.style.right = proposedRight + 'px';
      el.style.left = '';
      el.style.bottom = '';
      el.style.margin = '';
    };
    const onUp = () => {
      el.dataset.dragging = 'false';
      el.dataset.dragBlocked = 'false';
      el.style.cursor = 'grab';
      el.releasePointerCapture(ev.pointerId);
      el.removeEventListener('pointermove', onMove);
      el.removeEventListener('pointerup', onUp);
      // Persist the LAST VALID (collision-free) position so a reload
      // never lands the pill on top of content.
      writePos({ topPx: lastValidTop, rightPx: lastValidRight });
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
    // 24 px gutter (was 8) — operators flagged 8 px as feeling
    // "overlapping" because anti-aliased pill borders blended into
    // the adjacent button's edge. 24 reads as a deliberate gap.
    const right = Math.round(window.innerWidth - rect.left + 24);
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
