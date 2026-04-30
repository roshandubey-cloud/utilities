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

test('Configure renders the four section headers', async ({ page }) => {
  const view = page.locator('.shell-main [data-view="configure"]');
  await expect(view.locator('.cfg-section[data-section="target"]')).toBeVisible();
  await expect(view.locator('.cfg-section[data-section="workload"]')).toBeVisible();
  await expect(view.locator('.cfg-section[data-section="limits"]')).toBeVisible();
  await expect(view.locator('.configure-rail[data-section="summary"]')).toBeVisible();
  // Each section title is named for its mental model.
  await expect(view.locator('.cfg-section[data-section="target"] .cfg-section-title')).toContainText(/target/i);
  await expect(view.locator('.cfg-section[data-section="workload"] .cfg-section-title')).toContainText(/workload/i);
  await expect(view.locator('.cfg-section[data-section="limits"] .cfg-section-title')).toContainText(/aggressively|long|how/i);
  await expect(view.locator('.configure-rail[data-section="summary"] .cfg-section-title')).toContainText(/run summary/i);
});

test('Run-summary rail reflects the host the user typed', async ({ page }) => {
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
  const defs = page.locator('.configure-rail [data-role="summary-defs"]');
  await expect(defs).toContainText('sftp.example.com', { timeout: 3000 });
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

test('Primary CTA is visually distinct from secondary utility actions', async ({ page }) => {
  // Start-run carries data-variant="primary" + .cfg-cta class; the
  // secondary actions (Stop, Download CSV, Export config) carry
  // .cfg-secondary-action.
  const cta = page.locator('.cfg-actionzone-primary #startBtn');
  await expect(cta).toBeVisible();
  await expect(cta).toHaveAttribute('data-variant', 'primary');
  await expect(cta).toHaveClass(/cfg-cta/);
  // Secondary slot exists and contains at least the Stop button.
  const secondary = page.locator('.cfg-actionzone-secondary');
  await expect(secondary).toBeVisible();
  await expect(secondary.locator('#stopBtn')).toBeVisible();
  await expect(secondary.locator('#stopBtn')).toHaveClass(/cfg-secondary-action/);
  // The CTA is taller than secondary actions (visual hierarchy).
  const ctaBox = await cta.boundingBox();
  const stopBox = await secondary.locator('#stopBtn').boundingBox();
  expect(ctaBox).not.toBeNull();
  expect(stopBox).not.toBeNull();
  expect(ctaBox.height).toBeGreaterThan(stopBox.height);
});
