// Host-key consent flows — when /api/probe replies with requires_consent
// (new key) or requires_renewal (changed key), the UI MUST surface a
// modal showing the fingerprint(s) and let the operator accept or reject
// without touching the backend known_hosts file.
//
// Backend invariant: the runner never trusts a key on its own — every
// trust decision is a UI action. These tests intercept /api/probe with
// fixture responses so the UI path is exercised without needing real key
// material.

import { test, expect } from '@playwright/test';

const NEW_FP    = 'SHA256:AAAA1111BBBB2222CCCC3333DDDD4444';
const OLD_FP    = 'SHA256:ZZZZ9999YYYY8888XXXX7777WWWW6666';

async function fillConn(page, host = 'sftp.example.com', port = '22') {
  await page.locator('[data-action="view"][data-view="configure"]').click();
  await page.locator('#conn-host').fill(host);
  await page.locator('#conn-port').fill(port);
}

test('Test Connection: requires_consent renders inline accept/reject UI with the fingerprint', async ({ page }) => {
  await page.route('**/api/probe', (route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        ok: false,
        host: 'sftp.example.com',
        port: 22,
        stage: 'ssh_or_sftp',
        requires_consent: true,
        captured_fingerprint: NEW_FP,
        captured_for_host: 'sftp.example.com',
        error: 'Server presented a new host key. Verify the fingerprint and accept to continue.',
      }),
    });
  });
  await page.goto('/');
  await fillConn(page);
  await page.locator('[data-component="connection"] [data-role="submit"]').click();

  const result = page.locator('[data-component="connection"] [data-role="result"]');
  await expect(result).toHaveAttribute('data-state', 'consent');
  await expect(result).toContainText('New host key');
  await expect(result).toContainText(NEW_FP);
  // Accept and Cancel must both be reachable from the UI — the user never
  // has to drop to the shell to manage trust.
  await expect(result.locator('[data-role="consent-accept"]')).toBeVisible();
  await expect(result.locator('[data-role="consent-cancel"]')).toBeVisible();
});

test('Test Connection: requires_renewal renders BOTH fingerprints with a danger Accept button', async ({ page }) => {
  await page.route('**/api/probe', (route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        ok: false,
        host: 'sftp.example.com',
        port: 22,
        stage: 'ssh_or_sftp',
        requires_renewal: true,
        captured_fingerprint: NEW_FP,
        captured_previous_fingerprint: OLD_FP,
        captured_for_host: 'sftp.example.com',
        error: 'Server presented a DIFFERENT host key than the one already in known_hosts. Verify out-of-band before accepting.',
      }),
    });
  });
  await page.goto('/');
  await fillConn(page);
  await page.locator('[data-component="connection"] [data-role="submit"]').click();

  const result = page.locator('[data-component="connection"] [data-role="result"]');
  await expect(result).toHaveAttribute('data-state', 'renewal');
  await expect(result).toContainText('Host key has CHANGED');
  // Both old and new must be visible to the operator — no out-of-band
  // remediation required, no raw "delete known_hosts entry" message.
  await expect(result.locator('[data-role="fp-old"]')).toContainText(OLD_FP);
  await expect(result.locator('[data-role="fp-new"]')).toContainText(NEW_FP);
  await expect(result.locator('[data-role="renewal-accept"]')).toBeVisible();
  await expect(result.locator('[data-role="renewal-cancel"]')).toBeVisible();
  // Accept is rendered with the danger tone (red) — this is a high-friction
  // path; never a normal-looking primary button.
  await expect(result.locator('[data-role="renewal-accept"]')).toHaveClass(/btn-danger/);
});

test('Test Connection: requires_renewal Accept re-probes with accept_changed=true', async ({ page }) => {
  let probeCount = 0;
  const probeBodies = [];
  await page.route('**/api/probe', async (route) => {
    probeCount += 1;
    const reqBody = route.request().postDataJSON();
    probeBodies.push(reqBody);
    if (probeCount === 1) {
      // First probe — surface the renewal prompt.
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          ok: false,
          host: 'sftp.example.com',
          port: 22,
          stage: 'ssh_or_sftp',
          requires_renewal: true,
          captured_fingerprint: NEW_FP,
          captured_previous_fingerprint: OLD_FP,
          captured_for_host: 'sftp.example.com',
        }),
      });
    }
    // Second probe should carry accept_changed:true and trust_on_first_use:true
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        ok: true,
        host: 'sftp.example.com',
        port: 22,
        stage: 'complete',
        captured_fingerprint: NEW_FP,
        captured_for_host: 'sftp.example.com',
        tcp_ms: 1, ssh_sftp_ms: 5,
      }),
    });
  });

  await page.goto('/');
  await fillConn(page);
  await page.locator('[data-component="connection"] [data-role="submit"]').click();
  const result = page.locator('[data-component="connection"] [data-role="result"]');
  await expect(result).toHaveAttribute('data-state', 'renewal');
  await result.locator('[data-role="renewal-accept"]').click();

  await expect(result).toHaveAttribute('data-state', 'ok');
  expect(probeCount).toBe(2);
  expect(probeBodies[1].accept_changed).toBe(true);
  expect(probeBodies[1].trust_on_first_use).toBe(true);
});

test('No raw "delete known_hosts entry" message can leak to the user via friendlyProbeError', async ({ page }) => {
  // Anti-regression: even if the structured requires_renewal capture
  // somehow misses, the fallback error string must NOT instruct the
  // operator to edit files. The new copy points back to the UI.
  await page.route('**/api/probe', (route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        ok: false,
        host: 'sftp.example.com',
        port: 22,
        stage: 'ssh_or_sftp',
        error: 'Host key mismatch detected. Open Test Connection to view both fingerprints and decide whether to trust the new key.',
      }),
    });
  });
  await page.goto('/');
  await fillConn(page);
  await page.locator('[data-component="connection"] [data-role="submit"]').click();
  const result = page.locator('[data-component="connection"] [data-role="result"]');
  await expect(result).toHaveAttribute('data-state', 'error');
  await expect(result).not.toContainText(/delete the offending known_hosts/i);
  await expect(result).toContainText(/Test Connection/i);
});
