// 06 — happy-path probes against the mock SFTP / FTP / FTPS
// servers spun up by global-setup. Each spec exercises the real
// /api/probe handler against the matching mock, asserts the
// connection OK headline lands, and confirms the Details
// disclosure opens with non-trivial body content.
//
// These specs catch class-of-bug regressions that the per-bug
// specs would miss — e.g. an FTPS TLS rebuild that breaks against
// implicit mode but leaves SFTP intact.
import { test, expect, Page } from '@playwright/test';
import { gotoConfigure, switchProtocol } from '../fixtures/server';
import { MOCK_SFTP_PORT, MOCK_FTP_PORT, MOCK_FTPS_PORT } from '../global-setup';

async function fillCreds(page: Page, port: number, user = 'up1', pass = 'p') {
  const conn = page.locator('[data-component="connection"]');
  await conn.locator('[data-role="host"]').fill('127.0.0.1');
  // Replace contents — input may already carry a default port for
  // the protocol; we always overwrite to point at the mock.
  await conn.locator('[data-role="port"]').fill(String(port));
  await conn.locator('[data-role="username"]').fill(user);
  await conn.locator('[data-role="password"]').fill(pass);
}

async function runProbe(page: Page) {
  await page.locator('[data-component="connection"] [data-role="submit"]').click();
  const result = page.locator('[data-component="connection"] [data-role="result"]');
  await result.waitFor({ state: 'visible' });
  // Settled = the "Testing connection…" placeholder is gone OR a
  // consent / OK headline has rendered.  Wait up to 20s — the
  // first probe of the suite cold-starts the SSH KEX.
  await page.waitForFunction(() => {
    const el = document.querySelector('[data-component="connection"] [data-role="result"]');
    if (!el) return false;
    const t = el.textContent ?? '';
    return !t.includes('Testing connection') && t.length > 0;
  }, { timeout: 20_000 });
  return result;
}

test.describe('happy-path probes against mock servers', () => {
  test('SFTP probe to mock server reports Connection OK', async ({ page }) => {
    await gotoConfigure(page);
    await switchProtocol(page, 'sftp');
    await fillCreds(page, MOCK_SFTP_PORT);

    // The test server boots with -insecure-host-key so the SFTP
    // layer accepts any host key without invoking the trust store.
    // Probe lands directly on "Connection OK" — no consent prompt.
    await runProbe(page);
    const text = (await page.locator('[data-component="connection"] [data-role="result"]').textContent()) ?? '';
    expect(text).toContain('Connection OK');
  });

  test('FTP plain probe to mock server reports Connection OK', async ({ page }) => {
    await gotoConfigure(page);
    await switchProtocol(page, 'ftp');
    await fillCreds(page, MOCK_FTP_PORT);
    await runProbe(page);
    const text = (await page.locator('[data-component="connection"] [data-role="result"]').textContent()) ?? '';
    expect(text).toContain('Connection OK');
  });

  test('FTPS implicit probe to mock server reports Connection OK with skip-verify', async ({ page }) => {
    await gotoConfigure(page);
    await switchProtocol(page, 'ftps');
    // The mock self-signs its TLS cert; flip the Trust self-signed
    // toggle so the probe doesn't error on cert validation. The
    // implicit-TLS port serves TLS from byte 0.
    await page.locator('[data-component="connection"] [data-role="tls-mode-picker"] button[data-value="implicit"]').click();
    // The tls-skip-verify checkbox is wrapped in a custom toggle widget;
    // the <span class="toggle-thumb"> over-paints the input and intercepts
    // pointer events. force:true tells Playwright to bypass that.
    await page.locator('[data-component="connection"] [data-role="tls-skip-verify"]').check({ force: true });
    await fillCreds(page, MOCK_FTPS_PORT);
    await runProbe(page);
    const text = (await page.locator('[data-component="connection"] [data-role="result"]').textContent()) ?? '';
    expect(text).toContain('Connection OK');
  });

  test('FTPS explicit probe to mock server (AUTH TLS upgrade) reports Connection OK', async ({ page }) => {
    await gotoConfigure(page);
    await switchProtocol(page, 'ftps');
    await page.locator('[data-component="connection"] [data-role="tls-mode-picker"] button[data-value="explicit"]').click();
    // The tls-skip-verify checkbox is wrapped in a custom toggle widget;
    // the <span class="toggle-thumb"> over-paints the input and intercepts
    // pointer events. force:true tells Playwright to bypass that.
    await page.locator('[data-component="connection"] [data-role="tls-skip-verify"]').check({ force: true });
    // Explicit TLS uses the plain FTP port, then upgrades.
    await fillCreds(page, MOCK_FTP_PORT);
    await runProbe(page);
    const text = (await page.locator('[data-component="connection"] [data-role="result"]').textContent()) ?? '';
    expect(text).toContain('Connection OK');
  });
});
