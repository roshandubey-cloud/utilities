// Run detail pane (β1) — click a run, get a full pane with KPIs,
// latency bars, host peaks, suggestions, and records.

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

test('clicking Open on a runs-history card opens the detail pane', async ({ page }) => {
  test.setTimeout(45_000);
  page.on('dialog', async (d) => { await d.accept(); });
  await page.goto('/');

  // Configure + run a quick smoke so the runs-history card has at least one entry.
  await page.locator('#conn-host').fill('127.0.0.1');
  await page.locator('#conn-port').fill('22020');
  if (await page.locator('#upload-folder').count()) await page.locator('#upload-folder').fill('inbox');
  await page.locator('#parallel').fill('2');
  await page.locator('#duration').fill('0.0014');
  await page.locator('#poll').fill('1');
  await page.locator('#fpm').fill('600');
  await page.locator('#nmin').fill('1');
  await page.locator('#nmax').fill('1');
  const ta = page.locator('#normal_users');
  await ta.click(); await ta.fill('u1,p,f-*'); await ta.blur();
  if (!(await page.locator('#normal_enabled').isChecked())) await page.locator('#normal_enabled').check();
  await page.locator('#startBtn').click();
  // Wait for completion.
  await page.waitForFunction(async () => {
    const r = await fetch('/api/status', { headers: { 'X-Requested-With': 'sftp-loadtest' } });
    const j = await r.json();
    return j.active === false && j.metrics?.total_files > 0;
  }, null, { timeout: 30_000, polling: 500 });
  // Wait for /api/runs to populate.
  await page.waitForFunction(async () => {
    const r = await fetch('/api/runs', { headers: { 'X-Requested-With': 'sftp-loadtest' } });
    const j = await r.json();
    return (j.runs || []).some((x) => Number(x.total_files) > 0);
  }, null, { timeout: 15_000, polling: 500 });

  // Wait for the runs-history card and its decorated Open button.
  const card = page.locator('[data-component="runs-history"] .runs-history-card').first();
  await expect(card).toBeVisible({ timeout: 15_000 });
  const openBtn = card.locator('button:has-text("Open")');
  await expect(openBtn).toBeVisible({ timeout: 10_000 });
  await openBtn.click();

  // Detail view appears.
  const detail = page.locator('[data-component="run-detail"]');
  await expect(detail).toBeVisible();
  await expect(detail).toContainText(/run-/i);
  await expect(detail).toContainText(/success rate/i);
  await expect(detail).toContainText(/latency percentiles/i);
  await expect(detail).toContainText(/workload/i);
  await expect(detail).toContainText(/local host peaks/i);

  // Back button returns to the workbench.
  await detail.locator('[data-role="back"]').click();
  // Main pane is no longer hidden.
  await expect(page.locator('.shell-main')).toHaveAttribute('data-hidden', '0');
});
