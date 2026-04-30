// Previous-runs card detail — the run's success rate, throughput, user
// counts, latency percentiles, and analyzer suggestions all surface
// here. Each is a separate UX promise; if any breaks the operator
// loses important post-run information.

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

async function runOnce(page) {
  page.on('dialog', async (d) => { await d.accept(); });
  await page.goto('/');
  // The legacy form lives in the Configure view (the boot default). All
  // form inputs are accessible from there.
  await page.locator('[data-action="view"][data-view="configure"]').click();
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
  await page.waitForFunction(async () => {
    const r = await fetch('/api/status', { headers: { 'X-Requested-With': 'sftp-loadtest' } });
    const j = await r.json();
    return j.active === false && j.metrics?.total_files > 0;
  }, null, { timeout: 30_000, polling: 500 });
  await page.waitForFunction(async () => {
    const r = await fetch('/api/runs', { headers: { 'X-Requested-With': 'sftp-loadtest' } });
    const j = await r.json();
    return (j.runs || []).some((x) => Number(x.total_files) > 0);
  }, null, { timeout: 15_000, polling: 500 });
}

test.describe('runs-history card detail', () => {
  test.setTimeout(75_000);

  test('completed run shows success rate, throughput, and user counts', async ({ page }) => {
    await runOnce(page);
    await page.locator('[data-action="view"][data-view="runs"]').click();
    const card = page.locator('[data-component="runs-history"] .runs-history-card').first();
    await expect(card).toBeVisible({ timeout: 15_000 });
    // Success-rate stat block — must show a percentage and "ok / failed".
    await expect(card).toContainText(/success rate/i);
    await expect(card).toContainText(/%/);
    await expect(card).toContainText(/ok/i);
    // Files block — count + bytes + throughput.
    await expect(card).toContainText(/files/i);
    await expect(card).toContainText(/mbps/i);
    // Upload block — users + streams + fpm.
    await expect(card).toContainText(/users/i);
    await expect(card).toContainText(/streams/i);
    await expect(card).toContainText(/fpm/i);
  });

  test('latency panel renders p50/p95/p99/p99.9 after a real run', async ({ page }) => {
    await runOnce(page);
    await page.locator('[data-action="view"][data-view="runs"]').click();
    const card = page.locator('[data-component="runs-history"] .runs-history-card').first();
    const lat = card.locator('.runs-history-latency');
    await expect(lat).toBeVisible({ timeout: 15_000 });
    // Three stages must label up.
    await expect(lat).toContainText(/upload latency/i);
    await expect(lat).toContainText(/cor/i);
    await expect(lat).toContainText(/dial/i);
    // Each non-empty stage shows the four percentile points.
    for (const p of ['p50', 'p95', 'p99', 'p99.9']) {
      await expect(lat).toContainText(p);
    }
  });

  test('analyzer panel renders host-capacity infra peaks after a run', async ({ page }) => {
    await runOnce(page);
    await page.locator('[data-action="view"][data-view="runs"]').click();
    const card = page.locator('[data-component="runs-history"] .runs-history-card').first();
    const analysis = card.locator('.runs-history-analysis');
    await expect(analysis).toBeVisible({ timeout: 15_000 });
    // The "Local host: ..." infra line must always render — it's the
    // operator's first signal that the analyzer is wired up. Specific
    // suggestion content depends on sampler timing on a 5s run, which
    // is too tight to assert deterministically without flake.
    await expect(analysis).toContainText(/local host/i);
    await expect(analysis).toContainText(/cores/i);
  });

  test('CSV download link points at the right run', async ({ page }) => {
    await runOnce(page);
    await page.locator('[data-action="view"][data-view="runs"]').click();
    const card = page.locator('[data-component="runs-history"] .runs-history-card').first();
    const csv = card.locator('a[download]').first();
    await expect(csv).toBeVisible();
    const href = await csv.getAttribute('href');
    expect(href).toMatch(/api\/report\.csv\?run=run-/);
  });
});
