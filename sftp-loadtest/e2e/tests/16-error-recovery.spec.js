// Error recovery — the operator must be able to recover from a bad
// state without reloading the page. Validates that:
//   * A failed probe doesn't leave the form locked.
//   * After a Bad Configure → Start cycle, fixing the config and
//     re-clicking Start works.
//   * The toast/error surface clears between attempts (no stale message).

import { test, expect, request as playwrightRequest } from '@playwright/test';

async function stopAnyActiveRun(baseURL) {
  const ctx = await playwrightRequest.newContext({ baseURL, extraHTTPHeaders: { 'X-Requested-With': 'sftp-loadtest' } });
  try { await ctx.post('/api/stop', { data: {} }); } catch {}
  for (let i = 0; i < 40; i++) {
    const r = await ctx.get('/api/status');
    if (r.ok()) { const j = await r.json(); if (!j.active) break; }
    await new Promise((r) => setTimeout(r, 200));
  }
  await ctx.dispose();
}
test.beforeEach(async ({ baseURL }) => { await stopAnyActiveRun(baseURL); });

test('failed probe does not lock the test-connection button', async ({ page }) => {
  await page.goto('/');
  // Point at a port that nothing is listening on.
  await page.locator('#conn-host').fill('127.0.0.1');
  await page.locator('#conn-port').fill('1');           // close-to-guaranteed connection refused
  const btn = page.getByRole('button', { name: /test connection/i }).first();
  await btn.click();
  // Wait for the error surface.
  await expect(page.locator('[data-role="result"]')).toContainText(/(refused|connection|failed)/i, { timeout: 8000 });
  // Button must be re-enabled (not stuck in "testing" state).
  await expect(btn).toBeEnabled({ timeout: 5000 });
  // Now point at the real mock — a second click must still work.
  await page.locator('#conn-port').fill('22020');
  await page.locator('#conn-user').fill('u1');
  await page.locator('#conn-pass').fill('p');
  await btn.click();
  await expect(page.locator('[data-role="result"]')).toContainText(/(connection ok|complete)/i, { timeout: 8000 });
});

test('start with bad config → fix config → start succeeds', async ({ page }) => {
  test.setTimeout(45_000);
  page.on('dialog', async (d) => { await d.accept(); });
  await page.goto('/');
  // First attempt: empty users CSV → start must fail.
  await page.locator('#conn-host').fill('127.0.0.1');
  await page.locator('#conn-port').fill('22020');
  if (await page.locator('#upload-folder').count()) await page.locator('#upload-folder').fill('inbox');
  const ne = page.locator('#normal_enabled');
  if (!await ne.isChecked()) await ne.check();
  const ta = page.locator('#normal_users');
  await ta.click(); await ta.fill(''); await ta.blur();
  await page.locator('#startBtn').click();
  await page.waitForTimeout(1500);
  let status = await page.evaluate(async () => (await fetch('/api/status', { headers: { 'X-Requested-With': 'sftp-loadtest' } })).json());
  expect(status.active, 'first start must be rejected').toBe(false);
  // Now fix the config and start again.
  await page.locator('#parallel').fill('2');
  await page.locator('#duration').fill('0.0014');
  await page.locator('#poll').fill('1');
  await page.locator('#fpm').fill('600');
  await page.locator('#nmin').fill('1');
  await page.locator('#nmax').fill('1');
  await ta.click(); await ta.fill('u1,p,f-*'); await ta.blur();
  await page.locator('#startBtn').click();
  await page.waitForFunction(async () => {
    const r = await fetch('/api/status', { headers: { 'X-Requested-With': 'sftp-loadtest' } });
    const j = await r.json();
    return j.active === true || (j.active === false && j.metrics?.total_files > 0);
  }, null, { timeout: 12_000, polling: 200 });
});
