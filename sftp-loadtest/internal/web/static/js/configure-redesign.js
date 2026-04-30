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
      <section class="cfg-section" data-section="target">
        <header class="cfg-section-head">
          <div class="cfg-section-eyebrow">1 · Target</div>
          <h2 class="cfg-section-title">Where am I targeting?</h2>
          <p class="cfg-section-sub">Host, port, folder, and credentials — verified live by Quick checks.</p>
        </header>
        <div class="cfg-section-body" data-slot="target"></div>
      </section>

      <section class="cfg-section" data-section="workload">
        <header class="cfg-section-head">
          <div class="cfg-section-eyebrow">2 · Workload</div>
          <h2 class="cfg-section-title">What's the workload shape?</h2>
          <p class="cfg-section-sub">Combine up to three flows. Each can be enabled independently.</p>
        </header>
        <div class="cfg-section-body cfg-workload-body" data-slot="workload"></div>
      </section>

      <section class="cfg-section" data-section="limits">
        <header class="cfg-section-head">
          <div class="cfg-section-eyebrow">3 · Resource limits</div>
          <h2 class="cfg-section-title">How long, how aggressively?</h2>
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

    <aside class="configure-rail" data-section="summary" aria-label="Run summary">
      <div class="configure-rail-sticky">
        <div class="cfg-section-eyebrow">4 · Run summary</div>
        <h2 class="cfg-section-title">Run summary</h2>
        <p class="cfg-section-sub">Live preview — updates as you edit.</p>
        <dl class="cfg-summary-defs" data-role="summary-defs"></dl>
        <div class="cfg-summary-foot" data-role="summary-foot"></div>
      </div>
    </aside>
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
      title: 'Normal load',
      subtitle: 'Steady cadence of small / medium files (the bulk of most tests).',
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

  if (fParallel)  upSlot.appendChild(fParallel);
  if (fDParallel) dlSlot.appendChild(fDParallel);
  for (const f of [fDuration, fPoll, fTimeout, fMaxFails]) {
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
      primarySlot.appendChild(startBtn);
    }
    // Move the rest (stop, CSV link, export, import proxies) into the
    // secondary row so the CTA stands alone. Use a Set to dedupe so a
    // child that matches both stopBtn/csvBtn and the generic
    // querySelectorAll('button, a') sweep doesn't get appended twice.
    const seen = new Set();
    const candidates = [stopBtn, csvBtn, ...actionsRow.querySelectorAll('button, a')];
    candidates.forEach((el) => {
      if (!el || !el.parentElement) return;
      if (el === startBtn) return;
      if (seen.has(el)) return;
      seen.add(el);
      el.classList.add('cfg-secondary-action');
      secondarySlot.appendChild(el);
    });
    actionsRow.remove();
  }
  if (errEl) errSlot.appendChild(errEl);

  // The .grid wrapper is now empty (or only carries the hidden #connCard
  // and #largeCard). Keep it attached but visually collapse — its
  // children must remain in the DOM because legacy.js reads from them.
  grid.classList.add('configure-legacy-residue');

  // --- SUMMARY rail wiring ---------------------------------------------
  const defs = layout.querySelector('[data-role="summary-defs"]');
  const foot = layout.querySelector('[data-role="summary-foot"]');
  function renderSummary() {
    const cfg = pullConfig();
    const ufpm = Number(cfg.files_per_minute || 0);
    const dur  = Number(cfg.duration_hours || 0);
    const eta  = dur ? formatHours(dur) : '—';
    const totalFiles = ufpm && dur ? Math.round(ufpm * dur * 60) : 0;
    defs.innerHTML = `
      <dt>Target</dt><dd class="mono">${escapeHTML((cfg.host || '—'))}:${escapeHTML(String(cfg.port || ''))}</dd>
      <dt>Folder</dt><dd class="mono">${escapeHTML(cfg.upload_folder || '—')}</dd>
      <dt>Users</dt><dd>${csvCount(cfg.normal_users_csv)} normal · ${csvCount(cfg.large_users_csv)} large · ${csvCount(cfg.download_users_csv)} dl</dd>
      <dt>Files / min</dt><dd>${escapeHTML(String(ufpm))}</dd>
      <dt>Duration</dt><dd>${escapeHTML(eta)}</dd>
      <dt>Streams / user</dt><dd>${escapeHTML(String(cfg.parallel_streams || 1))}</dd>
      <dt>Approx. files</dt><dd>${totalFiles ? totalFiles.toLocaleString() : '—'}</dd>
    `;
    const flows = [];
    if (cfg.normal_enabled)   flows.push('Normal');
    if (cfg.large_enabled)    flows.push('Large');
    if (cfg.download_enabled) flows.push('Download');
    foot.innerHTML = flows.length
      ? `<span class="cfg-summary-flows">${flows.map((f) => `<span class="cfg-summary-chip">${f}</span>`).join('')}</span>`
      : '<span class="cfg-summary-empty">No flow enabled — toggle one above.</span>';
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
