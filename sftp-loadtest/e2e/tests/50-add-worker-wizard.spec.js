// 50-add-worker-wizard.spec.js — drives the v0.13.4 Add Worker wizard
// end-to-end. Replaces the form-with-tabs from v0.13.3 with a guided
// step-by-step flow.
//
// The wizard:
//   Step 0  — entry choice (Already running URL / Install fresh over SSH).
//   URL flow — single screen, URL + optional basic auth.
//   SSH flow — S1 Where, S2 Who, S3 How, S4 Install.
//   Each step's "Test this" button is optional but verifies the step.
//   Progress strip at the top reflects the active + completed steps.
//
// This spec covers:
//   1. Entry choice renders both cards.
//   2. SSH happy path: S1 reach test, S2 login test, S3 prereq verify,
//      S4 review + install + spawn → toast + sidebar entry.
//   3. SSH failure path on Step S4 — decoded error card, Retry button,
//      Back-to-fix-earlier-step button.
//   4. URL flow stays single-screen.

import { test, expect } from '@playwright/test';

async function openWizard(page) {
  await page.goto('/');
  await page.evaluate(() => { try { localStorage.removeItem('sftp-loadtest-workers-v1'); } catch {} });
  await page.locator('[data-action="view"][data-view="cluster"]').click();
  await page.locator('[data-role="cluster-add"]').click();
  await expect(page.locator('.modal-panel-wizard')).toBeVisible();
}

test.describe('Add Worker wizard', () => {
  test('Step 0 entry choice shows both options', async ({ page }) => {
    await openWizard(page);
    await expect(page.locator('[data-role="choice-url"]')).toBeVisible();
    await expect(page.locator('[data-role="choice-url"]')).toContainText(/Already running.*URL/i);
    await expect(page.locator('[data-role="choice-ssh"]')).toBeVisible();
    await expect(page.locator('[data-role="choice-ssh"]')).toContainText(/install one fresh/i);
  });

  test('URL flow stays a single screen', async ({ page }) => {
    await openWizard(page);
    await page.locator('[data-role="choice-url"]').click();
    await expect(page.locator('[data-role="wizard-step"][data-step-id="url"]')).toBeVisible();
    // Next is disabled until URL is typed.
    await expect(page.locator('.modal-foot [data-role="primary"]')).toBeDisabled();
    await page.locator('#addw_url').fill('http://10.0.0.5:8080');
    await expect(page.locator('.modal-foot [data-role="primary"]')).toBeEnabled();
    await page.locator('.modal-foot [data-role="primary"]').click();
    // Worker registered and modal closes.
    await expect(page.locator('.modal-panel')).toBeHidden({ timeout: 4000 });
    await expect(page.locator('[data-role="sidebar-workers"]')).toContainText('10.0.0.5:8080');
  });

  test('SSH wizard happy path: reach → login → prereq → install', async ({ page }) => {
    // Step S1 reach probe — tcp_only path, no auth needed.
    await page.route('**/api/worker/preflight', async (route) => {
      const body = JSON.parse(route.request().postData() || '{}');
      if (body.tcp_only) {
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            ok: true, reachable: true, latency_ms: 7,
            log: ['Probing TCP 10.0.0.5:22', '✓ tcp dial ok in 7 ms'],
          }),
        });
        return;
      }
      // Step S2 login + Step S3 full preflight.
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          ok: true, reachable: true,
          arch: 'linux-amd64', can_write: true, has_curl: true, has_unzip: true,
          whoami: 'ec2-user', hostname: 'ip-10-0-0-5',
          log: [
            'Dialing SSH 10.0.0.5:22 as ec2-user',
            '✓ ssh dial + auth ok',
            '✓ remote arch: linux-amd64 (Linux x86_64)',
            '✓ remote whoami: ec2-user',
            '✓ remote hostname: ip-10-0-0-5',
            '✓ install path writable: /tmp',
            '✓ curl available',
            '✓ unzip available',
            'Preflight passed — ready to spawn.',
          ],
        }),
      });
    });
    await page.route('**/api/worker/spawn', (route) => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        id: 'ssh-mock-2',
        url: 'http://127.0.0.1:54399',
        arch: 'linux-amd64',
        log: ['Tunnel ready'],
      }),
    }));

    await openWizard(page);
    await page.locator('[data-role="choice-ssh"]').click();

    // S1: type host, click reach test.
    await expect(page.locator('[data-role="wizard-step"][data-step-id="s1"]')).toBeVisible();
    await expect(page.locator('.modal-foot [data-role="primary"]')).toBeDisabled();
    await page.locator('#ssh_host').fill('10.0.0.5');
    await expect(page.locator('.modal-foot [data-role="primary"]')).toBeEnabled();
    await page.locator('[data-role="step-test-tcp"]').click();
    await expect(page.locator('[data-role="step-test-tcp-log"] .cluster-ssh-preflight-verdict.is-ok')).toContainText(/REACHABLE/);
    await page.locator('.modal-foot [data-role="primary"]').click();

    // S2: user + password, click login test.
    await expect(page.locator('[data-role="wizard-step"][data-step-id="s2"]')).toBeVisible();
    await page.locator('#ssh_user').fill('ec2-user');
    // Default auth tab is Password.
    await page.locator('#ssh_password').fill('s3cret');
    await page.locator('[data-role="step-test-login"]').click();
    await expect(page.locator('[data-role="step-test-login-log"] .cluster-ssh-preflight-verdict.is-ok')).toContainText(/LOGIN OK/);
    await page.locator('.modal-foot [data-role="primary"]').click();

    // S3: choose Upload, verify prerequisites.
    await expect(page.locator('[data-role="wizard-step"][data-step-id="s3"]')).toBeVisible();
    await page.locator('input[name="ssh_install"][value="upload"]').check();
    await page.locator('[data-role="step-test-prereq"]').click();
    await expect(page.locator('[data-role="step-test-prereq-log"] .cluster-ssh-preflight-verdict.is-ok')).toContainText(/READY/);
    await page.locator('.modal-foot [data-role="primary"]').click();

    // S4: review card + install button.
    await expect(page.locator('[data-role="wizard-step"][data-step-id="s4"]')).toBeVisible();
    await expect(page.locator('.wizard-review-card')).toContainText('10.0.0.5:22');
    await expect(page.locator('.wizard-review-card')).toContainText('ec2-user');
    await expect(page.locator('.wizard-review-card')).toContainText('Upload local binary');
    await page.locator('.modal-foot [data-role="primary"]').click();

    // Modal closes, sidebar shows new worker.
    await expect(page.locator('.modal-panel')).toBeHidden({ timeout: 5000 });
    await expect(page.locator('[data-role="sidebar-workers"]')).toContainText('127.0.0.1:54399');
  });

  test('SSH wizard failure on Step S4 surfaces decoded error + Retry + Back-to-fix', async ({ page }) => {
    await page.route('**/api/worker/spawn', (route) => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        ok: false,
        error: 'ssh dial: connect: connection refused',
        log: ['Dialing SSH'],
      }),
    }));

    await openWizard(page);
    await page.locator('[data-role="choice-ssh"]').click();
    await page.locator('#ssh_host').fill('10.0.0.5');
    await page.locator('.modal-foot [data-role="primary"]').click();
    await page.locator('#ssh_user').fill('ec2-user');
    await page.locator('#ssh_password').fill('p');
    await page.locator('.modal-foot [data-role="primary"]').click();
    // Skip prereq verification.
    await page.locator('.modal-foot [data-role="primary"]').click();
    // S4 install.
    await page.locator('.modal-foot [data-role="primary"]').click();

    const card = page.locator('.cluster-ssh-spawn-log .cluster-ssh-error-card');
    await expect(card).toBeVisible();
    await expect(card.locator('.cluster-ssh-error-title')).toContainText(/Connection refused/i);
    await expect(card.locator('.cluster-ssh-error-fix')).toContainText(/systemctl/);
    // Retry button + Back-to-fix-earlier-step both visible.
    await expect(page.locator('.modal-foot [data-role="primary"]')).toHaveText(/Retry/);
    await expect(page.locator('[data-role="back-to-fix"]')).toBeVisible();
  });

  test('progress strip lights up completed steps and lets you click back', async ({ page }) => {
    await openWizard(page);
    await page.locator('[data-role="choice-ssh"]').click();
    await page.locator('#ssh_host').fill('10.0.0.5');
    await page.locator('.modal-foot [data-role="primary"]').click();
    // After advancing, step S1 is marked done.
    const progress = page.locator('[data-role="wizard-progress"]');
    await expect(progress).toBeVisible();
    const s1Btn = progress.locator('[data-step="s1"]');
    await expect(s1Btn).toHaveClass(/is-done/);
    // Currently on S2.
    await expect(progress.locator('[data-step="s2"]')).toHaveClass(/is-active/);
    // Click S1 in the strip → wizard goes back.
    await s1Btn.click();
    await expect(page.locator('[data-role="wizard-step"][data-step-id="s1"]')).toBeVisible();
    // Host value persisted.
    await expect(page.locator('#ssh_host')).toHaveValue('10.0.0.5');
  });
});
