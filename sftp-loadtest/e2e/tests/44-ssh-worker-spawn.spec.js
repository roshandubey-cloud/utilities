// SSH-bootstrapped worker (v0.13.4 wizard) — UI flow.
//
// `/api/worker/spawn` is mocked because Playwright can't spin up a real
// SSH server here. The Go side has a real SSH-server-driven test that
// validates the wire protocol; this spec only validates the UI plumbing:
// wizard navigation, spawn-log rendering on Step S4, the addWorker →
// sidebar handoff, and the despawn-on-Forget contract.

import { test, expect } from '@playwright/test';

async function openSSHWizard(page) {
  await page.locator('.shell-sidebar [data-view="cluster"]').first().click();
  await expect(page.locator('.shell-main [data-view="cluster"] .cluster-view-panel')).toBeVisible();
  await page.locator('[data-role="cluster-add"]').click();
  await expect(page.locator('.modal-panel')).toBeVisible();
  await page.locator('[data-role="choice-ssh"]').click();
  await expect(page.locator('[data-role="wizard-step"][data-step-id="s1"]')).toBeVisible();
}

async function advance(page) {
  await page.locator('.modal-foot [data-role="primary"]').click();
}

test.beforeEach(async ({ page }) => {
  await page.goto('/');
  await page.evaluate(() => {
    try { localStorage.removeItem('sftp-loadtest-workers-v1'); } catch {}
  });
});

test('SSH wizard spawns a worker and shows the SSH badge', async ({ page }) => {
  await page.route('**/api/worker/spawn', async (route) => {
    await new Promise((r) => setTimeout(r, 200));
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        id: 'ssh-mock-1',
        url: 'http://127.0.0.1:54321',
        arch: 'linux-amd64',
        log: [
          'Dialing SSH 127.0.0.1:22 as test',
          'Detecting remote arch (uname -s -m)',
          'Detected arch: linux-amd64',
          'Reaping orphan workers (pkill)',
          'Downloading from https://github.com/...',
          'Installed at /tmp/sftp-loadtest',
          'Smoke test: /tmp/sftp-loadtest -version',
          'Spawning worker on 127.0.0.1:18081',
          'Waiting for worker to be ready on 127.0.0.1:18081',
          'Tunnel ready: http://127.0.0.1:54321 → 127.0.0.1:18081',
        ],
      }),
    });
  });

  await openSSHWizard(page);
  // Step S1.
  await page.locator('#ssh_host').fill('127.0.0.1');
  await advance(page);
  // Step S2.
  await expect(page.locator('[data-role="wizard-step"][data-step-id="s2"]')).toBeVisible();
  await page.locator('#ssh_user').fill('test');
  await page.locator('#ssh_password').fill('testpass');
  await advance(page);
  // Step S3.
  await expect(page.locator('[data-role="wizard-step"][data-step-id="s3"]')).toBeVisible();
  await advance(page);
  // Step S4.
  await expect(page.locator('[data-role="wizard-step"][data-step-id="s4"]')).toBeVisible();
  await advance(page);

  await expect(page.locator('.cluster-ssh-spawn-log')).toBeVisible();
  await expect(page.locator('.cluster-ssh-spawn-log')).toContainText(/Detecting arch/i);
  await expect(page.locator('.cluster-ssh-spawn-log')).toContainText(/Tunnel ready/i);

  await expect(page.locator('.modal-panel')).toBeHidden({ timeout: 5000 });

  const sidebar = page.locator('[data-role="sidebar-workers"]');
  await expect(sidebar).toContainText('127.0.0.1:54321', { timeout: 5000 });
  await expect(sidebar.locator('.cluster-ssh-badge')).toBeVisible();
});

test('Forget on an SSH worker POSTs /api/worker/despawn before drop', async ({ page }) => {
  await page.evaluate(() => {
    localStorage.setItem('sftp-loadtest-workers-v1', JSON.stringify([
      {
        id: 'wk-ssh', url: 'http://127.0.0.1:54321',
        auth_user: '', auth_pass: '',
        enabled: true, addedAt: new Date().toISOString(),
        source: 'ssh', spawn_id: 'ssh-mock-1',
      },
    ]));
  });

  let despawnSeen = null;
  await page.route('**/api/worker/despawn', async (route) => {
    despawnSeen = JSON.parse(route.request().postData() || '{}');
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ ok: true, id: despawnSeen.id }),
    });
  });

  await page.reload();
  await expect(page.locator('[data-role="sidebar-workers"]')).toContainText('127.0.0.1:54321', { timeout: 5000 });

  const delBtn = page.locator('[data-role="sidebar-workers"] [data-action="del"]').first();
  await delBtn.click();
  await page.locator('.modal-panel [data-role="primary"]').click();

  await expect.poll(() => despawnSeen, { timeout: 5000 }).not.toBeNull();
  expect(despawnSeen.id).toBe('ssh-mock-1');

  await expect(page.locator('[data-role="sidebar-workers"]')).toContainText(/add a sftp-loadtest url/i, { timeout: 5000 });
});

test('SSH wizard surfaces server errors in the spawn log on Step S4', async ({ page }) => {
  await page.route('**/api/worker/spawn', async (route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        ok: false,
        error: 'ssh dial 10.99.99.99:22: connection refused',
        log: ['Dialing SSH 10.99.99.99:22 as root'],
      }),
    });
  });

  await openSSHWizard(page);
  await page.locator('#ssh_host').fill('10.99.99.99');
  await advance(page);
  await page.locator('#ssh_user').fill('root');
  await page.locator('#ssh_password').fill('hunter2');
  await advance(page);
  await advance(page); // S3 → S4
  await advance(page); // Spawn

  // v0.13.3 structured error card stays in v0.13.4 — surfaces on S4.
  const card = page.locator('.cluster-ssh-spawn-log .cluster-ssh-error-card');
  await expect(card).toBeVisible();
  await expect(card).toContainText(/Connection refused.*SSH not listening/i);
  await expect(card.locator('.cluster-ssh-error-raw-disclosure')).toContainText(/Raw error/);
  await expect(page.locator('.modal-foot [data-role="primary"]')).toHaveText(/Retry/);
});
