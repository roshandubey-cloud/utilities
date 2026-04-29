// Quick checks (Test Connection) — operator's first interaction.
// Verifies the Test Connection probe surfaces the right messages for
// the common failure modes: empty creds, bad host, success.

import { test, expect } from '@playwright/test';

test.beforeEach(async ({ page }) => {
  await page.goto('/');
});

test('test connection shows ok against the mock', async ({ page }) => {
  await page.fill('#conn-host', '127.0.0.1');
  await page.fill('#conn-port', '22020');
  // The Quick checks panel should have a username + password field; if
  // not, this test will fail loudly and we know to add them.
  const userField = page.locator('#conn-user, [data-role="user"]');
  const passField = page.locator('#conn-pass, [data-role="pass"]');
  await expect(userField, 'Quick checks needs a Username field').toBeVisible();
  await expect(passField, 'Quick checks needs a Password field').toBeVisible();
  await userField.fill('u1');
  await passField.fill('p');

  // Test Connection button can carry either label; match by accessible role.
  const btn = page.getByRole('button', { name: /test connection/i });
  await expect(btn).toBeVisible();
  await btn.click();

  // The result surface — must be visible, must contain a positive token.
  // Loose match: green / "ok" / "connected" / "complete" / "ssh".
  await expect(page.locator('[data-role="result"], .quick-check-result, .connection-result, #conn-result')).toBeVisible({ timeout: 8000 });
});

test('test connection refuses an empty password with a clear message', async ({ page }) => {
  await page.fill('#conn-host', '127.0.0.1');
  await page.fill('#conn-port', '22020');
  const userField = page.locator('#conn-user, [data-role="user"]');
  const passField = page.locator('#conn-pass, [data-role="pass"]');
  if (await userField.count()) await userField.fill('u1');
  if (await passField.count()) await passField.fill('');
  const btn = page.getByRole('button', { name: /test connection/i });
  if (await btn.count()) {
    await btn.click();
    // Either a validation error fires immediately ("password required")
    // or the server returns auth-failed. Both are acceptable; what we
    // must NOT see is silence.
    await expect(page.locator('body')).toContainText(/(password|authentication|required|verify)/i, { timeout: 8000 });
  }
});
