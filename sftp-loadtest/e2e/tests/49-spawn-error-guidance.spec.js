// 49-spawn-error-guidance.spec.js — situation-aware error guidance
// for SSH-bootstrapped workers. The Add Worker → SSH bootstrap tab now:
//
//   - decodes raw SSH / install error strings into a Try-this card
//     with a title, why-it-failed paragraph, and step-by-step fix list.
//   - shows inline hints under each field explaining what to type.
//   - has a "Common SSH gotchas" disclosure for general reference.
//   - hints at the likely username when the host looks like a known
//     cloud provider (AWS / Azure / GCP / Oracle / VPS).
//
// Operators no longer stare at "ssh dial 127.0.0.1:22: connect:
// connection refused" with no idea what to do — the UI explains it
// and lists the things to try.

import { test, expect } from '@playwright/test';

test.describe('SSH spawn error guidance', () => {
  async function openSSHTab(page) {
    await page.goto('/');
    await page.locator('[data-action="view"][data-view="cluster"]').click();
    await page.locator('[data-role="cluster-add"]').click();
    await page.locator('.modal-tab[data-tab="ssh"]').click();
  }

  test('inline hints render under host / port / user / password / key fields', async ({ page }) => {
    await openSSHTab(page);
    const panel = page.locator('.modal-tab-panel[data-tab-panel="ssh"]');
    // The SSH-bootstrap tab carries multiple .modal-field-hint blocks
    // (host / port / user / password / key). Prove at least one is
    // visible AND that the wording covers the common questions an
    // operator has when filling these in.
    const hints = panel.locator('.modal-field-hint');
    expect(await hints.count()).toBeGreaterThanOrEqual(4);
    // Default-port + reachability + key-paste guidance are the three
    // pieces of copy that turn a blank stare into a typed value.
    const tabText = await panel.textContent();
    expect(tabText).toMatch(/Default 22/i);
    expect(tabText).toMatch(/Public IP|private IP|DNS name/i);
    const userHint = panel.locator('[data-role="user-hint"]');
    await expect(userHint).toContainText(/ec2-user/i);
    await expect(userHint).toContainText(/ubuntu/i);
  });

  test('Common SSH gotchas reference is collapsible and lists actionable items', async ({ page }) => {
    await openSSHTab(page);
    const gotchas = page.locator('.cluster-ssh-gotchas');
    await expect(gotchas).toBeVisible();
    // Open the disclosure.
    await gotchas.locator('summary').click();
    await expect(gotchas).toContainText(/Connection refused.*systemctl status sshd/);
    await expect(gotchas).toContainText(/permission denied.*publickey/);
    await expect(gotchas).toContainText(/Don't have an SSH server/i);
  });

  test('typing an AWS-looking host updates the username hint', async ({ page }) => {
    await openSSHTab(page);
    const userHint = page.locator('[data-role="user-hint"]');
    await page.locator('#ssh_host').fill('ec2-1-2-3-4.compute-1.amazonaws.com');
    await expect(userHint).toContainText(/AWS detected/i);
    await expect(userHint).toContainText(/ec2-user/i);
  });

  test('typing an Azure host updates the username hint to azureuser', async ({ page }) => {
    await openSSHTab(page);
    const userHint = page.locator('[data-role="user-hint"]');
    await page.locator('#ssh_host').fill('myvm.eastus.cloudapp.azure.com');
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
    await openSSHTab(page);
    await page.locator('#ssh_host').fill('127.0.0.1');
    await page.locator('#ssh_user').fill('ec2-user');
    await page.locator('#ssh_password').fill('p');
    // Click the modal's primary button (Spawn worker, since we're on SSH tab).
    await page.locator('[data-role="primary"]').click();
    const card = page.locator('.cluster-ssh-error-card');
    await expect(card).toBeVisible({ timeout: 8000 });
    await expect(card.locator('.cluster-ssh-error-title')).toContainText(/Connection refused.*SSH not listening/i);
    await expect(card.locator('.cluster-ssh-error-fix')).toContainText(/systemctl/);
    // Raw error is preserved in a collapsible.
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
    await openSSHTab(page);
    await page.locator('#ssh_host').fill('10.0.0.5');
    await page.locator('#ssh_user').fill('wrong-user');
    await page.locator('#ssh_password').fill('p');
    await page.locator('[data-role="primary"]').click();
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
    await openSSHTab(page);
    await page.locator('#ssh_host').fill('10.0.0.5');
    await page.locator('#ssh_user').fill('webuser');
    await page.locator('#ssh_password').fill('p');
    await page.locator('[data-role="ssh-preflight"]').click();
    const log = page.locator('[data-role="preflight-log"]');
    await expect(log).toBeVisible();
    // Decoded card surfaces the writable-path fix.
    const card = log.locator('.cluster-ssh-error-card');
    await expect(card).toBeVisible({ timeout: 5000 });
    await expect(card.locator('.cluster-ssh-error-title')).toContainText(/Install path is not writable/i);
    await expect(card.locator('.cluster-ssh-error-fix')).toContainText(/different path/i);
  });
});
