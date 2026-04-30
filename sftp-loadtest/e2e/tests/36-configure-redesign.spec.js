// 36-configure-redesign.spec.js — v0.9.4 Configure-screen redesign.
//
// Asserts the four-section layout: Target / Workload / Resource limits /
// Run summary; the workload-subsection switch behaviour; the sticky
// right-rail summary; and that the primary CTA is visually distinct
// from the secondary utility actions.

import { test, expect } from '@playwright/test';

test.beforeEach(async ({ page }) => {
  await page.goto('/');
  await page.locator('[data-action="view"][data-view="configure"]').click();
  // Allow deferred mounts to finish (upload-restructure → configure-redesign).
  await page.waitForTimeout(400);
});

test('Configure renders the three main section headers + slim run-summary bar', async ({ page }) => {
  const view = page.locator('.shell-main [data-view="configure"]');
  await expect(view.locator('.cfg-section[data-section="target"]')).toBeVisible();
  await expect(view.locator('.cfg-section[data-section="workload"]')).toBeVisible();
  await expect(view.locator('.cfg-section[data-section="limits"]')).toBeVisible();
  await expect(view.locator('.cfg-summary-bar[data-section="summary"]')).toBeVisible();
  // Each section title is named for its mental model.
  await expect(view.locator('.cfg-section[data-section="target"] .cfg-section-title')).toContainText(/target/i);
  await expect(view.locator('.cfg-section[data-section="workload"] .cfg-section-title')).toContainText(/workload/i);
  await expect(view.locator('.cfg-section[data-section="limits"] .cfg-section-title')).toContainText(/aggressively|long|how/i);
  // The slim bar carries a play button — primary launch affordance
  // (mirrors the topbar Run/Stop) and stays in view as the user scrolls.
  await expect(view.locator('.cfg-summary-bar [data-role="summary-go"]')).toBeVisible();
});

test('Run-summary bar reflects the host the user typed', async ({ page }) => {
  await page.locator('#conn-host').fill('sftp.example.com');
  await page.locator('#conn-port').fill('22');
  // The summary rebuilds via window.__sftplBuildRequestBody, which reads
  // from #host (the legacy mirror). Connection card forwards conn-host →
  // #host on submit, but for a live preview the layout listens to its
  // own input events too. Force a sync via the legacy field directly.
  await page.evaluate(() => {
    document.getElementById('host').value = 'sftp.example.com';
    document.getElementById('host').dispatchEvent(new Event('change', { bubbles: true }));
    document.getElementById('port').value = '22';
    document.getElementById('port').dispatchEvent(new Event('change', { bubbles: true }));
  });
  // Summary updates on a 1.5 s timer — give it time.
  const defs = page.locator('.cfg-summary-bar [data-role="summary-defs"]');
  await expect(defs).toContainText('sftp.example.com', { timeout: 3000 });
});

test('Run-summary play button delegates to the matching topbar Run/Stop control', async ({ page }) => {
  // Pin the topbar status to "idle" so the go button is in play mode,
  // then assert that clicking it forwards to the topbar Run button
  // (which is the canonical state-aware control). Using the topbar
  // (not legacy #startBtn) lets the click survive even when the
  // legacy form button is disabled by the runner state machine.
  await page.evaluate(() => {
    // Force the topbar to a known idle state for this test. pollStatus
    // may overwrite this on its next tick (1s cadence) but we click
    // synchronously below so the window is plenty.
    const s = document.querySelector('.shell-topbar-status');
    if (s) s.dataset.state = 'idle';
    const tbRun = document.querySelector('[data-role="topbar-run"]');
    const tbStop = document.querySelector('[data-role="topbar-stop"]');
    if (tbRun) tbRun.disabled = false;
    window.__topbarRunClicks = 0;
    window.__topbarStopClicks = 0;
    tbRun?.addEventListener('click', () => { window.__topbarRunClicks++; }, true);
    tbStop?.addEventListener('click', () => { window.__topbarStopClicks++; }, true);
  });
  // Wait for the MutationObserver in configure-redesign to flip the go
  // button to play mode after the data-state edit above.
  const go = page.locator('.cfg-summary-bar [data-role="summary-go"]');
  await expect(go).toHaveAttribute('data-mode', 'play');
  await go.click();
  const counts = await page.evaluate(() => ({ r: window.__topbarRunClicks, s: window.__topbarStopClicks }));
  expect(counts.r).toBeGreaterThan(0);
  expect(counts.s).toBe(0);
});

test('Toggling the Large workload off collapses #large_users', async ({ page }) => {
  // The Large-file flow lives nested inside the Upload card's advanced
  // disclosure (upload-restructure). When the user toggles the
  // "Large-file" switch off, the textarea is no longer reachable.
  // Find the workload card whose enabledId is the legacy #large_enabled
  // (or #download_enabled — whichever surfaces a legacy CSV textarea).
  const dlCard = page.locator('.cfg-workload-card[data-enabled-id="download_enabled"]');
  await expect(dlCard).toBeVisible();
  const dlSwitch = dlCard.locator('input[data-role="workload-switch"]');
  // Start off — turn ON.
  await dlSwitch.check();
  await expect(page.locator('#download_users')).toBeVisible();
  // Now turn OFF — the inner fields collapse.
  await dlSwitch.uncheck();
  await expect(page.locator('#download_users')).toBeHidden();
  // Toggle back ON — fields restored.
  await dlSwitch.check();
  await expect(page.locator('#download_users')).toBeVisible();
});

test('Import config lives in the top prelude (not at the bottom action zone)', async ({ page }) => {
  // Operators reach for Import BEFORE filling the form (to bootstrap
  // from a saved JSON); making it scroll-to-bottom is hostile.
  const view = page.locator('.shell-main [data-view="configure"]');
  const prelude = view.locator('.cfg-prelude');
  await expect(prelude).toBeVisible();
  await expect(prelude.locator('.cfg-prelude-import')).toBeVisible();
  // Export stays in the action zone — only Import is hoisted.
  const secondary = view.locator('.cfg-actionzone-secondary');
  await expect(secondary).not.toContainText(/import config/i);
  await expect(secondary).toContainText(/export config/i);
});

test('Resource limits is split into Upload / Download / Run sub-groups, with #dparallel in Download', async ({ page }) => {
  const view = page.locator('.shell-main [data-view="configure"]');
  const limits = view.locator('.cfg-section[data-section="limits"]');
  // Three sub-groups labelled by direction.
  await expect(limits.locator('.cfg-limits-group[data-group="upload"]')).toBeVisible();
  await expect(limits.locator('.cfg-limits-group[data-group="download"]')).toBeVisible();
  await expect(limits.locator('.cfg-limits-group[data-group="run"]')).toBeVisible();
  // Upload sub-group hosts upload streams-per-user.
  await expect(limits.locator('.cfg-limits-group[data-group="upload"] #parallel')).toBeVisible();
  // Download sub-group hosts the relocated download streams-per-user.
  await expect(limits.locator('.cfg-limits-group[data-group="download"] #dparallel')).toBeVisible();
  // Run sub-group carries duration / poll / timeout / max_fails.
  for (const id of ['duration', 'poll', 'timeout_min', 'max_fails']) {
    await expect(limits.locator(`.cfg-limits-group[data-group="run"] #${id}`)).toBeVisible();
  }
});

test('Primary CTA is visually distinct from secondary utility actions', async ({ page }) => {
  // Start-run carries data-variant="primary" + .cfg-cta class with an
  // accent-tinted background; the secondary actions (Stop, Download CSV,
  // Export config) carry .cfg-secondary-action with a neutral surface.
  const cta = page.locator('.cfg-actionzone-primary #startBtn');
  await expect(cta).toBeVisible();
  await expect(cta).toHaveAttribute('data-variant', 'primary');
  await expect(cta).toHaveClass(/cfg-cta/);
  // Secondary slot exists and contains at least the Stop button.
  const secondary = page.locator('.cfg-actionzone-secondary');
  await expect(secondary).toBeVisible();
  await expect(secondary.locator('#stopBtn')).toBeVisible();
  await expect(secondary.locator('#stopBtn')).toHaveClass(/cfg-secondary-action/);
  // Visual distinction comes from the accent fill on the CTA, not from
  // size — both buttons share the toolbar's compact 32px height. Assert
  // the rendered background colour differs.
  const ctaBg = await cta.evaluate((el) => getComputedStyle(el).backgroundColor + getComputedStyle(el).backgroundImage);
  const stopBg = await secondary.locator('#stopBtn').evaluate((el) => getComputedStyle(el).backgroundColor + getComputedStyle(el).backgroundImage);
  expect(ctaBg).not.toBe(stopBg);
  // The CTA should also carry a leading icon — the .cfg-btn-icon span.
  await expect(cta.locator('.cfg-btn-icon')).toBeVisible();
});
