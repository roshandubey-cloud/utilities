// Public-key SSH auth — v1 of the SFTP key auth feature.
//
// Coverage:
//   1. /api/probe accepts a private_key body and succeeds against the mock
//   2. Malformed PEM yields a clean error message (no stack / path leak)
//   3. A full run with key auth completes (record persisted)
//   4. The configure run-summary shows the auth-mode chip when PEM is set
//
// The mock SFTP server in global-setup accepts ANY public key, so the
// test rig only needs a well-formed PEM (generated in global-setup
// via Node's crypto). Password-auth specs in 03 / 22 keep working
// because the mock also keeps PasswordCallback enabled.

import { test, expect, request as playwrightRequest } from '@playwright/test';

const PEM = process.env.SFTPL_TESTKEY_PEM;

async function stopAnyActiveRun(baseURL) {
  const ctx = await playwrightRequest.newContext({
    baseURL,
    extraHTTPHeaders: { 'X-Requested-With': 'sftp-loadtest' },
  });
  try { await ctx.post('/api/stop', { data: {} }); } catch {}
  for (let i = 0; i < 40; i++) {
    const r = await ctx.get('/api/status');
    if (r.ok()) { const j = await r.json(); if (!j.active) break; }
    await new Promise((r) => setTimeout(r, 200));
  }
  await ctx.dispose();
}

test.beforeEach(async ({ baseURL }) => { await stopAnyActiveRun(baseURL); });

test.describe('SSH public-key auth', () => {
  test.setTimeout(75_000);

  test('probe with PEM body succeeds against the mock', async ({ request }) => {
    expect(PEM, 'global-setup must export SFTPL_TESTKEY_PEM').toBeTruthy();
    const r = await request.post('/api/probe', {
      data: {
        host: '127.0.0.1',
        port: 22020,
        username: 'u1',
        // Password is irrelevant when private_key is set — the mock
        // accepts any key, and the probe code uses key auth when PEM
        // is non-empty.
        password: 'ignored',
        private_key: PEM,
      },
    });
    expect(r.ok()).toBeTruthy();
    const j = await r.json();
    expect(j.ok, `probe failed: ${j.error || JSON.stringify(j)}`).toBeTruthy();
  });

  test('probe with malformed PEM returns a clean error', async ({ request }) => {
    const r = await request.post('/api/probe', {
      data: {
        host: '127.0.0.1',
        port: 22020,
        username: 'u1',
        password: 'ignored',
        private_key: 'this is not a real PEM',
      },
    });
    expect(r.ok()).toBeTruthy();
    const j = await r.json();
    expect(j.ok).toBeFalsy();
    // Must mention "private key" so the operator sees what's wrong;
    // must NOT leak a stack trace, library symbol, or filesystem path.
    expect(j.error || '').toMatch(/private key/i);
    expect(j.error || '').not.toMatch(/goroutine|\.go:\d+|\/Users\/|\/home\//);
  });

  test('run with key auth completes upload', async ({ page }) => {
    expect(PEM).toBeTruthy();
    page.on('dialog', async (d) => { await d.accept(); });
    await page.goto('/');
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
    await ta.click(); await ta.fill('u1,p,k-*'); await ta.blur();

    // Open the disclosure and paste the PEM so buildRequestBody
    // attaches private_key_pem to the start request body.
    const disclosure = page.locator('[data-role="key-disclosure"]');
    if (!(await disclosure.evaluate((el) => el.open))) {
      await disclosure.locator('summary').click();
    }
    await page.locator('#conn-private-key').fill(PEM);

    // Capture run-ids that exist BEFORE Start so we wait for THIS run's
    // completion, not a leftover from earlier specs.
    const before = await page.evaluate(async () => {
      const r = await fetch('/api/runs', { headers: { 'X-Requested-With': 'sftp-loadtest' } });
      const j = await r.json();
      return (j.runs || []).map((x) => x.id);
    });

    await page.locator('#startBtn').click();

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
    expect(runID, 'a new run must complete with at least one record').toBeTruthy();
  });

  test('configure run-summary shows the key indicator when PEM is set', async ({ page }) => {
    expect(PEM).toBeTruthy();
    await page.goto('/');
    await page.locator('[data-action="view"][data-view="configure"]').click();
    // Without PEM the chip should read "pass".
    const chip = page.locator('[data-role="chip-auth"]');
    await expect(chip).toContainText(/pass/i, { timeout: 5_000 });

    // Open the disclosure + paste a key. The summary refreshes on input
    // change, so the chip flips to the key indicator within a tick.
    const disclosure = page.locator('[data-role="key-disclosure"]');
    if (!(await disclosure.evaluate((el) => el.open))) {
      await disclosure.locator('summary').click();
    }
    await page.locator('#conn-private-key').fill(PEM);
    // Trigger an input event explicitly so the live re-render fires
    // even if Playwright's fill didn't bubble one (textarea quirks).
    await page.locator('#conn-private-key').dispatchEvent('input');

    await expect(chip).toContainText(/key/i, { timeout: 5_000 });
  });
});
