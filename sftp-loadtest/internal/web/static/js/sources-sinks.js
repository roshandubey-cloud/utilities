// sources-sinks.js — v0.14 Phase 2 wiring for the source/sink form
// disclosures inside Normal load / Large load / Download cards.
//
// Each disclosure carries:
//   - a [data-role="src-kind"]    or [data-role="sink-kind"]    segmented picker
//   - a [data-role="src-kind-value"] or [data-role="sink-kind-value"] hidden input
//   - kind-specific field groups gated by data-role="src-files-fields" /
//     "src-dir-fields" / "src-mode-fields" / "sink-fields"
//
// This module:
//   1. Wires the segmented buttons so clicking flips aria-pressed +
//      reveals the right field group.
//   2. Exports buildNormalSource / buildLargeSource / buildDownloadSink
//      that legacy.js calls when assembling the /api/start payload.
//      Returns null when the operator left the picker on the default
//      (synthetic / discard) so the JSON sent matches what an
//      unmodified v0.13 client would have produced.
//   3. Exports applyNormalSource / applyLargeSource / applyDownloadSink
//      that legacy.js's import-config path uses to populate the form
//      from a saved JSON.

export function mountSourcesAndSinks() {
  document.querySelectorAll('[data-role="source-disclosure"]').forEach((root) => {
    wireSourceDisclosure(root);
  });
  const sinkRoot = document.querySelector('[data-role="sink-disclosure"]');
  if (sinkRoot) wireSinkDisclosure(sinkRoot);

  // Bridge to legacy.js (non-module): legacy form-serializer needs to
  // read the picker state when assembling /api/start payload, and the
  // import-config path needs to populate fields from JSON. Expose the
  // public functions on window so the regular-script consumer can
  // call them without an import statement.
  if (typeof window !== 'undefined') {
    window.__srcSink = { readSource, readSink, applySource, applySink };
  }
}

function wireSourceDisclosure(root) {
  const kindPicker = root.querySelector('[data-role="src-kind"]');
  const kindValue  = root.querySelector('[data-role="src-kind-value"]');
  const filesEl    = root.querySelector('[data-role="src-files-fields"]');
  const dirEl      = root.querySelector('[data-role="src-dir-fields"]');
  const modeEl     = root.querySelector('[data-role="src-mode-fields"]');
  const modePicker = root.querySelector('[data-role="src-mode"]');
  const modeValue  = root.querySelector('[data-role="src-mode-value"]');
  if (!kindPicker || !kindValue) return;

  function applyKind(kind) {
    kindValue.value = kind;
    kindPicker.querySelectorAll('button').forEach((b) => {
      b.setAttribute('aria-pressed', b.dataset.value === kind ? 'true' : 'false');
    });
    if (filesEl) filesEl.hidden = kind !== 'local-files';
    if (dirEl)   dirEl.hidden   = kind !== 'local-dir';
    // Pick mode only matters for the file-backed sources.
    if (modeEl)  modeEl.hidden  = kind === 'synthetic';
  }
  kindPicker.querySelectorAll('button').forEach((btn) => {
    btn.addEventListener('click', (ev) => {
      ev.preventDefault();
      applyKind(btn.dataset.value);
    });
  });
  applyKind(kindValue.value || 'synthetic');

  if (modePicker && modeValue) {
    modePicker.querySelectorAll('button').forEach((btn) => {
      btn.addEventListener('click', (ev) => {
        ev.preventDefault();
        modeValue.value = btn.dataset.value;
        modePicker.querySelectorAll('button').forEach((b) => {
          b.setAttribute('aria-pressed', b === btn ? 'true' : 'false');
        });
      });
    });
  }
}

function wireSinkDisclosure(root) {
  const kindPicker = root.querySelector('[data-role="sink-kind"]');
  const kindValue  = root.querySelector('[data-role="sink-kind-value"]');
  const fieldsEl   = root.querySelector('[data-role="sink-fields"]');
  if (!kindPicker || !kindValue) return;

  function applyKind(kind) {
    kindValue.value = kind;
    kindPicker.querySelectorAll('button').forEach((b) => {
      b.setAttribute('aria-pressed', b.dataset.value === kind ? 'true' : 'false');
    });
    if (fieldsEl) fieldsEl.hidden = kind !== 'local-disk';
  }
  kindPicker.querySelectorAll('button').forEach((btn) => {
    btn.addEventListener('click', (ev) => {
      ev.preventDefault();
      applyKind(btn.dataset.value);
    });
  });
  applyKind(kindValue.value || 'discard');
}

// ---------- read field state into the JSON payload ----------

// readSource returns the SourceConfig for the disclosure with the
// given data-kind, or null when the picker is on "synthetic" with no
// overrides — keeping the wire-format parity with v0.13 clients.
export function readSource(kind /* "normal" | "large" */) {
  const root = document.querySelector(`[data-role="source-disclosure"][data-kind="${kind}"]`);
  if (!root) return null;
  const k = root.querySelector('[data-role="src-kind-value"]')?.value || 'synthetic';
  const advanced = parseAdvanced(root.querySelector('[data-role="src-advanced"]')?.value);
  const out = { kind: k };
  if (k === 'local-files') {
    const lines = (root.querySelector('[data-role="src-files"]')?.value || '')
      .split('\n').map((s) => s.trim()).filter(Boolean);
    if (lines.length === 0) return null; // misconfigured — drop the picker silently; backend would error
    out.files = lines;
    out.mode = root.querySelector('[data-role="src-mode-value"]')?.value || 'round-robin';
  } else if (k === 'local-dir') {
    const dir = (root.querySelector('[data-role="src-dir"]')?.value || '').trim();
    if (!dir) return null;
    out.dir = dir;
    out.mode = root.querySelector('[data-role="src-mode-value"]')?.value || 'round-robin';
  } else {
    // synthetic — only emit the config when there are advanced overrides
    // OR a non-default mode. Otherwise return null so the backend uses
    // the synthetic default verbatim.
    if (!advanced) return null;
  }
  if (advanced) {
    if (advanced.per_user)    out.per_user = advanced.per_user;
    if (advanced.per_pattern) out.per_pattern = advanced.per_pattern;
  }
  return out;
}

// readSink returns the SinkConfig for the download card, or null when
// kind=discard — wire-format parity with v0.13.
export function readSink() {
  const root = document.querySelector('[data-role="sink-disclosure"]');
  if (!root) return null;
  const k = root.querySelector('[data-role="sink-kind-value"]')?.value || 'discard';
  if (k === 'discard') return null;
  const root_ = (root.querySelector('[data-role="sink-root"]')?.value || '').trim();
  if (!root_) return null;
  const tpl = (root.querySelector('[data-role="sink-template"]')?.value || '').trim() || '{user}/{filename}';
  const overwrite = !!root.querySelector('[data-role="sink-overwrite"]')?.checked;
  return { kind: 'local-disk', root: root_, template: tpl, overwrite };
}

// ---------- populate field state from an imported JSON ----------

export function applySource(kind, src) {
  const root = document.querySelector(`[data-role="source-disclosure"][data-kind="${kind}"]`);
  if (!root) return;
  const kindValue = root.querySelector('[data-role="src-kind-value"]');
  const k = (src && src.kind) || 'synthetic';
  if (kindValue) {
    kindValue.value = k;
    root.querySelectorAll('[data-role="src-kind"] button').forEach((b) => {
      b.setAttribute('aria-pressed', b.dataset.value === k ? 'true' : 'false');
    });
  }
  // Toggle the kind-specific blocks.
  const filesEl = root.querySelector('[data-role="src-files-fields"]');
  const dirEl   = root.querySelector('[data-role="src-dir-fields"]');
  const modeEl  = root.querySelector('[data-role="src-mode-fields"]');
  if (filesEl) filesEl.hidden = k !== 'local-files';
  if (dirEl)   dirEl.hidden   = k !== 'local-dir';
  if (modeEl)  modeEl.hidden  = k === 'synthetic';

  // Populate kind-specific fields.
  if (src && k === 'local-files') {
    const filesEl = root.querySelector('[data-role="src-files"]');
    if (filesEl) filesEl.value = (src.files || []).join('\n');
  }
  if (src && k === 'local-dir') {
    const dirEl = root.querySelector('[data-role="src-dir"]');
    if (dirEl) dirEl.value = src.dir || '';
  }
  if (src && src.mode) {
    const mv = root.querySelector('[data-role="src-mode-value"]');
    const mp = root.querySelector('[data-role="src-mode"]');
    if (mv) mv.value = src.mode;
    if (mp) mp.querySelectorAll('button').forEach((b) => {
      b.setAttribute('aria-pressed', b.dataset.value === src.mode ? 'true' : 'false');
    });
  }
  // Advanced overrides round-trip through the JSON textarea.
  const adv = root.querySelector('[data-role="src-advanced"]');
  if (adv) {
    const merged = {};
    if (src && src.per_user)    merged.per_user = src.per_user;
    if (src && src.per_pattern) merged.per_pattern = src.per_pattern;
    adv.value = Object.keys(merged).length ? JSON.stringify(merged, null, 2) : '';
  }
  // Open the disclosure so the operator sees the populated state instead
  // of an unchanged "defaults to synthetic" closed summary.
  if (src && k !== 'synthetic') root.open = true;
}

export function applySink(sink) {
  const root = document.querySelector('[data-role="sink-disclosure"]');
  if (!root) return;
  const kindValue = root.querySelector('[data-role="sink-kind-value"]');
  const k = (sink && sink.kind) || 'discard';
  if (kindValue) {
    kindValue.value = k;
    root.querySelectorAll('[data-role="sink-kind"] button').forEach((b) => {
      b.setAttribute('aria-pressed', b.dataset.value === k ? 'true' : 'false');
    });
  }
  const fieldsEl = root.querySelector('[data-role="sink-fields"]');
  if (fieldsEl) fieldsEl.hidden = k !== 'local-disk';
  if (sink && k === 'local-disk') {
    root.querySelector('[data-role="sink-root"]').value = sink.root || '';
    root.querySelector('[data-role="sink-template"]').value = sink.template || '{user}/{filename}';
    root.querySelector('[data-role="sink-overwrite"]').checked = !!sink.overwrite;
    root.open = true;
  }
}

// parseAdvanced safely consumes the JSON textarea — empty / whitespace
// returns null; malformed JSON returns null + console.warn so a typo
// doesn't silently brick the run.
function parseAdvanced(raw) {
  if (!raw || !raw.trim()) return null;
  try {
    const parsed = JSON.parse(raw);
    if (parsed && typeof parsed === 'object') return parsed;
  } catch (e) {
    // eslint-disable-next-line no-console
    console.warn('source-advanced JSON parse failed; ignoring:', e);
  }
  return null;
}
