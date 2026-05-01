// 49-spawn-error-guidance.spec.js — situation-aware error guidance
// for SSH-bootstrapped workers. Updated for the v0.13.4 wizard.
//
// The wizard:
//   - Step 0 picks "Already running" vs "Install fresh".
//   - SSH flow: S1 (host/port) → S2 (user/password/key) → S3 (install
//     method + Verify prereqs button) → S4 (review + Install + spawn).
//   - decodeSpawnError + renderErrorCard fire on any test/spawn failure.
//   - Smart-username hint applies on Step S1 + carries to Step S2.
//   - "Need help? Common SSH gotchas" disclosure is in the modal foot
//     and visible on every SSH wizard step.

import { test, expect } from '@playwright/test';

async function openSSHWizard(page) {
  await page.goto('/');
  await page.locator('[data-action="view"][data-view="cluster"]').click();
  await page.locator('[data-role="cluster-add"]').click();
  await page.locator('[data-role="choice-ssh"]').click();
  await expect(page.locator('[data-role="wizard-step"][data-step-id="s1"]')).toBeVisible();
}

async function gotoStepS2(page) {
  await page.locator('#ssh_host').fill('10.0.0.5');
  await page.locator('.modal-foot [data-role="primary"]').click();
  await expect(page.locator('[data-role="wizard-step"][data-step-id="s2"]')).toBeVisible();
}

async function fullSpawn(page, host, user, pwd) {
  await page.locator('#ssh_host').fill(host);
  await page.locator('.modal-foot [data-role="primary"]').click();
  await page.locator('#ssh_user').fill(user);
  await page.locator('#ssh_password').fill(pwd);
  await page.locator('.modal-foot [data-role="primary"]').click();
  await expect(page.locator('[data-role="wizard-step"][data-step-id="s3"]')).toBeVisible();
  await page.locator('.modal-foot [data-role="primary"]').click();
  await expect(page.locator('[data-role="wizard-step"][data-step-id="s4"]')).toBeVisible();
  await page.locator('.modal-foot [data-role="primary"]').click();
}

test.describe('SSH spawn error guidance', () => {
  test('inline hints render on Steps S1 and S2', async ({ page }) => {
    await openSSHWizard(page);
    // Step S1 hints (host + port + reachability copy).
    const s1 = page.locator('[data-role="wizard-step"][data-step-id="s1"]');
    const s1Text = await s1.textContent();
    expect(s1Text).toMatch(/Default 22/i);
    expect(s1Text).toMatch(/Public IP|private IP|DNS name/i);
    // Step S2 carries the user-hint with the cloud defaults list.
    await gotoStepS2(page);
    const userHint = page.locator('[data-role="user-hint"]');
    await expect(userHint).toContainText(/ec2-user/i);
    await expect(userHint).toContainText(/ubuntu/i);
  });

  test('Common SSH gotchas reference is visible in the wizard footer', async ({ page }) => {
    await openSSHWizard(page);
    const gotchas = page.locator('.cluster-ssh-gotchas');
    await expect(gotchas).toBeVisible();
    await gotchas.locator('summary').click();
    await expect(gotchas).toContainText(/Connection refused.*systemctl status sshd/);
    await expect(gotchas).toContainText(/permission denied.*publickey/);
    await expect(gotchas).toContainText(/Don't have an SSH server/i);
  });

  test('typing an AWS-looking host updates the username hint', async ({ page }) => {
    await openSSHWizard(page);
    await page.locator('#ssh_host').fill('ec2-1-2-3-4.compute-1.amazonaws.com');
    // The hint is hidden on S1 but exists; advance to S2 to see it
    // applied. Smart-username hint persists across navigation.
    await page.locator('.modal-foot [data-role="primary"]').click();
    const userHint = page.locator('[data-role="user-hint"]');
    await expect(userHint).toContainText(/AWS detected/i);
    await expect(userHint).toContainText(/ec2-user/i);
  });

  test('typing an Azure host updates the username hint to azureuser', async ({ page }) => {
    await openSSHWizard(page);
    await page.locator('#ssh_host').fill('myvm.eastus.cloudapp.azure.com');
    await page.locator('.modal-foot [data-role="primary"]').click();
    const userHint = page.locator('[data-role="user-hint"]');
    await expect(userHint).toContainText(/Azure detected/i);
    await expect(userHint).toContainText(/azureuser/i);
  });

  test('a "connection refused" spawn failure renders a structured Try-this card', async ({ page }) => {
    await page.route('**/api/worker/spawn', (route) => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        ok: false,
        error: 'ssh dial 127.0.0.1:22: dial tcp 127.0.0.1:22: connect: connection refused',
        log: ['Dialing SSH 127.0.0.1:22 as ec2-user'],
      }),
    }));
    await openSSHWizard(page);
    await fullSpawn(page, '127.0.0.1', 'ec2-user', 'p');
    const card = page.locator('.cluster-ssh-error-card');
    await expect(card).toBeVisible({ timeout: 8000 });
    await expect(card.locator('.cluster-ssh-error-title')).toContainText(/Connection refused.*SSH not listening/i);
    await expect(card.locator('.cluster-ssh-error-fix')).toContainText(/systemctl/);
    await expect(card.locator('.cluster-ssh-error-raw-disclosure')).toContainText(/Raw error/i);
  });

  test('a "permission denied (publickey)" failure routes to an auth Try-this card', async ({ page }) => {
    await page.route('**/api/worker/spawn', (route) => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        ok: false,
        error: 'ssh dial 10.0.0.5:22: ssh: handshake failed: ssh: unable to authenticate, attempted methods [none publickey], no supported methods remain',
        log: ['Dialing SSH 10.0.0.5:22 as wrong-user'],
      }),
    }));
    await openSSHWizard(page);
    await fullSpawn(page, '10.0.0.5', 'wrong-user', 'p');
    const card = page.locator('.cluster-ssh-error-card');
    await expect(card).toBeVisible({ timeout: 8000 });
    await expect(card.locator('.cluster-ssh-error-title')).toContainText(/Authentication failed/i);
    await expect(card.locator('.cluster-ssh-error-fix')).toContainText(/ec2-user/);
    await expect(card.locator('.cluster-ssh-error-fix')).toContainText(/private key/i);
  });

  test('preflight verdict + decoded card render for "BLOCKED — install path not writable"', async ({ page }) => {
    await page.route('**/api/worker/preflight', (route) => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        ok: false,
        reachable: true,
        arch: 'linux-amd64',
        can_write: false,
        has_curl: true,
        has_unzip: true,
        whoami: 'webuser',
        log: [
          'Dialing SSH 10.0.0.5:22 as webuser',
          '✓ ssh dial + auth ok',
          '✗ install path NOT writable: /tmp',
        ],
      }),
    }));
    await openSSHWizard(page);
    await page.locator('#ssh_host').fill('10.0.0.5');
    await page.locator('.modal-foot [data-role="primary"]').click();
    await page.locator('#ssh_user').fill('webuser');
    await page.locator('#ssh_password').fill('p');
    await page.locator('.modal-foot [data-role="primary"]').click();
    await expect(page.locator('[data-role="wizard-step"][data-step-id="s3"]')).toBeVisible();
    await page.locator('[data-role="step-test-prereq"]').click();
    const log = page.locator('[data-role="preflight-log"]');
    await expect(log).toBeVisible();
    const card = log.locator('.cluster-ssh-error-card');
    await expect(card).toBeVisible({ timeout: 5000 });
    await expect(card.locator('.cluster-ssh-error-title')).toContainText(/Install path is not writable/i);
    await expect(card.locator('.cluster-ssh-error-fix')).toContainText(/different path/i);
  });
});
