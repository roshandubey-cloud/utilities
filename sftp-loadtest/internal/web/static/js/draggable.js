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
// element. v0.19.35 bumped 8 → 24 px after operator screenshots
// kept showing the pill's rounded edge "kissing" Save preset's
// rounded edge — anti-aliased at 8 px reads as overlap.
function rectsOverlap(a, b, margin = 24) {
  return !(
    a.right + margin <= b.left ||
    a.left >= b.right + margin ||
    a.bottom + margin <= b.top ||
    a.top >= b.bottom + margin
  );
}

// resolveCollision (v0.19.30) takes a proposed (top, right) position
// and pushes the pill OUT of any colliding avoid rect along the axis
// of minimum penetration. Iterates up to 8 times so a cascading
// resolution (push out of A, now collides with B) converges. Returns
// a position that's collision-free OR (if the workspace is so dense
// no free spot is reachable from the proposed point) the closest
// approximation we can find.
//
// This replaces v0.19.29's "refuse the move" approach — the operator
// asked for guidance, not a hard stop. Now the bar always responds
// to drag input but slides around obstacles, like a smooth puck on
// a magnetic table.
function resolveCollision(proposed, size, avoid, viewport) {
  let top = proposed.top;
  let right = proposed.right;
  const minTop = 8;
  const maxTop = Math.max(minTop, viewport.h - size.height - 8);
  const minRight = 8;
  const maxRight = Math.max(minRight, viewport.w - size.width - 8);

  for (let iter = 0; iter < 8; iter++) {
    const left = viewport.w - right - size.width;
    const pill = {
      top,
      bottom: top + size.height,
      left,
      right: left + size.width,
    };
    let conflict = null;
    for (const a of avoid) {
      if (rectsOverlap(pill, a)) { conflict = a; break; }
    }
    if (!conflict) return { top, right };

    // Compute the four push displacements + a breathing margin
    // matching rectsOverlap's gutter so a single pass clears the
    // collision threshold for the next iteration.
    const margin = 24;
    const pushUp    = pill.bottom - conflict.top + margin;
    const pushDown  = conflict.bottom - pill.top + margin;
    const pushLeft  = pill.right - conflict.left + margin;
    const pushRight = conflict.right - pill.left + margin;
    const min = Math.min(pushUp, pushDown, pushLeft, pushRight);
    if (min === pushUp)        top   = clampN(top - pushUp,   minTop, maxTop);
    else if (min === pushDown) top   = clampN(top + pushDown, minTop, maxTop);
    else if (min === pushLeft) right = clampN(right + pushLeft, minRight, maxRight);
    else                        right = clampN(right - pushRight, minRight, maxRight);
  }
  return { top, right };
}

function clampN(v, lo, hi) { return v < lo ? lo : v > hi ? hi : v; }

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
    let lastTop = startTop;
    let lastRight = startRight;

    const onMove = (mv) => {
      const dx = mv.clientX - startX;
      const dy = mv.clientY - startY;
      const proposed = {
        top: clamp(
          startTop + dy,
          DEFAULT_PADDING,
          Math.max(DEFAULT_PADDING, window.innerHeight - startRect.height - DEFAULT_PADDING),
        ),
        right: clamp(
          startRight - dx,
          DEFAULT_PADDING,
          Math.max(DEFAULT_PADDING, window.innerWidth - startRect.width - DEFAULT_PADDING),
        ),
      };
      // Deflect around any colliding content rect — the pill always
      // moves with the cursor, but slides out of occupied zones along
      // the axis of minimum penetration. Operator gets continuous
      // motion + an automatic "guided" feel.
      const resolved = resolveCollision(
        proposed,
        { width: startRect.width, height: startRect.height },
        avoid,
        { w: window.innerWidth, h: window.innerHeight },
      );
      lastTop = resolved.top;
      lastRight = resolved.right;
      // dragBlocked flag stays false during smooth deflection; only
      // set when the resolver couldn't escape after 8 iterations.
      el.dataset.dragBlocked = 'false';
      el.style.top = resolved.top + 'px';
      el.style.right = resolved.right + 'px';
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
      // Persist the last (already-resolved) position so a reload
      // never lands the pill on top of content.
      writePos({ topPx: lastTop, rightPx: lastRight });
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
    // Run the anchor measurement + collision resolver multiple times:
    //
    //   1. Initial RAF — gives the shell a chance to paint, and the
    //      pill at least picks up its CSS default origin so the user
    //      doesn't see a flash at (0,0) while we wait for mounts.
    //   2. setTimeout(120ms) — by now the chained setTimeout(0) in
    //      app.js has completed mountConfigureRedesign (which inserts
    //      Save preset), mountSidebar, etc. We re-measure now that
    //      the avoid set is fully populated. This is what actually
    //      catches the "Save preset wasn't yet in DOM" race.
    //   3. sftpl:view-changed listener — re-resolves whenever the
    //      operator switches view (different cards, different anchor
    //      positions).
    //
    // Operator pre-fix complaint: pill landed flush against / over
    // Save preset on first paint because the avoid set was empty
    // when the pill was positioned. Multi-tick re-resolve closes
    // that race for good.
    const place = () => {
      const saved = readPos();
      let pos;
      if (saved !== null) {
        pos = { topPx: saved.topPx, rightPx: saved.rightPx };
      } else {
        const measuredRight = computeAnchorRight();
        const rightPx = measuredRight !== null ? measuredRight : defaultRight;
        if (defaultTop === null && rightPx === null) return; // CSS default wins
        pos = { topPx: defaultTop, rightPx };
      }
      apply(pos);
      const rect = el.getBoundingClientRect();
      const avoid = gatherAvoidRects(el);
      const resolved = resolveCollision(
        { top: pos.topPx === null ? rect.top : pos.topPx, right: pos.rightPx === null ? (window.innerWidth - rect.right) : pos.rightPx },
        { width: rect.width, height: rect.height },
        avoid,
        { w: window.innerWidth, h: window.innerHeight },
      );
      apply({ topPx: resolved.top, rightPx: resolved.right });
    };
    requestAnimationFrame(place);
    setTimeout(place, 120);
    document.addEventListener('sftpl:view-changed', place);
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
