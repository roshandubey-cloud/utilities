// Run flow — central feature. Configure the visible inputs (Quick
// Checks + Upload card), trigger Start Run, watch records appear,
// confirm completion lands in Previous runs.
//
// We deliberately drive the visible UI rather than the hidden legacy
// inputs so the test catches regressions in the QC ↔ legacy sync.

import { test, expect, request as playwrightRequest } from '@playwright/test';

// stopAnyActiveRun is the cross-test cleanup hook. Tests share a single
// rig (one server, one mock) for boot-time speed, so a previous test's
// active run, if not stopped, would 409 the next /api/start. Best-effort
// — a 404 means there's nothing running, which is the desired state.
async function stopAnyActiveRun(baseURL) {
  const ctx = await playwrightRequest.newContext({
    baseURL,
    extraHTTPHeaders: { 'X-Requested-With': 'sftp-loadtest' },
  });
  try {
    await ctx.post('/api/stop', { data: {} });
  } catch { /* idempotent */ }
  // Poll /api/status until active is false (or timeout)
  for (let i = 0; i < 40; i++) {
    const r = await ctx.get('/api/status');
    if (r.ok()) {
      const j = await r.json();
      if (!j.active) break;
    }
    await new Promise((r) => setTimeout(r, 200));
  }
  await ctx.dispose();
}

test.beforeEach(async ({ baseURL }) => {
  await stopAnyActiveRun(baseURL);
});

test.describe('full run lifecycle', () => {
  test.setTimeout(60_000);

  test('configure → start → records appear → finishes → shows in history', async ({ page }) => {
    await page.goto('/');

    // Trust the mock so the Start preflight doesn't get stuck on a host-key
    // prompt. The capture-phase wrapper triggers a window.confirm() for
    // changed keys; auto-accept whatever shows up.
    page.on('dialog', async (d) => { await d.accept(); });

    // Quick Checks fields drive host/port/folder via a sync hook into the
    // legacy hidden inputs.
    await page.locator('#conn-host').fill('127.0.0.1');
    await page.locator('#conn-port').fill('22020');
    // Folder either through Quick Checks or the upload-card proxy; either
    // way both should sync to legacy #folder.
    if (await page.locator('#upload-folder').count()) {
      await page.locator('#upload-folder').fill('inbox');
    } else {
      await page.locator('#conn-folder').fill('inbox');
    }

    // Run mechanics — these inputs were relocated by upload-restructure
    // but retain their original IDs.
    await page.locator('#parallel').fill('2');
    await page.locator('#duration').fill('0.0014');     // ~5 s
    await page.locator('#poll').fill('1');
    if (await page.locator('#timeout_min').count()) {
      await page.locator('#timeout_min').fill('1');
    }

    // Enable normal load + fill the rest.
    const normalToggle = page.locator('#normal_enabled');
    if (!await normalToggle.isChecked()) await normalToggle.check();
    await page.locator('#fpm').fill('600');
    await page.locator('#nmin').fill('1');
    await page.locator('#nmax').fill('1');
    const usersTa = page.locator('#normal_users');
    await usersTa.click();
    await usersTa.fill('u1,p,f-*');
    await usersTa.blur();

    // Click Start Run.
    const startBtn = page.locator('#startBtn');
    await expect(startBtn).toBeVisible();
    await startBtn.click();

    // The status must flip to active within a few seconds.
    await page.waitForFunction(async () => {
      const r = await fetch('/api/status', { headers: { 'X-Requested-With': 'sftp-loadtest' } });
      const j = await r.json();
      return j.active === true;
    }, null, { timeout: 12_000, polling: 250 });

    // Wait for completion.
    await page.waitForFunction(async () => {
      const r = await fetch('/api/status', { headers: { 'X-Requested-With': 'sftp-loadtest' } });
      const j = await r.json();
      return j.active === false && j.metrics?.total_files > 0;
    }, null, { timeout: 30_000, polling: 500 });

    // Wait for /api/runs to reflect the completed run on disk (seal +
    // meta JSON write happens after active=false). The runs-history
    // panel polls every 8 s, so we give the UI 12 s once the API
    // confirms persistence.
    await page.waitForFunction(async () => {
      const r = await fetch('/api/runs', { headers: { 'X-Requested-With': 'sftp-loadtest' } });
      const j = await r.json();
      return (j.runs || []).some((x) => Number(x.total_files) > 0);
    }, null, { timeout: 15_000, polling: 500 });

    const history = page.locator('[data-component="runs-history"]');
    await expect(history).toBeVisible();
    await expect(history).toContainText(/run-/i, { timeout: 12_000 });
  });
});

test.describe('start-run validation', () => {
  test('start refuses when no users CSV is set', async ({ page }) => {
    page.on('dialog', async (d) => { await d.dismiss(); });
    await page.goto('/');
    // Wipe persisted config from the previous test so this run sees a
    // truly empty users CSV.
    await page.evaluate(() => { try { localStorage.clear(); } catch {} });
    await page.reload();

    await page.locator('#conn-host').fill('127.0.0.1');
    await page.locator('#conn-port').fill('22020');
    if (await page.locator('#upload-folder').count()) {
      await page.locator('#upload-folder').fill('inbox');
    }
    // Clear the users textarea
    const ta = page.locator('#normal_users');
    await ta.click();
    await ta.fill('');
    await ta.blur();

    const startBtn = page.locator('#startBtn');
    await startBtn.click();

    await page.waitForTimeout(2500);
    const status = await page.evaluate(async () => {
      const r = await fetch('/api/status', { headers: { 'X-Requested-With': 'sftp-loadtest' } });
      return r.json();
    });
    expect(status.active, 'an unconfigured run must not start').toBe(false);
  });
});
