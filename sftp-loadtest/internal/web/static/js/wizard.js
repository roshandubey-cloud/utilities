// wizard.js — lightweight 3-step stepper that filters which legacy card is
// visible: Workload → Schedule → Review. The legacy cards keep all their
// HTML, JS bindings, and validation; the stepper just toggles a CSS class
// on the .grid container that hides any .card not tagged with the active
// step.

const STEPS = [
  { id: 'workload', label: 'Workload',  hint: 'Files, sizes, users' },
  { id: 'schedule', label: 'Schedule',  hint: 'Run now or queue later' },
  { id: 'review',   label: 'Review',    hint: 'Confirm and start' },
];

// Map each legacy card id to the step it belongs to.
const CARD_TO_STEP = {
  normalCard:   'workload',
  largeCard:    'workload',
  downloadCard: 'workload',
  // The "Schedule & config" card has no id; we tag it via :nth pattern below.
};

export function mountWizard(rootSelector) {
  const root = document.querySelector(rootSelector);
  if (!root) return;
  const grid = document.querySelector('.grid');
  if (!grid) return;

  // Tag legacy cards with data-step so CSS can filter them.
  tagLegacyCards();

  let active = STEPS[0].id;

  function render() {
    root.innerHTML = `
      <nav class="wizard-steps" role="tablist" aria-label="Run configuration steps">
        ${STEPS.map((s, i) => `
          <button type="button" role="tab"
                  class="wizard-step ${active === s.id ? 'is-active' : ''}"
                  aria-selected="${active === s.id}"
                  data-step="${s.id}">
            <span class="wizard-step-num">${i + 1}</span>
            <span class="wizard-step-text">
              <span class="wizard-step-label">${s.label}</span>
              <span class="wizard-step-hint">${s.hint}</span>
            </span>
          </button>
        `).join('')}
      </nav>`;

    root.querySelectorAll('[data-step]').forEach((btn) => {
      btn.addEventListener('click', (ev) => {
        ev.preventDefault();
        active = btn.dataset.step;
        applyStep();
        render();
        renderReview();
        // Keep scroll-anchored on the stepper so the user doesn't lose place.
        root.scrollIntoView({ block: 'start', behavior: 'smooth' });
      });
    });
  }

  function applyStep() {
    grid.dataset.step = active;
  }

  function renderReview() {
    if (active !== 'review') return;
    let panel = document.getElementById('wizard-review');
    if (!panel) {
      panel = document.createElement('section');
      panel.id = 'wizard-review';
      panel.className = 'panel wizard-review';
      grid.parentNode.insertBefore(panel, grid.nextSibling);
    }
    panel.innerHTML = renderReviewMarkup();
    wireReview(panel);
  }
  function clearReview() {
    const panel = document.getElementById('wizard-review');
    if (panel) panel.remove();
  }

  function renderReviewMarkup() {
    const v = (id) => (document.getElementById(id) || {}).value || '';
    const c = (id) => (document.getElementById(id) || {}).checked || false;
    const csvCount = (id) => {
      const ta = document.getElementById(id);
      const raw = ta ? (ta.dataset.raw || ta.value || '') : '';
      return raw.split(/\r?\n/).filter((l) => l.trim() && l.split(',').length >= 2).length;
    };

    const target = `${v('host') || '—'}:${v('port') || '—'} → ${v('folder') || '—'}`;
    const normalLine = c('normal_enabled')
      ? `${csvCount('normal_users')} users · ${v('fpm') || '?'} fpm · ${v('nmin') || '?'}–${v('nmax') || '?'} MB ${v('ncontent') || ''}`.trim()
      : 'disabled';
    const largeLine = c('large_enabled')
      ? `${csvCount('large_users')} users · every ${v('interval') || '?'} min · ${v('lmin') || '?'}–${v('lmax') || '?'} ${v('lunit') || ''}`.trim()
      : 'disabled';
    const dlLine = c('download_enabled')
      ? `${csvCount('download_users')} users · folder ${v('dfolder') || '—'} · ${v('dparallel') || '?'} streams`
      : 'disabled';
    const sched = (v('sched_at') || '').trim();

    return `
      <div class="panel-header">
        <div class="panel-title-group">
          <div class="panel-title">Review</div>
          <div class="panel-subtitle">Confirm settings, then start the run.</div>
        </div>
        <div class="panel-actions">
          <button class="btn btn-ghost"   type="button" data-action="back">Back</button>
          <button class="btn btn-primary btn-lg" type="button" data-action="start">${sched ? 'Schedule run' : 'Start run'}</button>
        </div>
      </div>
      <div class="panel-body">
        <dl class="wizard-review-grid">
          <dt>Target</dt>           <dd class="mono">${escapeHTML(target)}</dd>
          <dt>Streams · duration</dt><dd>${escapeHTML(v('parallel') || '?')} streams · ${escapeHTML(v('duration') || '?')} h</dd>
          <dt>Normal files</dt>     <dd>${escapeHTML(normalLine)}</dd>
          <dt>Large files</dt>      <dd>${escapeHTML(largeLine)}</dd>
          <dt>Download phase</dt>   <dd>${escapeHTML(dlLine)}</dd>
          <dt>Schedule</dt>         <dd>${sched ? `<span class="mono">${escapeHTML(sched)}</span>` : 'Run immediately'}</dd>
        </dl>
      </div>`;
  }

  function wireReview(panel) {
    panel.querySelector('[data-action="back"]')?.addEventListener('click', (ev) => {
      ev.preventDefault();
      active = 'schedule';
      applyStep(); render(); clearReview();
    });
    panel.querySelector('[data-action="start"]')?.addEventListener('click', (ev) => {
      ev.preventDefault();
      // Defer to legacy handlers — they're wired to the Start Run / Schedule
      // buttons in the legacy actions area. We click them programmatically.
      const sched = (document.getElementById('sched_at') || {}).value || '';
      const legacyBtn = document.getElementById(sched ? 'schedBtn' : 'startBtn');
      if (legacyBtn) legacyBtn.click();
    });
  }

  // Initial mount.
  applyStep();
  render();
  renderReview();
}

function tagLegacyCards() {
  // Ids first.
  Object.entries(CARD_TO_STEP).forEach(([id, step]) => {
    const el = document.getElementById(id);
    if (el) el.dataset.step = step;
  });
  // The "Schedule & config" card lacks an id but lives in the LEFT column;
  // tag it by detecting the toggle-label text "Schedule & config".
  document.querySelectorAll('.grid .card > header .toggle-label').forEach((lbl) => {
    const txt = (lbl.textContent || '').trim().toLowerCase();
    const card = lbl.closest('.card');
    if (!card || card.dataset.step) return;
    if (txt.startsWith('schedule')) card.dataset.step = 'schedule';
  });
  // Anything else in the right column (Live metrics, Previous runs,
  // Slowdown events) is "review" — visible only when the user reaches the
  // review step (these are output, not input).
  document.querySelectorAll('.grid .card:not([data-step])').forEach((card) => {
    card.dataset.step = 'review';
  });
}

function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}
