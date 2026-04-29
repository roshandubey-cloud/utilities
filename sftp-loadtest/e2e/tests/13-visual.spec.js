// Visual capture — full-page screenshots of key states. Saves to the
// playwright-report/ tree and acts as a first-pass visual regression.
// Not byte-comparison — the UI uses system fonts whose rendering varies
// slightly across machines — but a quick visual scan after a UI tweak.

import { test } from '@playwright/test';

test('landing — light theme', async ({ page }) => {
  await page.goto('/');
  await page.getByRole('button', { name: 'Light' }).click();
  await page.waitForTimeout(200);
  await page.screenshot({ path: 'playwright-report/landing-light.png', fullPage: true });
});

test('landing — dark theme', async ({ page }) => {
  await page.goto('/');
  await page.getByRole('button', { name: 'Dark' }).click();
  await page.waitForTimeout(200);
  await page.screenshot({ path: 'playwright-report/landing-dark.png', fullPage: true });
});

test('download card with filename mode selected', async ({ page }) => {
  await page.goto('/');
  const dl = page.locator('#download_enabled');
  if (!await dl.isChecked()) await dl.check();
  await page.locator('#dmm_filename').check();
  await page.locator('#downloadCard').scrollIntoViewIfNeeded();
  await page.waitForTimeout(200);
  await page.locator('#downloadCard').screenshot({ path: 'playwright-report/download-filename-mode.png' });
});

test('quick checks panel — empty state', async ({ page }) => {
  await page.goto('/');
  await page.locator('[data-component="connection"]').screenshot({ path: 'playwright-report/quick-checks-empty.png' });
});

test('runs-history empty state', async ({ page }) => {
  await page.goto('/');
  await page.locator('[data-component="runs-history"]').screenshot({ path: 'playwright-report/history-empty.png' });
});
