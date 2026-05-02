import { guideRequiredFields } from './guidance.js';

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

  // Smart auto-link: when a local source is configured AND the
  // download flow is enabled AND the sink is on local-disk with
  // empty root, derive the sink root from the source dir. One-shot
  // — never overrides operator input. Re-runs on relevant changes
  // so a freshly-typed source dir or a freshly-flipped sink kind
  // picks it up.
  installSmartAutoLink();

  // Bridge to legacy.js (non-module): legacy form-serializer needs to
  // read the picker state when assembling /api/start payload, and the
  // import-config path needs to populate fields from JSON. Expose the
  // public functions on window so the regular-script consumer can
  // call them without an import statement.
  if (typeof window !== 'undefined') {
    window.__srcSink = { readSource, readSink, applySource, applySink, applySmartLink };
  }
}

// applySmartLink — fills empty sink fields with sensible defaults
// derived from the configured local source. Idempotent: anything the
// operator typed sticks; we only touch empty fields.
//
// Rules (today):
//   1. sink kind === local-disk AND sink root empty AND any source
//      has a non-empty local dir → set sink root to "<srcDir>-downloads".
//
// Future rules can layer in here without touching the wire-up.
function applySmartLink() {
  const sinkRoot = document.querySelector('[data-role="sink-disclosure"]');
  if (!sinkRoot) return;
  const sinkKindEl  = sinkRoot.querySelector('[data-role="sink-kind-value"]');
  const sinkRootEl  = sinkRoot.querySelector('[data-role="sink-root"]');
  if (!sinkKindEl || !sinkRootEl) return;
  if (sinkKindEl.value !== 'local-disk') return;
  if ((sinkRootEl.value || '').trim() !== '') return; // operator already set it

  // Find the first source disclosure with a non-empty local dir.
  let sourceDir = '';
  for (const root of document.querySelectorAll('[data-role="source-disclosure"]')) {
    const kind = root.querySelector('[data-role="src-kind-value"]')?.value;
    if (kind === 'local-dir') {
      const d = (root.querySelector('[data-role="src-dir"]')?.value || '').trim();
      if (d) { sourceDir = d; break; }
    } else if (kind === 'local-files') {
      // Use the parent of the first listed file as a reasonable proxy.
      const lines = (root.querySelector('[data-role="src-files"]')?.value || '')
        .split('\n').map((s) => s.trim()).filter(Boolean);
      if (lines.length) {
        const parent = lines[0].replace(/\/+[^/]*$/, '');
        if (parent) { sourceDir = parent; break; }
      }
    }
  }
  if (!sourceDir) return;
  // Derive: <dir>-downloads as a sibling. Clean trailing slashes first.
  const derived = sourceDir.replace(/\/+$/, '') + '-downloads';
  sinkRootEl.value = derived;
  sinkRootEl.dispatchEvent(new Event('input', { bubbles: true }));
}

// installSmartAutoLink wires DOM listeners on every input/picker the
// auto-link rules read. Each emits a microtask-delayed evaluation so
// applySource() / applySink() can finish their own writes first.
function installSmartAutoLink() {
  const schedule = () => Promise.resolve().then(applySmartLink);

  document.querySelectorAll('[data-role="source-disclosure"]').forEach((root) => {
    root.querySelector('[data-role="src-dir"]')?.addEventListener('input', schedule);
    root.querySelector('[data-role="src-files"]')?.addEventListener('input', schedule);
    root.querySelectorAll('[data-role="src-kind"] button').forEach((btn) => {
      btn.addEventListener('click', schedule);
    });
  });
  const sinkRoot = document.querySelector('[data-role="sink-disclosure"]');
  if (sinkRoot) {
    sinkRoot.querySelectorAll('[data-role="sink-kind"] button').forEach((btn) => {
      btn.addEventListener('click', schedule);
    });
  }
  // Run once on mount so an imported config triggers it.
  schedule();
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
  const probeMatrix= root.querySelector('[data-role="src-probe-matrix"]');
  const warnEl     = root.querySelector('[data-role="src-warn"]');
  const filesText  = root.querySelector('[data-role="src-files"]');
  const dirInput   = root.querySelector('[data-role="src-dir"]');
  const advText    = root.querySelector('[data-role="src-advanced"]');
  const advError   = root.querySelector('[data-role="src-advanced-error"]');
  const advDisclosure = root.querySelector('[data-role="src-advanced-disclosure"]');
  const advToggleBtn  = root.querySelector('[data-role="src-advanced-toggle"]');
  const realfileHint  = root.querySelector('[data-role="src-realfile-hint"]');
  const filesBrowse= root.querySelector('[data-role="src-files-browse"]');
  const dirBrowse  = root.querySelector('[data-role="src-dir-browse"]');
  const layoutPicker = root.querySelector('[data-role="src-layout"]');
  const layoutValue  = root.querySelector('[data-role="src-layout-value"]');
  const layoutHelp   = root.querySelector('[data-role="src-layout-help"]');
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
    // Trailing real-file hint only applies to disk-backed sources.
    if (realfileHint) realfileHint.hidden = kind === 'synthetic';
    // Advanced-JSON escape hatch: only useful when there's a real
    // source. Synthetic has nothing to override per-user.
    if (advToggleBtn) {
      const advHasContent = (advText?.value || '').trim().length > 0;
      advToggleBtn.hidden = kind === 'synthetic' && !advHasContent;
    }
    // If the operator flips to synthetic AND advanced JSON is empty,
    // collapse the disclosure too — no override semantics apply.
    if (advDisclosure && kind === 'synthetic') {
      const advHasContent = (advText?.value || '').trim().length > 0;
      if (!advHasContent) {
        advDisclosure.hidden = true;
        advDisclosure.open = false;
      }
    }
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

  // ---- Layout picker (only meaningful for kind=local-dir) — drives the
  // "n users, n files" knob: flat / by-user / by-pattern / by-user-pattern.
  if (layoutPicker && layoutValue) {
    function applyLayout(layout) {
      layoutValue.value = layout;
      layoutPicker.querySelectorAll('button').forEach((b) => {
        b.setAttribute('aria-pressed', b.dataset.value === layout ? 'true' : 'false');
      });
      if (layoutHelp) {
        layoutHelp.querySelectorAll('[data-layout-help]').forEach((s) => {
          s.hidden = s.dataset.layoutHelp !== layout;
        });
      }
    }
    layoutPicker.querySelectorAll('button').forEach((btn) => {
      btn.addEventListener('click', (ev) => {
        ev.preventDefault();
        applyLayout(btn.dataset.value);
      });
    });
    applyLayout(layoutValue.value || 'flat');
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

  // Advanced-JSON escape hatch. The disclosure is hidden by default —
  // 95% of operators reach the same outcome via the layout picker
  // (by-user / by-pattern). Power users get the disclosure via this
  // small toggle, OR it auto-reveals when an imported config carries
  // per_user / per_pattern.
  if (advToggleBtn && advDisclosure) {
    advToggleBtn.addEventListener('click', (ev) => {
      ev.preventDefault();
      advDisclosure.hidden = false;
      advDisclosure.open = true;
      advToggleBtn.hidden = true; // one-shot — disclosure is now in charge
      advText?.focus();
    });
  }

  // ---- Probe button. For non-flat layouts the backend wants a list of
  // sample users so it can resolve <root>/<username>/<glob> per row.
  // We pull them from the same load's CSV textarea — operator already
  // typed them up there, no point asking twice.
  if (probeBtn && probeOut) {
    probeBtn.addEventListener('click', async (ev) => {
      ev.preventDefault();
      // Empty-field guidance — the picker can be on local-files /
      // local-dir but if the textarea / dir input is blank,
      // readSource() returns null and the probe used to fire with
      // an empty config that the backend friendly-rejected. Catch
      // that here so the operator gets pointed at the right field.
      const k = kindValue.value;
      if (k === 'local-files') {
        if (!guideRequiredFields([{ el: filesText, label: 'Files (one path per line)' }],
            { action: 'probe the source' })) return;
      } else if (k === 'local-dir') {
        if (!guideRequiredFields([{ el: dirInput, label: 'Directory' }],
            { action: 'probe the source' })) return;
      }
      probeOut.textContent = 'Probing…';
      if (probeMatrix) { probeMatrix.hidden = true; probeMatrix.innerHTML = ''; }
      const cfg = readSource(root.dataset.kind) || { kind: kindValue.value };
      const layout = (cfg.kind === 'local-dir') ? (cfg.layout || 'flat') : 'flat';
      const sampleUsers = (layout !== 'flat') ? readSampleUsers(root.dataset.kind) : [];
      const body = (layout !== 'flat')
        ? { source: cfg, users: sampleUsers }
        : cfg;
      try {
        const r = await fetch('/api/probe-source', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'sftp-loadtest' },
          body: JSON.stringify(body),
        });
        const j = await r.json();
        probeOut.textContent = formatProbeResult(j);
        if (j.users && probeMatrix) renderProbeMatrix(probeMatrix, j);
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
      // Empty-root guidance for local-disk sink — discard kind has
      // no field requirements so always passes through.
      if (kindValue.value === 'local-disk') {
        if (!guideRequiredFields([{ el: rootInput, label: 'Root directory' }],
            { action: 'probe the sink' })) return;
      }
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

// readSampleUsers parses the relevant load's user CSV textarea into the
// {username, pattern} pairs /api/probe-source needs. Each user contributes
// one entry per pattern (the runner picks patterns round-robin per upload,
// so probing every pattern matches what the runtime will actually do).
// Returns [] when the textarea is empty so the backend can still answer
// with a "supply users" hint instead of erroring.
function readSampleUsers(kind /* "normal" | "large" */) {
  const id = kind === 'large' ? 'large_users' : 'normal_users';
  const ta = document.getElementById(id);
  if (!ta) return [];
  const out = [];
  const seen = new Set();
  (ta.value || '').split('\n').forEach((line) => {
    const cols = line.split(',').map((s) => s.trim()).filter(Boolean);
    if (cols.length < 3) return;
    const username = cols[0];
    cols.slice(2).forEach((pattern) => {
      const k = username + '|' + pattern;
      if (seen.has(k)) return;
      seen.add(k);
      out.push({ username, pattern });
    });
  });
  return out;
}

// renderProbeMatrix turns the per-user response from /api/probe-source
// into a compact table inside the disclosure: one row per (user,
// pattern) with file count + total size, or the friendly error when
// the resolution failed for that account.
function renderProbeMatrix(host, j) {
  if (!j.users || !j.users.length) {
    host.hidden = true;
    host.innerHTML = '';
    return;
  }
  const head = `<div class="probe-matrix-head">
    <span>Account</span><span>Pattern</span><span>Files</span><span>Total</span>
  </div>`;
  const rows = j.users.map((u) => {
    if (!u.ok) {
      return `<div class="probe-matrix-row probe-matrix-row-err">
        <span class="mono">${escapeHtml(u.username || '—')}</span>
        <span class="mono">${escapeHtml(u.pattern || '—')}</span>
        <span colspan="2" class="probe-matrix-err">${escapeHtml(u.error || 'unresolved')}</span>
      </div>`;
    }
    const n = (u.files || []).length;
    return `<div class="probe-matrix-row">
      <span class="mono">${escapeHtml(u.username)}</span>
      <span class="mono">${escapeHtml(u.pattern || '—')}</span>
      <span>${n}</span>
      <span>${humanBytes(u.total_bytes || 0)}</span>
    </div>`;
  }).join('');
  host.innerHTML = head + rows;
  host.hidden = false;
}

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  }[c]));
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
    // Layout drives the "n users, n files" knob. Omit when "flat" so
    // the wire format stays identical to v0.14.0–3 for unchanged
    // configs.
    const layout = root.querySelector('[data-role="src-layout-value"]')?.value || 'flat';
    if (layout && layout !== 'flat') out.layout = layout;
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
    // Restore layout (defaults to flat when omitted) so the segmented
    // picker + the right teaching-copy span both reflect the imported
    // state.
    const layout = src.layout || 'flat';
    const lv = root.querySelector('[data-role="src-layout-value"]');
    const lp = root.querySelector('[data-role="src-layout"]');
    const lh = root.querySelector('[data-role="src-layout-help"]');
    if (lv) lv.value = layout;
    if (lp) lp.querySelectorAll('button').forEach((b) => {
      b.setAttribute('aria-pressed', b.dataset.value === layout ? 'true' : 'false');
    });
    if (lh) lh.querySelectorAll('[data-layout-help]').forEach((s) => {
      s.hidden = s.dataset.layoutHelp !== layout;
    });
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
  const advDisclosure = root.querySelector('[data-role="src-advanced-disclosure"]');
  const advToggleBtn  = root.querySelector('[data-role="src-advanced-toggle"]');
  if (adv) {
    const merged = {};
    if (src && src.per_user)    merged.per_user = src.per_user;
    if (src && src.per_pattern) merged.per_pattern = src.per_pattern;
    adv.value = Object.keys(merged).length ? JSON.stringify(merged, null, 2) : '';
    // If the imported config carries overrides, auto-reveal the
    // (otherwise hidden) disclosure so the operator can see and edit
    // them. Hide the escape-hatch button — disclosure is now in
    // charge.
    if (advDisclosure) {
      const hasOverrides = Object.keys(merged).length > 0;
      advDisclosure.hidden = !hasOverrides;
      if (hasOverrides) advDisclosure.open = true;
      if (advToggleBtn) advToggleBtn.hidden = hasOverrides; // hide if disclosure is now visible
    }
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
