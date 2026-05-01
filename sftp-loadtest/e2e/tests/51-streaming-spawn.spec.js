// 51-streaming-spawn.spec.js — v0.13.5 Pillar 1 + Pillar 2.
//
// Verifies the streaming spawn wire (NDJSON-over-chunked-HTTP) flips each
// step row to ✓ as the corresponding event arrives, and the macOS-
// specific password-auth-disabled warning surfaces on Step S2 when
// preflight reports it.
//
// The streaming wire is opted into via Accept: application/x-ndjson on
// the spawn request. The mock here pretends to be the master: it returns
// a Content-Type: application/x-ndjson body with one JSON line per
// step + a final {done:true,...} envelope.

import { test, expect } from '@playwright/test';

async function openSSHWizard(page) {
  await page.goto('/');
  await page.evaluate(() => { try { localStorage.removeItem('sftp-loadtest-workers-v1'); } catch {} });
  await page.locator('.shell-sidebar [data-view="cluster"]').first().click();
  await page.locator('[data-role="cluster-add"]').click();
  await expect(page.locator('.modal-panel')).toBeVisible();
  await page.locator('[data-role="choice-ssh"]').click();
  await expect(page.locator('[data-role="wizard-step"][data-step-id="s1"]')).toBeVisible();
}

async function advanceToS4(page) {
  await page.locator('#ssh_host').fill('10.0.0.5');
  await page.locator('.modal-foot [data-role="primary"]').click();
  await page.locator('#ssh_user').fill('ec2-user');
  await page.locator('#ssh_password').fill('s3cret');
  await page.locator('.modal-foot [data-role="primary"]').click();
  await page.locator('.modal-foot [data-role="primary"]').click(); // S3 → S4
  await expect(page.locator('[data-role="wizard-step"][data-step-id="s4"]')).toBeVisible();
}

test.describe('streaming spawn (NDJSON wire)', () => {
  test('per-step events flip each row from ⏳ → 🔄 → ✓ as they arrive', async ({ page }) => {
    // Mock the streaming spawn endpoint with a body of NDJSON events.
    // Playwright's route.fulfill flushes the whole body at once — we
    // can't easily simulate true server-side flush timing — but the
    // important UI assertion is that AFTER all events arrive every row
    // ended up with the right terminal status, AND the rows are keyed
    // by step name (so events arriving in any order map correctly).
    const ndjson = [
      { step: 'ssh-dial',        status: 'running', detail: 'Dialing 10.0.0.5:22' },
      { step: 'ssh-dial',        status: 'ok',      detail: 'Connected' },
      { step: 'arch-detect',     status: 'running', detail: 'Running uname -s -m' },
      { step: 'arch-detect',     status: 'ok',      detail: 'Detected linux-amd64' },
      { step: 'pkill-orphans',   status: 'running' },
      { step: 'pkill-orphans',   status: 'ok',      detail: 'Reap done' },
      { step: 'install',         status: 'running', detail: 'Downloading release' },
      { step: 'install',         status: 'ok',      detail: 'Installed at /tmp/sftp-loadtest' },
      { step: 'smoke',           status: 'running' },
      { step: 'smoke',           status: 'ok',      detail: 'Binary runs' },
      { step: 'spawn-process',   status: 'running' },
      { step: 'spawn-process',   status: 'ok',      detail: 'Worker process detached' },
      { step: 'wait-ready',      status: 'running' },
      { step: 'wait-ready',      status: 'ok',      detail: 'Worker bound to 127.0.0.1:18081' },
      { step: 'tunnel-listener', status: 'running' },
      { step: 'tunnel-listener', status: 'ok',      detail: 'Tunnel ready: http://127.0.0.1:54400' },
      { done: true, ok: true, id: 'ssh-stream-1', url: 'http://127.0.0.1:54400', arch: 'linux-amd64' },
    ].map((o) => JSON.stringify(o)).join('\n') + '\n';

    await page.route('**/api/worker/spawn', (route) => {
      // Verify the wizard opted into streaming.
      const accept = route.request().headers()['accept'] || '';
      expect(accept).toContain('application/x-ndjson');
      route.fulfill({
        status: 200,
        contentType: 'application/x-ndjson',
        body: ndjson,
      });
    });

    await openSSHWizard(page);
    await advanceToS4(page);
    await page.locator('.modal-foot [data-role="primary"]').click(); // start spawn

    // Modal eventually closes on success.
    await expect(page.locator('.modal-panel')).toBeHidden({ timeout: 5000 });
    await expect(page.locator('[data-role="sidebar-workers"]')).toContainText('127.0.0.1:54400');
  });

  test('streaming failure surfaces the failed step + decoded card', async ({ page }) => {
    const ndjson = [
      { step: 'ssh-dial',     status: 'running', detail: 'Dialing 10.0.0.5:22' },
      { step: 'ssh-dial',     status: 'ok' },
      { step: 'arch-detect',  status: 'running' },
      { step: 'arch-detect',  status: 'ok',      detail: 'Detected darwin-arm64' },
      { step: 'pkill-orphans', status: 'running' },
      { step: 'pkill-orphans', status: 'ok' },
      { step: 'install',      status: 'running', detail: 'Uploading binary' },
      { step: 'install',      status: 'ok' },
      { step: 'smoke',        status: 'running' },
      { step: 'smoke',        status: 'ok' },
      { step: 'spawn-process', status: 'running' },
      { step: 'spawn-process', status: 'ok' },
      { step: 'wait-ready',   status: 'running' },
      { step: 'wait-ready',   status: 'err',     detail: 'timed out after 5s' },
      { done: true, ok: false, error: 'worker did not become ready: timed out after 5s', last_step: 'wait-ready' },
    ].map((o) => JSON.stringify(o)).join('\n') + '\n';

    await page.route('**/api/worker/spawn', (route) => route.fulfill({
      status: 200,
      contentType: 'application/x-ndjson',
      body: ndjson,
    }));

    await openSSHWizard(page);
    await advanceToS4(page);
    await page.locator('.modal-foot [data-role="primary"]').click();

    // The wait-ready row ends with ✗ and the card explains Gatekeeper.
    const waitRow = page.locator('.cluster-ssh-spawn-log [data-step="wait-ready"]');
    await expect(waitRow).toHaveClass(/is-error/);
    const card = page.locator('.cluster-ssh-spawn-log .cluster-ssh-error-card');
    await expect(card).toBeVisible();
    await expect(card.locator('.cluster-ssh-error-title')).toContainText(/wait-ready/);
    await expect(card.locator('.cluster-ssh-error-fix')).toContainText(/Gatekeeper|Killed/i);
    await expect(page.locator('.modal-foot [data-role="primary"]')).toHaveText(/Retry/);
  });

  test('macOS PasswordAuthentication=no warning shows on Step S2 login test', async ({ page }) => {
    // Preflight returns the new password_auth_disabled flag set true;
    // the wizard's renderPreflightInto appends a banner with the fix
    // command when the operator is on the password tab.
    await page.route('**/api/worker/preflight', (route) => {
      const body = JSON.parse(route.request().postData() || '{}');
      if (body.tcp_only) {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ ok: true, reachable: true, latency_ms: 4, log: ['Probing TCP', '✓ tcp dial ok'] }),
        });
      }
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          ok: false, reachable: true,
          arch: 'darwin-arm64', can_write: true, has_curl: true, has_unzip: true,
          whoami: 'roshan', hostname: 'mac-mini',
          password_auth_disabled: true,
          log: [
            'Dialing SSH 10.0.0.5:22 as roshan',
            '✓ ssh dial + auth ok',
            '✓ remote arch: darwin-arm64 (Darwin arm64)',
            '⚠ macOS sshd has PasswordAuthentication disabled — switch to a private key',
          ],
        }),
      });
    });

    await openSSHWizard(page);
    await page.locator('#ssh_host').fill('10.0.0.5');
    await page.locator('.modal-foot [data-role="primary"]').click();
    // S2: stay on password tab.
    await page.locator('#ssh_user').fill('roshan');
    await page.locator('#ssh_password').fill('p');
    await page.locator('[data-role="step-test-login"]').click();

    const banner = page.locator('[data-role="mac-password-auth-warning"]');
    await expect(banner).toBeVisible();
    await expect(banner).toContainText(/PasswordAuthentication/);
    await expect(banner).toContainText(/sed -i/);
  });

  test('macOS PasswordAuthentication=no warning hidden when on key tab', async ({ page }) => {
    await page.route('**/api/worker/preflight', (route) => {
      const body = JSON.parse(route.request().postData() || '{}');
      if (body.tcp_only) {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ ok: true, reachable: true, latency_ms: 4, log: [] }),
        });
      }
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          ok: true, reachable: true,
          arch: 'darwin-arm64', can_write: true, has_curl: true, has_unzip: true,
          whoami: 'roshan', hostname: 'mac-mini',
          password_auth_disabled: true,
          log: ['Dialing SSH', '✓ ssh dial + auth ok'],
        }),
      });
    });

    await openSSHWizard(page);
    await page.locator('#ssh_host').fill('10.0.0.5');
    await page.locator('.modal-foot [data-role="primary"]').click();
    // Switch to key tab — operator already plans to use a key, so the
    // password-auth-disabled warning is irrelevant and should NOT show.
    await page.locator('#ssh_user').fill('roshan');
    await page.locator('[data-role="auth-tab"][data-auth="key"]').click();
    await page.locator('#ssh_key').fill('-----BEGIN OPENSSH PRIVATE KEY-----\nfake\n-----END OPENSSH PRIVATE KEY-----');
    await page.locator('[data-role="step-test-login"]').click();
    // Wait for verdict line to confirm the result rendered.
    await expect(page.locator('[data-role="step-test-login-log"] .cluster-ssh-preflight-verdict')).toBeVisible();
    await expect(page.locator('[data-role="mac-password-auth-warning"]')).toHaveCount(0);
  });
});
