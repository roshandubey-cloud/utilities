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
  const probeEl    = root.querySelector('[data-role="src-probe-fields"]');
  const probeBtn   = root.querySelector('[data-role="src-probe"]');
  const probeOut   = root.querySelector('[data-role="src-probe-out"]');
  const warnEl     = root.querySelector('[data-role="src-warn"]');
  const filesText  = root.querySelector('[data-role="src-files"]');
  const dirInput   = root.querySelector('[data-role="src-dir"]');
  const advText    = root.querySelector('[data-role="src-advanced"]');
  const advError   = root.querySelector('[data-role="src-advanced-error"]');
  const filesBrowse= root.querySelector('[data-role="src-files-browse"]');
  const dirBrowse  = root.querySelector('[data-role="src-dir-browse"]');
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
    // Probe is only meaningful when the source actually points at disk.
    if (probeEl) probeEl.hidden = kind === 'synthetic';
    refreshWarning();
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

  // ---- Wails native pickers — only show when desktop bindings exist.
  // Web build leaves the buttons hidden so the operator's hand-typed
  // path workflow stays the only affordance.
  if (filesBrowse && hasWailsPicker('PickFiles')) {
    filesBrowse.hidden = false;
    filesBrowse.addEventListener('click', async (ev) => {
      ev.preventDefault();
      try {
        const picked = await window.go.main.App.PickFiles('Choose upload fixtures');
        if (!picked) return;
        const existing = (filesText.value || '').trim();
        filesText.value = existing ? existing + '\n' + picked : picked;
        filesText.dispatchEvent(new Event('input', { bubbles: true }));
      } catch (e) { console.warn('PickFiles failed:', e); }
    });
  }
  if (dirBrowse && hasWailsPicker('PickDirectory')) {
    dirBrowse.hidden = false;
    dirBrowse.addEventListener('click', async (ev) => {
      ev.preventDefault();
      try {
        const picked = await window.go.main.App.PickDirectory('Choose upload directory');
        if (!picked) return;
        dirInput.value = picked;
        dirInput.dispatchEvent(new Event('input', { bubbles: true }));
      } catch (e) { console.warn('PickDirectory failed:', e); }
    });
  }

  // ---- Live misconfiguration warning.
  function refreshWarning() {
    if (!warnEl) return;
    const k = kindValue.value;
    let msg = '';
    if (k === 'local-files') {
      const lines = (filesText?.value || '').split('\n').map((s) => s.trim()).filter(Boolean);
      if (lines.length === 0) msg = 'Local files selected but no paths entered — the run will silently use synthetic random bytes.';
    } else if (k === 'local-dir') {
      if (!(dirInput?.value || '').trim()) msg = 'Local directory selected but no path entered — the run will silently use synthetic random bytes.';
    }
    warnEl.hidden = !msg;
    warnEl.textContent = msg;
  }
  filesText?.addEventListener('input', refreshWarning);
  dirInput?.addEventListener('input', refreshWarning);
  refreshWarning();

  // ---- Advanced JSON inline parse error so a typo doesn't silently
  // brick the per-user / per-pattern overrides.
  function refreshAdvError() {
    if (!advText || !advError) return;
    const raw = (advText.value || '').trim();
    if (!raw) { advError.hidden = true; advError.textContent = ''; return; }
    try { JSON.parse(raw); advError.hidden = true; advError.textContent = ''; }
    catch (e) {
      advError.hidden = false;
      advError.textContent = 'JSON parse error: ' + e.message;
    }
  }
  advText?.addEventListener('input', refreshAdvError);
  refreshAdvError();

  // ---- Probe button.
  if (probeBtn && probeOut) {
    probeBtn.addEventListener('click', async (ev) => {
      ev.preventDefault();
      probeOut.textContent = 'Probing…';
      const cfg = readSource(root.dataset.kind) || { kind: kindValue.value };
      try {
        const r = await fetch('/api/probe-source', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'sftp-loadtest' },
          body: JSON.stringify(cfg),
        });
        const j = await r.json();
        probeOut.textContent = formatProbeResult(j);
      } catch (e) {
        probeOut.textContent = 'Probe failed: ' + e.message;
      }
    });
  }
}

function wireSinkDisclosure(root) {
  const kindPicker = root.querySelector('[data-role="sink-kind"]');
  const kindValue  = root.querySelector('[data-role="sink-kind-value"]');
  const fieldsEl   = root.querySelector('[data-role="sink-fields"]');
  const rootInput  = root.querySelector('[data-role="sink-root"]');
  const tplInput   = root.querySelector('[data-role="sink-template"]');
  const overwrite  = root.querySelector('[data-role="sink-overwrite"]');
  const previewEl  = root.querySelector('[data-role="sink-preview-path"]');
  const chipBox    = root.querySelector('[data-role="sink-var-chips"]');
  const rootBrowse = root.querySelector('[data-role="sink-root-browse"]');
  const probeBtn   = root.querySelector('[data-role="sink-probe"]');
  const probeOut   = root.querySelector('[data-role="sink-probe-out"]');
  const warnEl     = root.querySelector('[data-role="sink-warn"]');
  if (!kindPicker || !kindValue) return;

  function applyKind(kind) {
    kindValue.value = kind;
    kindPicker.querySelectorAll('button').forEach((b) => {
      b.setAttribute('aria-pressed', b.dataset.value === kind ? 'true' : 'false');
    });
    if (fieldsEl) fieldsEl.hidden = kind !== 'local-disk';
    refreshWarning();
  }
  kindPicker.querySelectorAll('button').forEach((btn) => {
    btn.addEventListener('click', (ev) => {
      ev.preventDefault();
      applyKind(btn.dataset.value);
    });
  });
  applyKind(kindValue.value || 'discard');

  // ---- Native folder picker (Wails desktop only).
  if (rootBrowse && hasWailsPicker('PickDirectory')) {
    rootBrowse.hidden = false;
    rootBrowse.addEventListener('click', async (ev) => {
      ev.preventDefault();
      try {
        const picked = await window.go.main.App.PickDirectory('Choose download root');
        if (!picked) return;
        rootInput.value = picked;
        rootInput.dispatchEvent(new Event('input', { bubbles: true }));
      } catch (e) { console.warn('PickDirectory failed:', e); }
    });
  }

  // ---- Variable chips: clicking inserts the {var} at the caret.
  if (chipBox && tplInput) {
    chipBox.addEventListener('click', (ev) => {
      const btn = ev.target.closest('[data-var]');
      if (!btn) return;
      ev.preventDefault();
      const v = btn.dataset.var;
      const start = tplInput.selectionStart ?? tplInput.value.length;
      const end   = tplInput.selectionEnd   ?? tplInput.value.length;
      const before = tplInput.value.slice(0, start);
      const after  = tplInput.value.slice(end);
      tplInput.value = before + v + after;
      const newPos = start + v.length;
      tplInput.setSelectionRange(newPos, newPos);
      tplInput.focus();
      tplInput.dispatchEvent(new Event('input', { bubbles: true }));
    });
  }

  // ---- Live preview: render the template against a sample upload.
  function refreshPreview() {
    if (!previewEl) return;
    const tpl = (tplInput?.value || '').trim() || '{user}/{filename}';
    const root_ = (rootInput?.value || '').trim() || '<root>';
    const sample = {
      user: 'dl1',
      filename: 'doc-12345.pdf',
      basename: 'doc-12345',
      ext: '.pdf',
      trackid: 'a1b2c3d4',
      run_id: 'run-1700000000',
      date: new Date().toISOString().slice(0, 10),
      datetime: new Date().toISOString().slice(0, 19).replace(/[T:]/g, '-'),
    };
    const rendered = renderTemplateClient(tpl, sample);
    previewEl.textContent = root_.replace(/\/+$/, '') + '/' + rendered.replace(/^\/+/, '');
  }
  tplInput?.addEventListener('input', refreshPreview);
  rootInput?.addEventListener('input', refreshPreview);
  refreshPreview();

  // ---- Misconfig warning + Probe.
  function refreshWarning() {
    if (!warnEl) return;
    let msg = '';
    if (kindValue.value === 'local-disk') {
      if (!(rootInput?.value || '').trim()) {
        msg = 'Local disk selected but no root directory entered — downloads will be discarded silently.';
      }
    }
    warnEl.hidden = !msg;
    warnEl.textContent = msg;
  }
  rootInput?.addEventListener('input', refreshWarning);
  refreshWarning();

  if (probeBtn && probeOut) {
    probeBtn.addEventListener('click', async (ev) => {
      ev.preventDefault();
      probeOut.textContent = 'Probing…';
      const cfg = readSink() || { kind: kindValue.value, root: (rootInput?.value || '').trim() };
      try {
        const r = await fetch('/api/probe-sink', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'sftp-loadtest' },
          body: JSON.stringify(cfg),
        });
        const j = await r.json();
        if (j.ok) {
          probeOut.textContent = j.note ? j.note : `OK — ${j.root} is writable`;
        } else {
          probeOut.textContent = 'Error: ' + (j.error || 'unknown');
        }
      } catch (e) { probeOut.textContent = 'Probe failed: ' + e.message; }
    });
  }
}

// hasWailsPicker returns true when the Wails desktop runtime has bound
// the named App method onto window.go. Web builds (CLI/server SKU) skip
// the binding — we hide the Browse buttons instead of stubbing them.
function hasWailsPicker(name) {
  return typeof window !== 'undefined' &&
         typeof window.go === 'object' &&
         window.go.main &&
         typeof window.go.main.App === 'object' &&
         typeof window.go.main.App[name] === 'function';
}

// renderTemplateClient mirrors internal/sink/sink.go's renderTemplate so
// the live preview shows the same path the runner would write. Keep
// this in sync with the Go-side variable list.
function renderTemplateClient(tpl, vars) {
  return tpl.replace(/\$?\{([a-z_]+)\}/g, (_, k) => vars[k] !== undefined ? vars[k] : '{' + k + '}');
}

// formatProbeResult turns the /api/probe-source response into a one-line
// human-readable summary. Failures show the error; success shows
// "<n> files, <bytes>" with the first three filenames.
function formatProbeResult(j) {
  if (!j.ok) return 'Error: ' + (j.error || 'unknown');
  if (j.kind === 'synthetic' || (!j.files || j.files.length === 0)) {
    return j.note || 'OK';
  }
  const total = j.total_bytes || 0;
  const head = j.files.slice(0, 3).map((f) => {
    if (f.error) return f.path + ' (' + f.error + ')';
    return f.path + ' (' + humanBytes(f.size) + ')';
  });
  let line = `OK — ${j.files.length} file${j.files.length === 1 ? '' : 's'}, ${humanBytes(total)} total`;
  if (head.length) line += '\n  ' + head.join('\n  ');
  if (j.files.length > 3) line += `\n  …and ${j.files.length - 3} more`;
  return line;
}

function humanBytes(n) {
  if (n < 1024) return n + ' B';
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB';
  if (n < 1024 * 1024 * 1024) return (n / (1024 * 1024)).toFixed(1) + ' MB';
  return (n / (1024 * 1024 * 1024)).toFixed(2) + ' GB';
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
