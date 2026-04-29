// Form validation — confirm the Quick Checks inline-error path fires
// for invalid host/port and that legacy validation messages are
// surfaced (not swallowed silently).

import { test, expect } from '@playwright/test';

test('quick checks blocks Test Connection on empty host with inline error', async ({ page }) => {
  await page.goto('/');
  await page.locator('#conn-host').fill('');
  await page.locator('#conn-port').fill('22020');
  const btn = page.getByRole('button', { name: /test connection/i });
  await btn.click();
  // The connection.js validateField path adds .field-error inside the
  // Host field group when empty. Element must appear and be readable.
  const fieldError = page.locator('.field[data-invalid="true"] .field-error');
  await expect(fieldError).toBeVisible({ timeout: 3000 });
  await expect(fieldError).toContainText(/required/i);
});

test('quick checks rejects out-of-range port', async ({ page }) => {
  await page.goto('/');
  await page.locator('#conn-host').fill('127.0.0.1');
  await page.locator('#conn-port').fill('999999');
  const btn = page.getByRole('button', { name: /test connection/i });
  await btn.click();
  const fieldError = page.locator('.field[data-invalid="true"] .field-error');
  await expect(fieldError).toBeVisible({ timeout: 3000 });
  await expect(fieldError).toContainText(/(1[-–]65535|range)/i);
});
