// configure-redesign.js — v0.9.4 Configure-screen overhaul.
//
// Wraps the existing Configure-view DOM (Quick Checks card + the legacy
// .grid that holds normalCard / downloadCard / .actions / #err) into a
// deliberate, semantic section layout:
//
//   ┌───────────────────────────────────────┬──────────────────┐
//   │ Target          (Quick Checks)        │                  │
//   ├───────────────────────────────────────┤   Run summary    │
//   │ Workload (sticky-headed group)        │   (sticky rail)  │
//   │   ├─ Normal load      [switch]        │                  │
//   │   ├─ Large file       [switch]        │                  │
//   │   └─ Download         [switch]        │                  │
//   ├───────────────────────────────────────┤                  │
//   │ Resource limits (parallel / duration  │                  │
//   │   / poll / timeout / disable-after)   │                  │
//   ├───────────────────────────────────────┴──────────────────┤
//   │  Action zone:  [ Start run ]   Stop · CSV · Export       │
//   └──────────────────────────────────────────────────────────┘
//
// Hard rules:
//   - Every legacy id (#host, #port, #folder, #parallel, #duration, #poll,
//     #timeout_min, #max_fails, #fpm, #normal_users, #large_users,
//     #download_users, #startBtn, #stopBtn, etc.) stays attached.
//   - upload-restructure.js has already nested largeCard's body INTO the
//     #normalCard advanced disclosure; we leave that as-is.
//   - Legacy #connCard is hidden by shell.css; we promote each of its
//     run-mechanics fields into a new "Resource limits" section so they
//     are reachable from the redesigned form. upload-restructure already
//     moved them to inside #normalCard — we move them out to their own
//     section so the workload card stays focused on workload.
//   - On narrow viewports (<1100px) the right rail collapses to a static
//     summary block above the action zone.

const SUMMARY_REFRESH_MS = 1500;

// Minimal stroke icons used in the action-zone toolbar buttons. Kept
// inline (no font / no SVG sprite dep) so they ship with the binary.
const ICON_PLAY =
  '<svg viewBox="0 0 16 16" fill="currentColor" aria-hidden="true">' +
  '<path d="M5.5 3.2v9.6L13 8z"/></svg>';
const ICON_STOP =
  '<svg viewBox="0 0 16 16" fill="currentColor" aria-hidden="true">' +
  '<rect x="4" y="4" width="8" height="8" rx="1.5"/></svg>';
const ICON_DOWNLOAD =
  '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
  '<path d="M8 2v8"/><path d="M4.5 7L8 10.5 11.5 7"/><path d="M3 13h10"/></svg>';
const ICON_UPLOAD_CLOUD =
  '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
  '<path d="M8 11V5"/><path d="M5 8l3-3 3 3"/><path d="M3 13h10"/></svg>';
const ICON_DOWNLOAD_CLOUD =
  '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
  '<path d="M8 5v6"/><path d="M5 8l3 3 3-3"/><path d="M3 3h10"/></svg>';
const ICON_SAVE =
  '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
  '<path d="M3 3h8l2 2v8H3z"/><path d="M5 3v3.5h6V3"/><path d="M5 9.5h6V13H5z"/></svg>';

// decorateActionBtn — prepends a small icon span to a legacy button so
// the action-zone toolbar reads as a coherent set. Idempotent: skips if
// the button already carries one.
function decorateActionBtn(el, svg) {
  if (!el || el.querySelector('.cfg-btn-icon')) return;
  const icon = document.createElement('span');
  icon.className = 'cfg-btn-icon';
  icon.setAttribute('aria-hidden', 'true');
  icon.innerHTML = svg;
  el.insertBefore(icon, el.firstChild);
}

export function mountConfigureRedesign() {
  const view = document.querySelector('.shell-main [data-view="configure"]');
  if (!view) return;
  if (view.dataset.redesigned === '1') return;
  view.dataset.redesigned = '1';

  // --- locate the major existing pieces --------------------------------
  const connectionCard = view.querySelector('[data-component="connection"]');
  const grid = view.querySelector('.grid');
  if (!grid) return; // nothing to redesign — bail quietly

  const normalCard   = grid.querySelector('#normalCard');
  const downloadCard = grid.querySelector('#downloadCard');
  const largeCard    = grid.querySelector('#largeCard');           // hidden by upload-restructure
  // Multiple .actions rows live inside the grid (one inside #connCard with
  // the legacy #probeBtn, plus the Start/Stop row at the bottom). We want
  // the one carrying #startBtn — pick by content, not by document order.
  const startBtnEl   = grid.querySelector('#startBtn');
  const actionsRow   = startBtnEl ? startBtnEl.closest('.actions') : null;
  const errEl        = grid.querySelector('#err');

  // upload-restructure relocated the run-mechanics rows (#parallel,
  // #duration, #poll, #timeout_min, #max_fails) into a child
  // .upload-run-mechanics group inside #normalCard. We pull THAT group
  // out and host it in a dedicated "Resource limits" section so workload
  // and resource limits aren't crammed into the same card.
  const runMechanics = normalCard?.querySelector('.upload-run-mechanics') || null;

  // --- build the wrapper layout ----------------------------------------
  const layout = document.createElement('div');
  layout.className = 'configure-layout';
  layout.innerHTML = `
    <div class="configure-main">
      <!-- Prelude toolbar — bootstrap actions that belong BEFORE the form
           (Import config). The user wants Import reachable without
           scrolling all the way to the action zone at the bottom. -->
      <div class="cfg-prelude" data-slot="prelude"></div>

      <section class="cfg-section" data-section="target">
        <header class="cfg-section-head">
          <div class="cfg-section-eyebrow">1 · Target</div>
          <p class="cfg-section-sub">Host, port, and credentials — verified live by Test connection.</p>
        </header>
        <div class="cfg-section-body" data-slot="target"></div>
      </section>

      <section class="cfg-section" data-section="workload">
        <header class="cfg-section-head">
          <div class="cfg-section-eyebrow">2 · Workload</div>
          <p class="cfg-section-sub">Combine up to three flows. Each can be enabled independently.</p>
          <!-- v0.18.4 — Duration is the run-shape decision the operator
               makes BEFORE picking flows. Hoisted here from "Run
               controls" so it sits next to the workload framing
               instead of buried at the bottom of the form. -->
          <div class="cfg-workload-headline" data-slot="workload-headline"></div>
        </header>
        <div class="cfg-section-body cfg-workload-body" data-slot="workload"></div>
      </section>

      <section class="cfg-section" data-section="limits">
        <header class="cfg-section-head">
          <div class="cfg-section-eyebrow">3 · Resource limits</div>
          <p class="cfg-section-sub">Per-user parallelism (split for upload + download), plus run-wide duration, polling, timeouts, and the disable-after-fails ceiling.</p>
        </header>
        <div class="cfg-section-body" data-slot="limits"></div>
      </section>

      <section class="cfg-actionzone" data-section="actions">
        <div class="cfg-actionzone-primary" data-slot="primary"></div>
        <div class="cfg-actionzone-secondary" data-slot="secondary"></div>
        <div class="cfg-actionzone-error" data-slot="err"></div>
      </section>
    </div>

    <!-- Slim sticky run-summary strip. Replaces the wide right rail; reads
         as one row at the bottom of Configure with an inline play/stop
         icon button (mirrors the topbar Run/Stop). The chips collapse
         off-screen on narrow widths but the play button stays. -->
    <div class="cfg-summary-bar" data-section="summary" aria-label="Run summary">
      <button type="button" class="cfg-summary-go" data-role="summary-go"
              aria-label="Start run" title="Start run (⌘↵)">
        <svg viewBox="0 0 16 16" width="14" height="14" fill="currentColor" aria-hidden="true"
             data-role="summary-icon-play"><path d="M5.5 3.2v9.6L13 8z"/></svg>
        <svg viewBox="0 0 16 16" width="14" height="14" fill="currentColor" aria-hidden="true"
             data-role="summary-icon-stop" style="display:none"><rect x="4" y="4" width="8" height="8" rx="1.5"/></svg>
      </button>
      <dl class="cfg-summary-chips" data-role="summary-defs"></dl>
      <div class="cfg-summary-flows-inline" data-role="summary-foot"></div>
    </div>
  `;

  // Mount layout INTO the configure view, before .grid (so .grid can be
  // emptied and removed below).
  view.insertBefore(layout, grid);

  // --- TARGET slot ------------------------------------------------------
  const targetSlot = layout.querySelector('[data-slot="target"]');
  if (connectionCard) targetSlot.appendChild(connectionCard);

  // --- WORKLOAD slot — wrap each card with a switch-style header -------
  const workloadSlot = layout.querySelector('[data-slot="workload"]');
  // Card 1: Normal (renamed by upload-restructure to "Upload"). The
  // switch maps to #normal_enabled. We rename the visible legacy header
  // to a slimmer pill and keep the legacy <header> intact for any tests.
  if (normalCard) {
    const wrap = makeWorkloadWrap({
      title: 'Upload',
      subtitle: 'Steady cadence of small / medium files — the primary load.',
      enabledId: 'normal_enabled',
      cardEl: normalCard,
    });
    workloadSlot.appendChild(wrap);
  }
  // Card 2: Large-file mode is now nested INSIDE the upload card's
  // advanced disclosure. We don't re-surface largeCard (its content was
  // moved out by upload-restructure); the legacy outer largeCard remains
  // hidden in place. We surface a separate switch only if largeCard
  // still has body content (defensive — it should not).
  if (largeCard && largeCard.querySelector('.body')?.children.length > 0) {
    const wrap = makeWorkloadWrap({
      title: 'Large-file load',
      subtitle: 'Large payloads at a slower cadence.',
      enabledId: 'large_enabled',
      cardEl: largeCard,
    });
    workloadSlot.appendChild(wrap);
  }
  // Card 3: Download (round-trip).
  if (downloadCard) {
    const wrap = makeWorkloadWrap({
      title: 'Download (round-trip)',
      subtitle: 'Pull files back from the outbox to measure end-to-end latency.',
      enabledId: 'download_enabled',
      cardEl: downloadCard,
    });
    workloadSlot.appendChild(wrap);
  }

  // --- RESOURCE LIMITS slot --------------------------------------------
  // Split into three sub-groups so per-stream-direction parallelism is
  // categorised away from the run-wide knobs:
  //   Upload    → #parallel  (streams / user)
  //   Download  → #dparallel (streams / user; relocated out of the
  //               Download workload card so the workload card holds
  //               only workload-shape concerns: folder + match mode)
  //   Run       → #duration / #poll / #timeout_min / #max_fails
  const limitsSlot = layout.querySelector('[data-slot="limits"]');
  limitsSlot.innerHTML = `
    <div class="cfg-limits-stream">
      <div class="cfg-limits-group" data-group="upload">
        <div class="cfg-limits-eyebrow">Upload</div>
        <div class="cfg-limits-rows" data-slot="limits-upload"></div>
      </div>
      <div class="cfg-limits-group" data-group="download">
        <div class="cfg-limits-eyebrow">Download</div>
        <div class="cfg-limits-rows" data-slot="limits-download"></div>
      </div>
    </div>
    <div class="cfg-limits-group" data-group="run">
      <div class="cfg-limits-eyebrow">Run controls</div>
      <div class="cfg-limits-rows" data-slot="limits-run"></div>
    </div>
  `;
  const upSlot   = limitsSlot.querySelector('[data-slot="limits-upload"]');
  const dlSlot   = limitsSlot.querySelector('[data-slot="limits-download"]');
  const runSlot  = limitsSlot.querySelector('[data-slot="limits-run"]');

  // Resolve the rows. The legacy form gives us .row containers with the
  // <input> inside; we steal each <input>'s containing field-cell so the
  // labels travel with their input.
  function fieldFor(id) {
    const el = document.getElementById(id);
    if (!el) return null;
    // The legacy form wraps <label>+<input> in a plain <div>. The .row
    // is its grandparent; grab the immediate <div> wrapper so we can
    // rehome a single field independently of any siblings on the row.
    let cell = el;
    while (cell.parentElement && !cell.parentElement.classList.contains('row')) {
      cell = cell.parentElement;
    }
    return cell.parentElement && cell.parentElement.classList.contains('row') ? cell : null;
  }

  const fParallel  = fieldFor('parallel');
  const fDuration  = fieldFor('duration');
  const fPoll      = fieldFor('poll');
  const fTimeout   = fieldFor('timeout_min');
  const fMaxFails  = fieldFor('max_fails');
  const fDParallel = fieldFor('dparallel');
  // v0.18.0 — quality-control knobs land in the Run controls
  // section: a numeric floor with its warmup, plus a checkbox for
  // SHA-256 round-trip verification. fieldFor walks up to the
  // enclosing .row > <div> wrapper exactly the same way the
  // pre-existing extractions do, so the labels follow.
  const fSpeedFloor   = fieldFor('speed_floor_percent');
  const fSpeedWarmup  = fieldFor('speed_floor_warmup_sec');
  const fSpeedBreach  = fieldFor('speed_floor_breach_sec');
  // v0.18.x — verify_hashes is no longer extracted into Run controls;
  // it lives inside the Download workload card so the operator
  // discovers it next to the round-trip mode picker (the only place
  // a hash check is meaningful — no download phase, nothing to
  // verify against).

  if (fParallel)  upSlot.appendChild(fParallel);
  if (fDParallel) dlSlot.appendChild(fDParallel);
  // v0.18.4 — Duration goes to the Workload section's headline slot
  // (right under "Combine up to three flows…") instead of the bottom
  // Run controls group. The remaining run-wide knobs stay in their
  // original limits group.
  const workloadHeadline = layout.querySelector('[data-slot="workload-headline"]');
  if (fDuration && workloadHeadline) workloadHeadline.appendChild(fDuration);
  for (const f of [fPoll, fTimeout, fMaxFails, fSpeedFloor, fSpeedWarmup, fSpeedBreach]) {
    if (f) runSlot.appendChild(f);
  }

  // The runMechanics scaffold (and any leftover .row husks now empty
  // after we extracted their cells) remain in the DOM but are visually
  // collapsed via .configure-legacy-residue, so legacy.js's $('parallel')
  // lookups still find the inputs.
  if (runMechanics) {
    const oldHeading = runMechanics.querySelector('.upload-run-mechanics-heading');
    if (oldHeading) oldHeading.remove();
  }

  // --- ACTION ZONE -----------------------------------------------------
  const primarySlot = layout.querySelector('[data-slot="primary"]');
  const secondarySlot = layout.querySelector('[data-slot="secondary"]');
  const errSlot = layout.querySelector('[data-slot="err"]');

  if (actionsRow) {
    const startBtn = actionsRow.querySelector('#startBtn');
    const stopBtn  = actionsRow.querySelector('#stopBtn');
    const csvBtn   = actionsRow.querySelector('#csvBtn');
    if (startBtn) {
      startBtn.classList.add('cfg-cta');
      startBtn.dataset.variant = 'primary';
      decorateActionBtn(startBtn, ICON_PLAY);
      primarySlot.appendChild(startBtn);
    }
    if (stopBtn) {
      stopBtn.classList.add('cfg-secondary-action');
      stopBtn.dataset.tone = 'danger';
      decorateActionBtn(stopBtn, ICON_STOP);
      secondarySlot.appendChild(stopBtn);
    }
    if (csvBtn) {
      csvBtn.classList.add('cfg-secondary-action');
      decorateActionBtn(csvBtn, ICON_DOWNLOAD);
      secondarySlot.appendChild(csvBtn);
    }
    // Sweep any remaining utility buttons (Export config / Import config
    // proxies that run-actions.js has already inserted) and align them.
    // Import config is routed to a TOP prelude slot so users can load a
    // saved JSON without scrolling to the bottom of the form. Export
    // (and any other tail-of-flow utility) stays in the action zone.
    const preludeSlot = layout.querySelector('[data-slot="prelude"]');
    const seen = new Set([startBtn, stopBtn, csvBtn]);
    actionsRow.querySelectorAll('button, a').forEach((el) => {
      if (!el || seen.has(el) || !el.parentElement) return;
      seen.add(el);
      el.classList.add('cfg-secondary-action');
      const txt = (el.textContent || '').toLowerCase();
      if (txt.includes('export')) {
        decorateActionBtn(el, ICON_UPLOAD_CLOUD);
        secondarySlot.appendChild(el);
      } else if (txt.includes('import')) {
        decorateActionBtn(el, ICON_DOWNLOAD_CLOUD);
        // Drop the secondary-action class for the prelude variant so it
        // can pick up its own pill styling without competing with the
        // toolbar look.
        el.classList.remove('cfg-secondary-action');
        el.classList.add('cfg-prelude-import');
        preludeSlot.appendChild(el);
      } else {
        secondarySlot.appendChild(el);
      }
    });
    actionsRow.remove();
  }
  if (errEl) errSlot.appendChild(errEl);

  // Save current config as a named preset. Lives in the prelude
  // alongside Import config so the operator can save / load presets
  // without leaving the Configure view (the only path before this was
  // ⌘K → "Save current config…", which most operators never discover).
  const preludeSlot = layout.querySelector('[data-slot="prelude"]');
  if (preludeSlot && !preludeSlot.querySelector('[data-role="save-preset"]')) {
    const saveBtn = document.createElement('button');
    saveBtn.type = 'button';
    saveBtn.className = 'cfg-prelude-import'; // share the prelude pill style
    saveBtn.dataset.role = 'save-preset';
    saveBtn.title = 'Save the current form as a named preset (sidebar → Saved configs)';
    decorateActionBtn(saveBtn, ICON_SAVE);
    const label = document.createElement('span');
    label.textContent = 'Save preset…';
    saveBtn.appendChild(label);
    saveBtn.addEventListener('click', async (ev) => {
      ev.preventDefault();
      const { promptSavePreset } = await import('./save-preset.js');
      promptSavePreset();
    });
    // Insert BEFORE Import config so the order reads "Save | Import" —
    // saving is a more frequent action than importing a JSON file.
    preludeSlot.insertBefore(saveBtn, preludeSlot.firstChild);
  }

  // The .grid wrapper is now empty (or only carries the hidden #connCard
  // and #largeCard). Keep it attached but visually collapse — its
  // children must remain in the DOM because legacy.js reads from them.
  grid.classList.add('configure-legacy-residue');

  // --- SUMMARY bar wiring -----------------------------------------------
  // The slim bottom strip carries: [▶/■ go] · target · folder · users · fpm · duration · files · flow chips.
  // It mirrors the topbar Run/Stop so the operator never has to scroll
  // back up to launch — the bar travels with the page (sticky bottom).
  const defs = layout.querySelector('[data-role="summary-defs"]');
  const flowsEl = layout.querySelector('[data-role="summary-foot"]');
  const goBtn = layout.querySelector('[data-role="summary-go"]');
  const iconPlay = goBtn?.querySelector('[data-role="summary-icon-play"]');
  const iconStop = goBtn?.querySelector('[data-role="summary-icon-stop"]');

  // Wire go button — delegate to the (now relocated) #startBtn / #stopBtn.
  // Read state from the topbar status pill so play/stop stays in sync
  // with whatever drove the last state change (preflight modal, ⌘↵ key,
  // legacy click, etc.).
  function syncGoState() {
    if (!goBtn) return;
    const status = document.querySelector('.shell-topbar-status');
    const active = status && status.dataset.state === 'active';
    goBtn.dataset.mode = active ? 'stop' : 'play';
    goBtn.setAttribute('aria-label', active ? 'Stop run' : 'Start run');
    goBtn.setAttribute('title', active ? 'Stop the active run (⌘.)' : 'Start run (⌘↵)');
    if (iconPlay) iconPlay.style.display = active ? 'none' : '';
    if (iconStop) iconStop.style.display = active ? '' : 'none';
  }
  if (goBtn) {
    goBtn.addEventListener('click', (ev) => {
      ev.preventDefault();
      const status = document.querySelector('.shell-topbar-status');
      const active = status && status.dataset.state === 'active';
      // Prefer the topbar Run/Stop buttons — they're properly disabled-
      // gated by pollStatus and route through the same handlers as the
      // legacy form. Fall back to the legacy buttons if the topbar isn't
      // wired (early boot / unit-test stub).
      const topbarRole = active ? 'topbar-stop' : 'topbar-run';
      const topbar = document.querySelector(`[data-role="${topbarRole}"]`);
      const legacy = document.getElementById(active ? 'stopBtn' : 'startBtn');
      const target = (topbar && !topbar.disabled) ? topbar
                   : (legacy && !legacy.disabled) ? legacy
                   : (topbar || legacy);
      target?.click();
    });
    // Watch the topbar status pill for state changes — pollStatus updates
    // its data-state every tick, so a MutationObserver catches go/stop.
    const status = document.querySelector('.shell-topbar-status');
    if (status) {
      const mo = new MutationObserver(syncGoState);
      mo.observe(status, { attributes: true, attributeFilter: ['data-state'] });
    }
    syncGoState();
  }

  function renderSummary() {
    const cfg = pullConfig();
    const ufpm = Number(cfg.files_per_minute || 0);
    const dur  = Number(cfg.duration_hours || 0);
    const eta  = dur ? formatHours(dur) : '—';
    const totalFiles = ufpm && dur ? Math.round(ufpm * dur * 60) : 0;
    const userParts = [];
    const nu = csvCount(cfg.normal_users_csv);   if (nu) userParts.push(`${nu}n`);
    const lu = csvCount(cfg.large_users_csv);    if (lu) userParts.push(`${lu}l`);
    const du = csvCount(cfg.download_users_csv); if (du) userParts.push(`${du}d`);
    const userText = userParts.length ? userParts.join(' · ') : '0';
    const proto = (cfg.protocol || 'sftp').toLowerCase();
    defs.innerHTML = `
      <span class="cfg-chip" data-role="chip-proto"><span class="cfg-chip-key">proto</span>
        <span class="cfg-chip-val">${escapeHTML(proto)}</span></span>
      <span class="cfg-chip" data-role="chip-target"><span class="cfg-chip-key">target</span>
        <span class="cfg-chip-val mono">${escapeHTML((cfg.host || '—'))}:${escapeHTML(String(cfg.port || ''))}</span></span>
      <span class="cfg-chip"><span class="cfg-chip-key">folder</span>
        <span class="cfg-chip-val mono">${escapeHTML(cfg.upload_folder || '—')}</span></span>
      <span class="cfg-chip"><span class="cfg-chip-key">users</span>
        <span class="cfg-chip-val">${escapeHTML(userText)}</span></span>
      <span class="cfg-chip"><span class="cfg-chip-key">fpm</span>
        <span class="cfg-chip-val">${escapeHTML(String(ufpm))}</span></span>
      <span class="cfg-chip"><span class="cfg-chip-key">dur</span>
        <span class="cfg-chip-val">${escapeHTML(eta)}</span></span>
      <span class="cfg-chip"><span class="cfg-chip-key">files</span>
        <span class="cfg-chip-val">${totalFiles ? totalFiles.toLocaleString() : '—'}</span></span>
      <span class="cfg-chip" data-role="chip-auth"><span class="cfg-chip-key">auth</span>
        <span class="cfg-chip-val">${cfg.private_key_pem ? 'key 🔑' : 'pass'}</span></span>
    `;
    const flows = [];
    if (cfg.normal_enabled)   flows.push('N');
    if (cfg.large_enabled)    flows.push('L');
    if (cfg.download_enabled) flows.push('D');
    flowsEl.innerHTML = flows.length
      ? flows.map((f) => `<span class="cfg-flow-dot" data-flow="${f}">${f}</span>`).join('')
      : '<span class="cfg-flow-empty" title="No flow enabled — toggle one above">—</span>';
  }
  renderSummary();
  setInterval(renderSummary, SUMMARY_REFRESH_MS);
  // Re-render on any input change inside the configure layout — instant feel.
  layout.addEventListener('input', renderSummary);
  layout.addEventListener('change', renderSummary);
}

// makeWorkloadWrap wraps a legacy workload .card with a switch-style
// header so each subsection feels like a sibling. The legacy header
// (with its checkbox label) is hidden — the switch takes over. Toggling
// the switch flips the legacy checkbox so legacy.js's submit handler
// reads the right value.
function makeWorkloadWrap({ title, subtitle, enabledId, cardEl }) {
  const wrap = document.createElement('section');
  wrap.className = 'cfg-workload-card';
  wrap.dataset.enabledId = enabledId;

  const enabledInput = document.getElementById(enabledId);
  const checked = enabledInput ? !!enabledInput.checked : false;
  wrap.dataset.enabled = String(checked);

  wrap.innerHTML = `
    <header class="cfg-workload-head">
      <div class="cfg-workload-titles">
        <div class="cfg-workload-title">${escapeHTML(title)}</div>
        <div class="cfg-workload-sub">${escapeHTML(subtitle)}</div>
      </div>
      <label class="switch" title="Enable / disable this flow">
        <input type="checkbox" data-role="workload-switch" ${checked ? 'checked' : ''}>
        <span class="switch-track"><span class="switch-thumb"></span></span>
        <span class="switch-state">${checked ? 'On' : 'Disabled'}</span>
      </label>
    </header>
    <div class="cfg-workload-body" data-role="workload-body"></div>
  `;
  // Move the legacy card INTO the body of the wrapper, BUT extract its
  // <header> first and host it OUTSIDE the body so the legacy
  // #*_enabled checkbox stays reachable when the body is collapsed
  // (turning a workload OFF hides the body — if the legacy checkbox is
  // inside that body it can no longer be re-enabled, breaking
  // import / restore / programmatic .check() flows).
  const bodyHost = wrap.querySelector('[data-role="workload-body"]');
  cardEl.classList.add('cfg-legacy-card');
  const legacyHeader = cardEl.querySelector(':scope > header');
  if (legacyHeader) {
    // Park the legacy header inside the wrap (NOT inside body), as a
    // sibling of the new switch row. CSS hides it visually while
    // keeping it in normal flow for visibility.
    legacyHeader.classList.add('cfg-legacy-header-park');
    wrap.appendChild(legacyHeader);
  }
  bodyHost.appendChild(cardEl);

  // Wire the switch to the legacy checkbox.
  const sw = wrap.querySelector('[data-role="workload-switch"]');
  const stateLabel = wrap.querySelector('.switch-state');
  function syncFromLegacy() {
    if (!enabledInput) return;
    const v = !!enabledInput.checked;
    sw.checked = v;
    wrap.dataset.enabled = String(v);
    stateLabel.textContent = v ? 'On' : 'Disabled';
  }
  function syncToLegacy() {
    if (!enabledInput) return;
    enabledInput.checked = sw.checked;
    enabledInput.dispatchEvent(new Event('change', { bubbles: true }));
    wrap.dataset.enabled = String(sw.checked);
    stateLabel.textContent = sw.checked ? 'On' : 'Disabled';
  }
  sw.addEventListener('change', syncToLegacy);
  if (enabledInput) enabledInput.addEventListener('change', syncFromLegacy);
  return wrap;
}

function pullConfig() {
  if (typeof window !== 'undefined' && typeof window.__sftplBuildRequestBody === 'function') {
    try { return window.__sftplBuildRequestBody(); } catch { /* ignore */ }
  }
  return {};
}
function csvCount(raw) {
  if (!raw) return 0;
  return String(raw).split(/\r?\n/).filter((l) => l.trim() && l.split(',').length >= 2).length;
}
function formatHours(h) {
  if (h < 1) return `${Math.round(h * 60)} min`;
  if (Number.isInteger(h)) return `${h} h`;
  return `${h.toFixed(2)} h`;
}
function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}
