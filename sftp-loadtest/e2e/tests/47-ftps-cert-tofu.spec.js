// 47-ftps-cert-tofu.spec.js — wire the FTPS cert-TOFU consent flow into
// the existing hostKeyConsent UI. v0.13.0 surfaced the cert fingerprint;
// v0.13.1 turns it into a real consent gate:
//
//   - First probe of a new FTPS host (TLSStore empty, tofu off) →
//     requires_consent + fingerprint. UI renders setConsent → user
//     accepts → re-probe with trust_on_first_use=true → store records
//     the cert → silent ok next time.
//   - Cert changes after acceptance → requires_renewal + both
//     fingerprints. UI renders setRenewal (red Accept) → user accepts
//     → re-probe with accept_changed=true + trust_on_first_use=true →
//     store overwrites → silent ok next time.
//
// Both UI branches were already wired in connection.js for SSH; this
// spec proves they work for FTPS too — the data shape is identical.

import { test, expect } from '@playwright/test';

const FP_NEW = 'SHA256:1111aaaa2222bbbb3333cccc4444dddd5555eeee6666ffff7777aaaa8888bbbb';
const FP_OLD = 'SHA256:9999zzzz8888yyyy7777xxxx6666wwww5555vvvv4444uuuu3333tttt2222ssss';

async function fillFTPS(page) {
  await page.goto('/');
  await page.evaluate(() => { try { localStorage.clear(); } catch {} });
  await page.reload();
  await page.locator('[data-role="protocol-picker"] button[data-value="ftps"]').click();
  await page.locator('#conn-host').fill('ftps.acme.test');
  await page.locator('#conn-port').fill('990');
  await page.locator('#conn-user').fill('user');
  await page.locator('#conn-pass').fill('pass');
}

test('FTPS unknown cert → requires_consent → setConsent UI with fingerprint + Accept/Cancel', async ({ page }) => {
  await page.route('**/api/probe', (route) => route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({
      ok: false,
      protocol: 'ftps',
      host: 'ftps.acme.test',
      port: 990,
      stage: 'connect',
      requires_consent: true,
      captured_fingerprint: FP_NEW,
      tls_fingerprint: FP_NEW,
      captured_for_host: 'ftps.acme.test',
      error: 'FTPS server presented a new certificate. Verify the fingerprint and accept to continue.',
    }),
  }));
  await fillFTPS(page);
  await page.locator('[data-component="connection"] [data-role="submit"]').click();

  const result = page.locator('[data-component="connection"] [data-role="result"]');
  await expect(result).toHaveAttribute('data-state', 'consent');
  await expect(result).toContainText(FP_NEW);
  await expect(result.locator('[data-role="consent-accept"]')).toBeVisible();
  await expect(result.locator('[data-role="consent-cancel"]')).toBeVisible();
});

test('FTPS changed cert → requires_renewal → setRenewal UI shows BOTH fingerprints + danger Accept', async ({ page }) => {
  await page.route('**/api/probe', (route) => route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({
      ok: false,
      protocol: 'ftps',
      host: 'ftps.acme.test',
      port: 990,
      stage: 'connect',
      requires_renewal: true,
      captured_fingerprint: FP_NEW,
      tls_fingerprint: FP_NEW,
      captured_previous_fingerprint: FP_OLD,
      captured_for_host: 'ftps.acme.test',
      error: 'FTPS server presented a DIFFERENT certificate than the one already trusted. Verify out-of-band before accepting.',
    }),
  }));
  await fillFTPS(page);
  await page.locator('[data-component="connection"] [data-role="submit"]').click();

  const result = page.locator('[data-component="connection"] [data-role="result"]');
  await expect(result).toHaveAttribute('data-state', 'renewal');
  await expect(result.locator('[data-role="fp-old"]')).toContainText(FP_OLD);
  await expect(result.locator('[data-role="fp-new"]')).toContainText(FP_NEW);
  await expect(result.locator('[data-role="renewal-accept"]')).toHaveClass(/btn-danger/);
});

test('FTPS renewal Accept re-probes with accept_changed=true AND forwards FTPS fields', async ({ page }) => {
  let probeCount = 0;
  const probeBodies = [];
  await page.route('**/api/probe', async (route) => {
    probeCount += 1;
    probeBodies.push(route.request().postDataJSON());
    if (probeCount === 1) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          ok: false,
          protocol: 'ftps',
          host: 'ftps.acme.test',
          port: 990,
          stage: 'connect',
          requires_renewal: true,
          captured_fingerprint: FP_NEW,
          captured_previous_fingerprint: FP_OLD,
          captured_for_host: 'ftps.acme.test',
        }),
      });
    }
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        ok: true,
        protocol: 'ftps',
        host: 'ftps.acme.test',
        port: 990,
        stage: 'complete',
        connect_ms: 4,
        tls_fingerprint: FP_NEW,
        captured_fingerprint: FP_NEW,
        captured_for_host: 'ftps.acme.test',
      }),
    });
  });

  await fillFTPS(page);
  await page.locator('[data-component="connection"] [data-role="submit"]').click();
  const result = page.locator('[data-component="connection"] [data-role="result"]');
  await expect(result).toHaveAttribute('data-state', 'renewal');
  await result.locator('[data-role="renewal-accept"]').click();

  await expect(result).toHaveAttribute('data-state', 'ok');
  expect(probeCount).toBe(2);
  expect(probeBodies[1].accept_changed).toBe(true);
  expect(probeBodies[1].trust_on_first_use).toBe(true);
  // FTPS-specific fields must travel with the re-probe so the backend
  // can re-attempt the same TLS handshake under the new trust state.
  expect(probeBodies[1].protocol).toBe('ftps');
  expect(probeBodies[1].tls_mode).toBeDefined();
});
