// 46-ftps-trust.spec.js — surface the FTPS leaf-cert fingerprint in the
// probe response and on the OK card. The cert TOFU consent flow uses the
// existing hostKeyConsent modal pattern; this spec only validates the
// happy path (insecure-skip-verify enabled + fingerprint surfaced).

import { test, expect, request as playwrightRequest } from '@playwright/test';

async function stopAnyActiveRun(baseURL) {
  const ctx = await playwrightRequest.newContext({
    baseURL,
    extraHTTPHeaders: { 'X-Requested-With': 'sftp-loadtest' },
  });
  try { await ctx.post('/api/stop', { data: {} }); } catch {}
  await ctx.dispose();
}

test.beforeEach(async ({ baseURL }) => {
  await stopAnyActiveRun(baseURL);
});

test('FTPS implicit probe via UI surfaces the captured fingerprint chip', async ({ page }) => {
  await page.goto('/');
  await page.evaluate(() => { try { localStorage.clear(); } catch {} });
  await page.reload();

  // Switch picker → FTPS, mode → implicit, port → 22022 (mock).
  await page.locator('[data-role="protocol-picker"] button[data-value="ftps"]').click();
  await page.locator('[data-role="tls-mode-picker"] button[data-value="implicit"]').click();
  await page.locator('#conn-host').fill('127.0.0.1');
  await page.locator('#conn-port').fill('22022');
  await page.locator('#tls_skip_verify').check({ force: true });
  await page.locator('#conn-user').fill('u1');
  await page.locator('#conn-pass').fill('p');

  await page.locator('[data-role="submit"]').click();

  // The OK card carries the fingerprint chip.
  await expect(page.locator('[data-role="result"]')).toHaveAttribute('data-state', 'ok', { timeout: 10_000 });
  await expect(page.locator('[data-role="captured-fingerprint"]')).toContainText(/SHA256:/);
});

test('FTPS explicit probe succeeds via UI and shows OK', async ({ page }) => {
  await page.goto('/');
  await page.evaluate(() => { try { localStorage.clear(); } catch {} });
  await page.reload();

  await page.locator('[data-role="protocol-picker"] button[data-value="ftps"]').click();
  // Explicit is default; reassert anyway.
  await page.locator('[data-role="tls-mode-picker"] button[data-value="explicit"]').click();
  await page.locator('#conn-host').fill('127.0.0.1');
  await page.locator('#conn-port').fill('22021');
  await page.locator('#tls_skip_verify').check({ force: true });
  await page.locator('#conn-user').fill('u1');
  await page.locator('#conn-pass').fill('p');

  await page.locator('[data-role="submit"]').click();
  await expect(page.locator('[data-role="result"]')).toHaveAttribute('data-state', 'ok', { timeout: 10_000 });
});
