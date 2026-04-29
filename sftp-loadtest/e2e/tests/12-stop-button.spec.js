// Stop button — must transition the active run to inactive promptly.
// Verifies the operator can abort a long-running test without waiting
// for the duration to elapse.

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

test('stop button aborts an active run within a couple of seconds', async ({ page }) => {
  test.setTimeout(45_000);
  page.on('dialog', async (d) => { await d.accept(); });
  await page.goto('/');
  // Start a long run (1 hour) so we have plenty of time to stop it.
  await page.locator('#conn-host').fill('127.0.0.1');
  await page.locator('#conn-port').fill('22020');
  if (await page.locator('#upload-folder').count()) await page.locator('#upload-folder').fill('inbox');
  await page.locator('#parallel').fill('2');
  await page.locator('#duration').fill('1');           // 1 hour
  await page.locator('#poll').fill('1');
  if (await page.locator('#timeout_min').count()) await page.locator('#timeout_min').fill('1');
  const ne = page.locator('#normal_enabled');
  if (!await ne.isChecked()) await ne.check();
  await page.locator('#fpm').fill('60');
  await page.locator('#nmin').fill('1');
  await page.locator('#nmax').fill('1');
  const ta = page.locator('#normal_users');
  await ta.click(); await ta.fill('u1,p,f-*'); await ta.blur();

  await page.locator('#startBtn').click();
  await page.waitForFunction(async () => {
    const r = await fetch('/api/status', { headers: { 'X-Requested-With': 'sftp-loadtest' } });
    const j = await r.json();
    return j.active === true;
  }, null, { timeout: 12_000, polling: 200 });

  // Click Stop. The button is at #stopBtn in the legacy actions row.
  const stop = page.locator('#stopBtn');
  await expect(stop).toBeVisible();
  await stop.click();

  // Active should flip to false within 5 s (graceful drain typically completes well under that).
  await page.waitForFunction(async () => {
    const r = await fetch('/api/status', { headers: { 'X-Requested-With': 'sftp-loadtest' } });
    const j = await r.json();
    return j.active === false;
  }, null, { timeout: 8_000, polling: 200 });
});
