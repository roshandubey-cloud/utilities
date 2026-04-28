// run-actions.js — adds Export / Import config controls to the bottom of the
// Workload step's actions row (right next to Start run / Stop / Download CSV).
// Operators want export available AFTER they've filled the form, not before —
// so the buttons live at the form's tail, not in the hero.
//
// Both buttons delegate to the existing legacy click handlers (#exportBtn,
// #importBtn) so the password-stripping serialiser and import-replay logic
// keep applying without duplication.

export function mountRunActions() {
  // Find the actions row that contains the Start-run button.
  const startBtn = document.getElementById('startBtn');
  const actionsRow = startBtn?.closest('.actions');
  if (!actionsRow || actionsRow.dataset.runActionsMounted) return;
  actionsRow.dataset.runActionsMounted = '1';

  const exportBtn = document.getElementById('exportBtn');
  const importBtn = document.getElementById('importBtn');
  if (!exportBtn) return;

  // Build proxy buttons styled like the new system; click delegates to the
  // legacy buttons that own the actual logic.
  const proxyExport = document.createElement('button');
  proxyExport.type = 'button';
  proxyExport.className = 'btn btn-secondary';
  proxyExport.textContent = 'Export config';
  proxyExport.title = 'Save the current configuration as JSON';
  proxyExport.addEventListener('click', (ev) => { ev.preventDefault(); exportBtn.click(); });

  const proxyImport = document.createElement('button');
  proxyImport.type = 'button';
  proxyImport.className = 'btn btn-ghost';
  proxyImport.textContent = 'Import config';
  proxyImport.title = 'Load a previously-exported configuration';
  if (importBtn) {
    proxyImport.addEventListener('click', (ev) => { ev.preventDefault(); importBtn.click(); });
  }

  // Insert at the END of the row (after Download CSV) — operators reach for
  // export only after everything else is set up.
  actionsRow.appendChild(proxyExport);
  if (importBtn) actionsRow.appendChild(proxyImport);
}
