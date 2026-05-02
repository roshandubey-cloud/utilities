// guidance.js — gentle nudges instead of silent ignores.
//
// When a primary action button (Test connection, Start run, Probe…)
// fires with prerequisite fields empty or invalid, we used to either
// silently return or POST to the server and surface a backend error
// message far from the field that needs attention. Operators
// learned the hard way: "click did nothing → maybe try again".
//
// guideRequiredFields() unifies the response: focus the first empty
// field, briefly pulse a soft accent ring around it, and toast a
// short message naming the missing labels. No CTA is ever truly a
// no-op for the operator — they always know why a click didn't go
// through and what to fill in next.
//
// Usage:
//   const ok = guideRequiredFields([
//     { el: $('host'), label: 'Host' },
//     { el: $('port'), label: 'Port' },
//   ], {
//     action: 'test the connection',     // appears in the toast
//   });
//   if (!ok) return;
//
// emptyOf() — true when the field has no usable value. Numbers count
// as "empty" when 0 (Port=0 is no port) UNLESS allowZero is set.

import { pushToast } from './toast.js';

const PULSE_CLASS = 'field-pulse';
const PULSE_MS = 1500;

export function emptyOf(el, { allowZero = false } = {}) {
  if (!el) return true;
  const v = (el.value ?? '').toString().trim();
  if (v === '') return true;
  if (!allowZero && el.type === 'number' && Number(v) === 0) return true;
  return false;
}

// Pulses a soft accent ring around `el` for ~1.5s. The class is on
// every <input> / <textarea> with a value-bearing role; pure CSS so
// the animation doesn't fight other listeners.
export function pulseField(el) {
  if (!el) return;
  el.classList.remove(PULSE_CLASS);
  // Force reflow so the animation restarts cleanly even if pulseField
  // is called twice in quick succession.
  void el.offsetWidth;
  el.classList.add(PULSE_CLASS);
  setTimeout(() => el.classList.remove(PULSE_CLASS), PULSE_MS);
}

// guideRequiredFields validates that every {el, label} entry is
// non-empty. On a hit it returns true; otherwise focuses the first
// empty input, pulses every empty one, and toasts a friendly
// message like "Fill in Host and Port to test the connection."
//
// opts:
//   action       — verb phrase that completes "to <action>". Default
//                  "continue".
//   type         — toast type. Default "warn".
//   timeout      — toast timeout ms. Default 5000.
//   allowZero    — pass-through to emptyOf for fields where 0 is OK.
export function guideRequiredFields(fields, opts = {}) {
  const missing = fields.filter((f) => emptyOf(f.el, { allowZero: opts.allowZero }));
  if (missing.length === 0) return true;

  // Focus the first empty field — operators expect cursor-on-target.
  const first = missing[0];
  if (first.el && typeof first.el.focus === 'function') {
    try { first.el.focus({ preventScroll: false }); } catch { first.el.focus(); }
  }
  // Pulse all of them.
  missing.forEach((f) => pulseField(f.el));

  const labels = missing.map((f) => f.label).filter(Boolean);
  const noun = labels.length === 0 ? 'a required field'
              : labels.length === 1 ? labels[0]
              : labels.length === 2 ? labels.join(' and ')
              : labels.slice(0, -1).join(', ') + ', and ' + labels.slice(-1)[0];
  const action = opts.action || 'continue';
  const message = `Fill in ${noun} to ${action}.`;
  pushToast(message, opts.type || 'warn', { timeout: opts.timeout || 5000 });
  return false;
}

// guideCondition is the predicate variant: when `ok` is false, focus
// `focusEl` (if provided), pulse it, and toast `message`. Use for
// rules that aren't "field empty" — e.g. "no workload enabled" or
// "users CSV has no rows".
export function guideCondition(ok, message, { focusEl, type = 'warn', timeout = 5000 } = {}) {
  if (ok) return true;
  if (focusEl) {
    try { focusEl.focus({ preventScroll: false }); } catch {}
    pulseField(focusEl);
  }
  pushToast(message, type, { timeout });
  return false;
}

// Bridge to legacy.js (non-module script tag at the bottom of
// index.html). Can't `import` from there, so the module-side
// consumers wire this up on the window object once and the legacy
// script reaches for it through window.__guide.
if (typeof window !== 'undefined') {
  window.__guide = { guideRequiredFields, guideCondition, pulseField, emptyOf };
}
