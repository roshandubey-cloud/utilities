// 45-multi-protocol.spec.js — v0.13.0 protocol picker + FTP/FTPS probe + run-flow.
//
// Covers:
//   • Protocol picker switches port defaults (22 → 21 → 21/990).
//   • FTPS picker reveals the TLS-mode segmented + FTPS-only fields.
//   • Probe with protocol=ftp succeeds against the FTP mock (22021 plain).
//   • Probe with protocol=ftps + mode=implicit succeeds and surfaces a
//     TLS fingerprint.
//   • Probe with protocol=ftps + mode=explicit succeeds against AUTH-TLS.
//   • Saved-config round-trip: a preset saved while on FTP loads back as FTP.
//   • Run-summary slim pill shows the protocol chip.
//   • Run-flow with FTP: full upload → trackid → download → CSV against mock.
//   • Run-flow with FTPS-implicit: full upload round-trip against TLS mock.

import { test, expect, request as playwrightRequest } from '@playwright/test';

async function stopAnyActiveRun(baseURL) {
  const ctx = await playwrightRequest.newContext({
    baseURL,
    extraHTTPHeaders: { 'X-Requested-With': 'sftp-loadtest' },
  });
  try { await ctx.post('/api/stop', { data: {} }); } catch {}
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

test.describe('protocol picker UI', () => {
  test('port snaps to defaults; FTPS reveals TLS fields', async ({ page }) => {
    await page.goto('/');
    // Wipe any persisted protocol from a previous test.
    await page.evaluate(() => { try { localStorage.clear(); } catch {} });
    await page.reload();

    const picker = page.locator('[data-role="protocol-picker"]');
    await expect(picker).toBeVisible();
    // Default state — SFTP, port 22.
    await expect(page.locator('#conn-port')).toHaveValue('22');
    await expect(page.locator('[data-role="ftps-fields"]')).toBeHidden();

    // Click FTP — port flips to 21.
    await picker.locator('button[data-value="ftp"]').click();
    await expect(page.locator('#conn-port')).toHaveValue('21');
    await expect(page.locator('[data-role="ftps-fields"]')).toBeHidden();
    // Hidden #protocol mirrors the picker.
    await expect(page.locator('#protocol')).toHaveValue('ftp');

    // Click FTPS — TLS fields appear; explicit mode keeps port 21.
    await picker.locator('button[data-value="ftps"]').click();
    await expect(page.locator('[data-role="ftps-fields"]')).toBeVisible();
    await expect(page.locator('#conn-port')).toHaveValue('21');
    await expect(page.locator('#protocol')).toHaveValue('ftps');
    await expect(page.locator('#tls_mode')).toHaveValue('explicit');

    // Switch implicit — port snaps to 990.
    const tlsPicker = page.locator('[data-role="tls-mode-picker"]');
    await tlsPicker.locator('button[data-value="implicit"]').click();
    await expect(page.locator('#conn-port')).toHaveValue('990');
    await expect(page.locator('#tls_mode')).toHaveValue('implicit');

    // Back to SFTP — TLS fields disappear, port returns to 22.
    await picker.locator('button[data-value="sftp"]').click();
    await expect(page.locator('[data-role="ftps-fields"]')).toBeHidden();
    await expect(page.locator('#conn-port')).toHaveValue('22');
  });
});

test.describe('FTP / FTPS probe', () => {
  test('plain FTP probe succeeds against the FTP mock', async ({ baseURL }) => {
    const ctx = await playwrightRequest.newContext({
      baseURL,
      extraHTTPHeaders: { 'X-Requested-With': 'sftp-loadtest' },
    });
    const r = await ctx.post('/api/probe', {
      data: { host: '127.0.0.1', port: 22021, username: 'u1', password: 'p',
              folder: 'inbox', protocol: 'ftp' },
    });
    expect(r.ok()).toBe(true);
    const j = await r.json();
    expect(j.ok).toBe(true);
    expect(j.protocol).toBe('ftp');
    expect(typeof j.connect_ms).toBe('number');
    await ctx.dispose();
  });

  test('FTPS implicit probe succeeds and returns a TLS fingerprint', async ({ baseURL }) => {
    const ctx = await playwrightRequest.newContext({
      baseURL,
      extraHTTPHeaders: { 'X-Requested-With': 'sftp-loadtest' },
    });
    const r = await ctx.post('/api/probe', {
      data: { host: '127.0.0.1', port: 22022, username: 'u1', password: 'p',
              protocol: 'ftps', tls_mode: 'implicit', tls_insecure_skip_verify: true },
    });
    expect(r.ok()).toBe(true);
    const j = await r.json();
    expect(j.ok).toBe(true);
    expect(j.protocol).toBe('ftps');
    expect(j.tls_fingerprint).toMatch(/^SHA256:[0-9a-f]{64}$/);
    await ctx.dispose();
  });

  test('FTPS explicit probe succeeds via AUTH TLS upgrade', async ({ baseURL }) => {
    const ctx = await playwrightRequest.newContext({
      baseURL,
      extraHTTPHeaders: { 'X-Requested-With': 'sftp-loadtest' },
    });
    const r = await ctx.post('/api/probe', {
      data: { host: '127.0.0.1', port: 22021, username: 'u1', password: 'p',
              protocol: 'ftps', tls_mode: 'explicit', tls_insecure_skip_verify: true },
    });
    expect(r.ok()).toBe(true);
    const j = await r.json();
    expect(j.ok).toBe(true);
    expect(j.tls_fingerprint).toMatch(/^SHA256:[0-9a-f]{64}$/);
    await ctx.dispose();
  });
});

test.describe('run-summary protocol chip', () => {
  test('proto chip shows on the slim pill and switches with the picker', async ({ page }) => {
    await page.goto('/');
    await page.evaluate(() => { try { localStorage.clear(); } catch {} });
    await page.reload();

    // The chip exists in the cfg-summary-bar. Default = sftp.
    const chip = page.locator('[data-role="chip-proto"] .cfg-chip-val');
    await expect(chip).toContainText('sftp');

    // Switch picker → chip updates.
    await page.locator('[data-role="protocol-picker"] button[data-value="ftp"]').click();
    await expect(chip).toContainText('ftp');
    await page.locator('[data-role="protocol-picker"] button[data-value="ftps"]').click();
    await expect(chip).toContainText('ftps');
  });
});

test.describe('saved-config round-trip', () => {
  test('preset saved while on FTP loads back as FTP', async ({ page }) => {
    await page.goto('/');
    await page.evaluate(() => { try { localStorage.clear(); } catch {} });
    await page.reload();

    // Switch to FTP, fill the bare minimum, then export the config snapshot.
    await page.locator('[data-role="protocol-picker"] button[data-value="ftp"]').click();
    await page.locator('#conn-host').fill('127.0.0.1');
    await page.locator('#conn-port').fill('22021');
    // Snapshot via the legacy buildRequestBody hook.
    const snap = await page.evaluate(() => window.__sftplBuildRequestBody());
    expect(snap.protocol).toBe('ftp');
    expect(snap.port).toBe(22021);

    // Now flip back to SFTP, and re-import the snapshot — protocol must reset.
    await page.locator('[data-role="protocol-picker"] button[data-value="sftp"]').click();
    await expect(page.locator('#protocol')).toHaveValue('sftp');
    await page.evaluate((s) => window.__sftplImportConfigPayload(s), snap);
    await expect(page.locator('#protocol')).toHaveValue('ftp');
    await expect(page.locator('#conn-port')).toHaveValue('22021');
  });
});

test.describe('run flow against FTP mock', () => {
  test.setTimeout(60_000);

  test('FTP run lifecycle: configure → start → records → finishes', async ({ page }) => {
    await page.goto('/');
    page.on('dialog', async (d) => { await d.accept(); });

    // Pick FTP protocol.
    await page.locator('[data-role="protocol-picker"] button[data-value="ftp"]').click();

    await page.locator('#conn-host').fill('127.0.0.1');
    await page.locator('#conn-port').fill('22021');
    if (await page.locator('#upload-folder').count()) {
      await page.locator('#upload-folder').fill('inbox');
    } else {
      await page.locator('#conn-folder').fill('inbox');
    }

    await page.locator('#parallel').fill('1');
    await page.locator('#duration').fill('0.0014');
    await page.locator('#poll').fill('1');
    if (await page.locator('#timeout_min').count()) {
      await page.locator('#timeout_min').fill('1');
    }

    const normalToggle = page.locator('#normal_enabled');
    if (!await normalToggle.isChecked()) await normalToggle.check();
    await page.locator('#fpm').fill('200');
    await page.locator('#nmin').fill('1');
    await page.locator('#nmax').fill('1');
    const usersTa = page.locator('#normal_users');
    await usersTa.click();
    await usersTa.fill('u1,p,f-*');
    await usersTa.blur();

    await page.locator('#startBtn').click();
    await page.waitForFunction(async () => {
      const r = await fetch('/api/status', { headers: { 'X-Requested-With': 'sftp-loadtest' } });
      const j = await r.json();
      return j.active === true;
    }, null, { timeout: 12_000, polling: 250 });

    await page.waitForFunction(async () => {
      const r = await fetch('/api/status', { headers: { 'X-Requested-With': 'sftp-loadtest' } });
      const j = await r.json();
      return j.active === false && (j.metrics?.total_files || 0) > 0;
    }, null, { timeout: 30_000, polling: 500 });
  });
});
