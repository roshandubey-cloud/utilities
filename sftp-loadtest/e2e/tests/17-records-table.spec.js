// Records (live activity) — during a run, every uploaded file must
// appear as a row with all the columns the operator expects (user,
// kind, filename, sizes, durations, errors, download fields, etc.)

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

test('records table populates with rows during an active run', async ({ page }) => {
  test.setTimeout(45_000);
  page.on('dialog', async (d) => { await d.accept(); });
  await page.goto('/');
  await page.locator('#conn-host').fill('127.0.0.1');
  await page.locator('#conn-port').fill('22020');
  if (await page.locator('#upload-folder').count()) await page.locator('#upload-folder').fill('inbox');
  await page.locator('#parallel').fill('2');
  await page.locator('#duration').fill('0.0014');
  await page.locator('#poll').fill('1');
  if (await page.locator('#timeout_min').count()) await page.locator('#timeout_min').fill('1');
  const ne = page.locator('#normal_enabled');
  if (!await ne.isChecked()) await ne.check();
  await page.locator('#fpm').fill('600');
  await page.locator('#nmin').fill('1');
  await page.locator('#nmax').fill('1');
  const ta = page.locator('#normal_users');
  await ta.click(); await ta.fill('u1,p,f-*'); await ta.blur();

  await page.locator('#startBtn').click();
  // The records component should show at least one tabular row.
  const records = page.locator('[data-component="records"]');
  await expect(records).toContainText(/u1/, { timeout: 12_000 });
  // Headers should label the columns the operator depends on.
  for (const col of [/user/i, /file/i, /kind/i, /size/i]) {
    await expect(records).toContainText(col);
  }
});
