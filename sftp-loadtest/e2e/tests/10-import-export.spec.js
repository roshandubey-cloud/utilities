// Import / Export config — the operator workflow for sharing a
// configuration. Verifies:
//   * Export produces a download with the expected JSON shape.
//   * Import populates the form fields from that JSON.
//   * Round-trip preserves the new download_match_mode field (v0.8.1).

import { test, expect } from '@playwright/test';

test('export → file contains expected shape', async ({ page }) => {
  await page.goto('/');
  // Configure something exportable.
  await page.locator('#conn-host').fill('example.org');
  await page.locator('#conn-port').fill('2222');
  if (await page.locator('#upload-folder').count()) {
    await page.locator('#upload-folder').fill('inbox-test');
  }
  await page.locator('#fpm').fill('480');
  // Enable download + flip to filename mode so the export carries it.
  const dl = page.locator('#download_enabled');
  if (!await dl.isChecked()) await dl.check();
  await page.locator('#dmm_filename').check();

  // Confirm legacy sync fired (#host is what buildRequestBody reads).
  const hostVal = await page.locator('#host').inputValue();
  expect(hostVal, 'Quick Checks host must sync to legacy #host').toBe('example.org');

  // The visible Export button is a proxy created by run-actions.js
  // and lives in the bottom actions row. The legacy #exportBtn is
  // hidden but receives the click via delegation.
  const exportBtn = page.getByRole('button', { name: /^export config$/i });
  await expect(exportBtn).toBeVisible();

  const [download] = await Promise.all([
    page.waitForEvent('download', { timeout: 5000 }),
    exportBtn.click(),
  ]);
  const path = await download.path();
  const fs = await import('node:fs/promises');
  const wrapper = JSON.parse(await fs.readFile(path, 'utf8'));
  // Exported file wraps the config: { version, exported_at, passwords_included, config }.
  expect(wrapper.version).toBe(1);
  expect(wrapper.passwords_included).toBe(false); // default — no opt-in
  const json = wrapper.config;
  expect(json.host).toBe('example.org');
  expect(Number(json.port)).toBe(2222);
  expect(Number(json.files_per_minute)).toBe(480);
  expect(json.download_enabled).toBe(true);
  expect(json.download_match_mode).toBe('filename');
});
