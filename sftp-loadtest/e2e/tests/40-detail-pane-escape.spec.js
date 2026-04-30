// 40-detail-pane-escape.spec.js — sidebar nav must always work, even
// after a sub-pane (like run-detail) hid .shell-main. Regression: a
// previously-opened detail pane left main display:none, so any
// subsequent click on Configure / Workbench / Schedule / etc. silently
// no-op'd because the view container toggled inside an invisible
// parent. setView now force-restores main + dismisses the detail pane.

import { test, expect } from '@playwright/test';

async function runOnce(page) {
  await page.locator('[data-action="view"][data-view="configure"]').click();
  await page.evaluate(() => {
    const set = (id, val) => {
      const el = document.getElementById(id);
      if (!el) return;
      el.value = String(val);
      el.dispatchEvent(new Event('input', { bubbles: true }));
      el.dispatchEvent(new Event('change', { bubbles: true }));
    };
    set('host', '127.0.0.1');
    set('port', '22020');
    set('folder', 'inbox');
    set('fpm', '60');
    set('nmin', '1');
    set('nmax', '1');
    set('duration', '0.005');
    set('poll', '1');
    set('timeout_min', '1');
  });
  const ta = page.locator('#normal_users');
  await ta.click();
  await ta.fill('u1,p1,probe*');
  await ta.blur();
  await page.locator('#startBtn').click();
  // Wait for at least one record then a subsequent inactive state.
  await page.waitForFunction(async () => {
    const r = await fetch('/api/status', { headers: { 'X-Requested-With': 'sftp-loadtest' }});
    if (!r.ok) return false;
    const j = await r.json();
    return j.run_id && j.active === false;
  }, null, { timeout: 30_000, polling: 500 });
}

test('sidebar Configure click escapes a run-detail pane and shows the form', async ({ page }) => {
  test.setTimeout(75_000);
  await page.goto('/');
  await runOnce(page);
  // Open the Recent Run via the sidebar's run row, which fires the
  // synthesised proxy click that run-detail.js intercepts.
  await page.locator('[data-action="view"][data-view="runs"]').click();
  await page.locator('[data-component="runs-history"] .runs-history-card [data-view-detail]').first().click();
  // .shell-main is hidden now; the detail pane is visible.
  const detail = page.locator('.run-detail-view');
  await expect(detail).toBeVisible();
  // Click Configure — must restore main, dismiss detail, and show the
  // Configure section with its target heading.
  await page.locator('[data-action="view"][data-view="configure"]').click();
  await expect(page.locator('.shell-main [data-view="configure"][data-view-active="true"]')).toBeVisible();
  await expect(page.locator('.cfg-section[data-section="target"] .cfg-section-title')).toBeVisible();
  await expect(detail).toBeHidden();
});
