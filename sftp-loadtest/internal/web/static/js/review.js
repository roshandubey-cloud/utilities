// review.js — "About to run" plan section, top of the merged Runs view.
//
// Renders a summary of the configured run so the operator can confirm
// before clicking Start. Pulls values from the legacy form via
// window.__sftplBuildRequestBody (the same hook saved-configs uses).
// Refreshed every 1.5 s so changes in Configure / Schedule reflect
// immediately when the operator switches views.
//
// Mounts in [data-role="runs-plan"], which the shell places ABOVE the
// past-runs history panel inside the Runs view.

const REFRESH_MS = 1500;

export function mountReview() {
  const view = document.querySelector('.shell-main [data-view="runs"] [data-role="runs-plan"]');
  if (!view || view.dataset.reviewMounted) return;
  view.dataset.reviewMounted = '1';

  function render() {
    const cfg = pullConfig();
    const sched = (document.getElementById('sched_at') || {}).value || '';
    const scheduleNote = (document.getElementById('sched_note') || {}).value || '';

    const u = csvCount(cfg.normal_users_csv);
    const l = csvCount(cfg.large_users_csv);
    const d = csvCount(cfg.download_users_csv);

    view.innerHTML = `
      <section class="review-view-panel">
        <header class="review-view-head">
          <div>
            <div class="review-view-title">About to run</div>
            <div class="review-view-sub">Confirm the configuration; the topbar Run button starts the test. Past runs are listed below.</div>
          </div>
          <div class="review-view-actions">
            ${sched
              ? `<button type="button" class="btn btn-primary" id="reviewScheduleBtn">Schedule for ${escapeHTML(formatLocal(sched))}</button>`
              : `<button type="button" class="btn btn-primary" id="reviewStartBtn">Start run now</button>`}
          </div>
        </header>

        <div class="review-view-grid">
          <div class="review-view-card">
            <div class="review-view-card-title">Target</div>
            <dl class="review-view-defs">
              <dt>Host</dt><dd class="mono">${escapeHTML(cfg.host || '—')}:${escapeHTML(String(cfg.port || ''))}</dd>
              <dt>Upload folder</dt><dd class="mono">${escapeHTML(cfg.upload_folder || '—')}</dd>
              <dt>Parallel streams</dt><dd>${escapeHTML(String(cfg.parallel_streams || 1))} per user</dd>
              <dt>Duration</dt><dd>${escapeHTML(String(cfg.duration_hours || '?'))} hours</dd>
              <dt>Poll interval</dt><dd>${escapeHTML(String(cfg.poll_seconds || 3))} s</dd>
              <dt>Track-id timeout</dt><dd>${formatSeconds(cfg.track_id_timeout_seconds || 0)}</dd>
              <dt>Disable user after</dt><dd>${escapeHTML(String(cfg.max_consecutive_failures || 0))} consecutive failures</dd>
            </dl>
          </div>

          <div class="review-view-card">
            <div class="review-view-card-title">Upload</div>
            ${cfg.normal_enabled ? `
              <dl class="review-view-defs">
                <dt>Users</dt><dd>${u}</dd>
                <dt>Files / minute</dt><dd>${escapeHTML(String(cfg.files_per_minute || 0))}</dd>
                <dt>File size</dt><dd>${escapeHTML(String(cfg.normal_min_mb || 0))}–${escapeHTML(String(cfg.normal_max_mb || 0))} MB</dd>
                <dt>Content</dt><dd>${escapeHTML(cfg.normal_content_type || 'binary')}</dd>
              </dl>` : '<div class="review-view-empty">disabled</div>'}
          </div>

          <div class="review-view-card">
            <div class="review-view-card-title">Large file</div>
            ${cfg.large_enabled ? `
              <dl class="review-view-defs">
                <dt>Users</dt><dd>${l}</dd>
                <dt>Cadence</dt><dd>every ${escapeHTML(String(cfg.interval_minutes || 0))} min</dd>
                <dt>File size</dt><dd>${escapeHTML(String(cfg.large_min || 0))}–${escapeHTML(String(cfg.large_max || 0))} ${escapeHTML(cfg.large_unit || 'MB')}</dd>
              </dl>` : '<div class="review-view-empty">disabled</div>'}
          </div>

          <div class="review-view-card">
            <div class="review-view-card-title">Download (round-trip)</div>
            ${cfg.download_enabled ? `
              <dl class="review-view-defs">
                <dt>Users</dt><dd>${d}</dd>
                <dt>Folder</dt><dd class="mono">${escapeHTML(cfg.download_folder || '—')}</dd>
                <dt>Streams / user</dt><dd>${escapeHTML(String(cfg.download_parallel_streams || 1))}</dd>
                <dt>Match mode</dt><dd>${cfg.download_match_mode === 'filename' ? 'filename pattern' : 'track-id suffix'}</dd>
              </dl>` : '<div class="review-view-empty">disabled</div>'}
          </div>

          <div class="review-view-card">
            <div class="review-view-card-title">Schedule</div>
            ${sched
              ? `<dl class="review-view-defs">
                  <dt>Fires at</dt><dd class="mono">${escapeHTML(formatLocal(sched))}</dd>
                  ${scheduleNote ? `<dt>Note</dt><dd>${escapeHTML(scheduleNote)}</dd>` : ''}
                </dl>`
              : '<div class="review-view-empty">Run immediately on Start.</div>'}
          </div>
        </div>

      </section>`;

    view.querySelector('#reviewStartBtn')?.addEventListener('click', () => {
      document.getElementById('startBtn')?.click();
    });
    view.querySelector('#reviewScheduleBtn')?.addEventListener('click', () => {
      document.getElementById('scheduleBtn')?.click();
    });
  }
  render();
  setInterval(render, REFRESH_MS);
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

function formatSeconds(sec) {
  if (!sec) return '—';
  const m = Math.floor(sec / 60);
  if (m < 60) return `${m} min`;
  return `${Math.floor(m / 60)} h ${m % 60} m`;
}

function formatLocal(iso) {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString();
}

function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}
