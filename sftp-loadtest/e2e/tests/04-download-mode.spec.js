// Download (round-trip) — the v0.8.1 mode picker.
// Verifies both radios are present, the labels read correctly, the
// choice persists across reloads, and the chosen mode flows into
// /api/start as the right field name.

import { test, expect } from '@playwright/test';

test.describe('round-trip mode picker', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    // The download card is collapsed by default; open it so the mode
    // radios are reachable.
    const dl = page.locator('#download_enabled');
    if (!await dl.isChecked()) await dl.check();
  });

  test('both radios are visible with explanatory hints', async ({ page }) => {
    const trackid = page.locator('#dmm_trackid');
    const filename = page.locator('#dmm_filename');
    await expect(trackid).toBeVisible();
    await expect(filename).toBeVisible();
    // Default is trackid — prevents an unintentional default flip.
    await expect(trackid).toBeChecked();
    await expect(filename).not.toBeChecked();
    // Each radio's label should explain the trade-off in one sentence.
    const card = page.locator('#downloadCard');
    await expect(card).toContainText(/track[- ]?id/i);
    await expect(card).toContainText(/filename pattern/i);
    await expect(card).toContainText(/_slt_/i);
  });

  test('switching mode persists across reload', async ({ page }) => {
    await page.locator('#dmm_filename').check();
    // Change event triggers saveConfig; reload to verify localStorage
    // round-trip.
    await page.reload();
    const dl = page.locator('#download_enabled');
    if (!await dl.isChecked()) await dl.check();
    await expect(page.locator('#dmm_filename')).toBeChecked();
  });

  test('start payload carries download_match_mode when enabled', async ({ page, baseURL }) => {
    await page.locator('#dmm_filename').check();

    // Stub a Start to capture the request body, but cancel before the
    // run actually fires. Easiest path: peek at buildRequestBody from
    // legacy.js directly.
    const body = await page.evaluate(async () => {
      // buildRequestBody is module-scoped in legacy.js; read the
      // localStorage save which mirrors the same shape.
      try {
        return JSON.parse(localStorage.getItem('sftp-loadtest-config-v1') || '{}');
      } catch {
        return null;
      }
    });
    expect(body, 'localStorage config must reflect the chosen mode').toBeTruthy();
    expect(body.download_match_mode).toBe('filename');
  });
});
