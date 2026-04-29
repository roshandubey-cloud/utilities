// Recent connections — Quick Checks remembers up to 8 host:port
// pairs. Confirms the chip-list renders, click-to-fill works, and a
// successful probe writes the connection back to the list.

import { test, expect } from '@playwright/test';

test('successful probe records host:port to recent', async ({ page }) => {
  await page.goto('/');
  await page.locator('#conn-host').fill('127.0.0.1');
  await page.locator('#conn-port').fill('22020');
  await page.locator('#conn-user').fill('u1');
  await page.locator('#conn-pass').fill('p');
  await page.getByRole('button', { name: /test connection/i }).click();
  // Wait for the OK state.
  await expect(page.locator('[data-role="result"]')).toContainText(/(connection ok|complete)/i, { timeout: 8000 });
  // The recent-connections list now contains 127.0.0.1:22020.
  const recent = page.locator('[data-role="recent"]');
  await expect(recent).toContainText('127.0.0.1:22020');
});

test('clicking a recent chip refills host/port', async ({ page }) => {
  // Seed localStorage so a chip exists.
  await page.goto('/');
  await page.evaluate(() => {
    try {
      localStorage.setItem(
        'sftp-loadtest-conn-history-v1',
        JSON.stringify([{ host: 'remembered.example', port: 2200 }])
      );
    } catch {}
  });
  await page.reload();
  const chip = page.locator('[data-role="recent"] .btn').filter({ hasText: 'remembered.example:2200' });
  await expect(chip).toBeVisible();
  await chip.click();
  await expect(page.locator('#conn-host')).toHaveValue('remembered.example');
  await expect(page.locator('#conn-port')).toHaveValue('2200');
});
