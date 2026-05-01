// 48-worker-preflight-probe.spec.js — preflight verification +
// sidebar worker health LED. Updated for the v0.13.4 wizard.
//
// In the wizard, the "full preflight" lives on Step S3 ("How does the
// binary get installed?"). The button is [data-role="step-test-prereq"]
// and a hidden compat alias [data-role="ssh-preflight"] still exists so
// older specs continue to pass. Output goes into both
// [data-role="step-test-prereq-log"] and the legacy [data-role="preflight-log"].

import { test, expect, request as playwrightRequest } from '@playwright/test';

const CSRF = { 'X-Requested-With': 'sftp-loadtest' };

async function openWizardOnStepS3(page) {
  await page.goto('/');
  await page.locator('[data-action="view"][data-view="cluster"]').click();
  await page.locator('[data-role="cluster-add"]').click();
  await page.locator('[data-role="choice-ssh"]').click();
  await page.locator('#ssh_host').fill('10.0.0.5');
  await page.locator('.modal-foot [data-role="primary"]').click();
  await page.locator('#ssh_user').fill('ec2-user');
  await page.locator('#ssh_password').fill('s3cret');
  await page.locator('.modal-foot [data-role="primary"]').click();
  await expect(page.locator('[data-role="wizard-step"][data-step-id="s3"]')).toBeVisible();
}

test.describe('worker preflight + live probe', () => {
  test('Step S3 prereq button surfaces a preflight log + verdict', async ({ page }) => {
    await page.route('**/api/worker/preflight', (route) => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        ok: true,
        reachable: true,
        arch: 'linux-amd64',
        can_write: true,
        has_curl: true,
        has_unzip: true,
        whoami: 'ec2-user',
        hostname: 'ip-10-0-0-5',
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
    }));
    await openWizardOnStepS3(page);
    await page.locator('[data-role="step-test-prereq"]').click();
    const log = page.locator('[data-role="preflight-log"]');
    await expect(log).toBeVisible();
    await expect(log).toContainText('ssh dial + auth ok');
    await expect(log).toContainText('linux-amd64');
    const verdict = log.locator('.cluster-ssh-preflight-verdict.is-ok');
    await expect(verdict).toBeVisible();
    await expect(verdict).toContainText(/READY/);
  });

  test('Preflight surfaces a BLOCKED verdict when the install path is not writable', async ({ page }) => {
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
          '✗ install path NOT writable: /tmp (need a directory the operator\'s user can write)',
        ],
      }),
    }));
    await openWizardOnStepS3(page);
    await page.locator('[data-role="step-test-prereq"]').click();
    const log = page.locator('[data-role="preflight-log"]');
    await expect(log).toBeVisible();
    const verdict = log.locator('.cluster-ssh-preflight-verdict.is-warn');
    await expect(verdict).toBeVisible();
    await expect(verdict).toContainText(/BLOCKED.*not writable/i);
  });

  test('Sidebar worker LED flips to idle (green) when /api/worker/probe says ok', async ({ page }) => {
    await page.route('**/api/worker/probe', (route) => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        ok: true,
        url: 'http://10.0.0.5:8080',
        status: 200,
        active: false,
        latency_ms: 12,
      }),
    }));
    await page.goto('/');
    await page.evaluate(() => {
      try {
        localStorage.setItem('sftp-loadtest-workers-v1', JSON.stringify([
          { id: 'wk-1', url: 'http://10.0.0.5:8080', auth_user: '', auth_pass: '', enabled: true, addedAt: new Date().toISOString() },
        ]));
      } catch {}
    });
    await page.reload();
    const row = page.locator('[data-role="sidebar-workers"] .shell-sidebar-row[data-id="wk-1"]');
    await expect(row).toBeVisible();
    await expect.poll(async () => row.getAttribute('data-health'), { timeout: 5000 })
      .toBe('idle');
  });

  test('Sidebar worker LED flips to down (red) when probe fails', async ({ page }) => {
    await page.route('**/api/worker/probe', (route) => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        ok: false,
        url: 'http://10.0.0.99:8080',
        status: 0,
        latency_ms: 5000,
        error: 'connection refused',
      }),
    }));
    await page.goto('/');
    await page.evaluate(() => {
      try {
        localStorage.setItem('sftp-loadtest-workers-v1', JSON.stringify([
          { id: 'wk-down', url: 'http://10.0.0.99:8080', auth_user: '', auth_pass: '', enabled: true, addedAt: new Date().toISOString() },
        ]));
      } catch {}
    });
    await page.reload();
    const row = page.locator('[data-role="sidebar-workers"] .shell-sidebar-row[data-id="wk-down"]');
    await expect(row).toBeVisible();
    await expect.poll(async () => row.getAttribute('data-health'), { timeout: 5000 })
      .toBe('down');
  });

  test('/api/worker/probe rejects an empty url and returns a friendly error for unreachable hosts', async ({ baseURL }) => {
    const ctx = await playwrightRequest.newContext({
      baseURL, extraHTTPHeaders: CSRF,
    });
    const bad = await ctx.post('/api/worker/probe', { data: { url: '' } });
    expect(bad.status()).toBe(400);
    const r = await ctx.post('/api/worker/probe', { data: { url: 'http://127.0.0.1:1' } });
    expect(r.ok()).toBe(true);
    const j = await r.json();
    expect(j.ok).toBe(false);
    expect(typeof j.error).toBe('string');
    expect(j.error.length).toBeGreaterThan(0);
    await ctx.dispose();
  });
});
