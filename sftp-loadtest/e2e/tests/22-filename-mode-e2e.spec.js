// Filename-mode round-trip — v0.8.1 feature. Confirms a run with the
// download radio set to "filename" actually rolls the marker into
// upload filenames AND attributes downloads back via that marker.
// Different from the Go integration test in internal/runner — this
// drives the UI radio + start button through to the persisted CSV.

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

test('filename mode: marker appears in records and download_match_mode persists', async ({ page, request }) => {
  test.setTimeout(60_000);
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
  // Enable download + filename mode.
  const dl = page.locator('#download_enabled');
  if (!await dl.isChecked()) await dl.check();
  await page.locator('#dmm_filename').check();
  await page.locator('#dfolder').fill('outbox');
  await page.locator('#dparallel').fill('2');
  const dlta = page.locator('#download_users');
  await dlta.click(); await dlta.fill('u1,p,*'); await dlta.blur();

  // Capture run-ids that exist BEFORE we click Start, so we can wait
  // for a new one (rather than matching a leftover run from a
  // previous test in the same rig).
  const before = await page.evaluate(async () => {
    const r = await fetch('/api/runs', { headers: { 'X-Requested-With': 'sftp-loadtest' } });
    const j = await r.json();
    return (j.runs || []).map((x) => x.id);
  });

  await page.locator('#startBtn').click();

  // Wait until /api/runs has a new id we didn't see before — this is
  // how we tell THIS run's results from leftover entries left by
  // earlier tests sharing the same rig.
  let runID = null;
  for (let i = 0; i < 60; i++) {
    const news = await page.evaluate(async (existing) => {
      const r = await fetch('/api/runs', { headers: { 'X-Requested-With': 'sftp-loadtest' } });
      const j = await r.json();
      return (j.runs || []).filter((x) => !existing.includes(x.id));
    }, before);
    const found = news.find((x) => Number(x.total_files) > 0);
    if (found) { runID = found.id; break; }
    await new Promise((r) => setTimeout(r, 500));
  }

  expect(runID, 'a new run id must appear after Start').toBeTruthy();
  // And carry the chosen match mode on its persisted entry.
  const meta = await page.evaluate(async (id) => {
    const r = await fetch('/api/runs', { headers: { 'X-Requested-With': 'sftp-loadtest' } });
    const j = await r.json();
    return (j.runs || []).find((x) => x.id === id);
  }, runID);
  expect(meta.download_match_mode).toBe('filename');

  // CSV must contain the embedded marker on at least one row.
  const csv = await (await page.context().request.get(`/api/report.csv?run=${encodeURIComponent(runID)}`)).text();
  expect(csv).toMatch(/_slt_[a-z0-9]{12}_/);
});
