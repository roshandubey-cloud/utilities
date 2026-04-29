// Cluster UI (β2) — sidebar Workers section + Distribute load toggle.

import { test, expect } from '@playwright/test';

test('Workers section renders empty state on a fresh boot', async ({ page }) => {
  await page.goto('/');
  await page.evaluate(() => { try { localStorage.removeItem('sftp-loadtest-workers-v1'); } catch {} });
  await page.reload();
  const sb = page.locator('.shell-sidebar');
  await expect(sb).toContainText(/workers/i);
  await expect(sb).toContainText(/add a sftp-loadtest url/i);
});

test('Distribute load toggle appears in the upload card', async ({ page }) => {
  await page.goto('/');
  await expect(page.locator('#cluster_distribute')).toBeVisible();
  await expect(page.locator('.cluster-distribute-status')).toContainText(/no workers enabled/i);
});

test('Adding a worker via localStorage shows it in the sidebar', async ({ page }) => {
  await page.goto('/');
  await page.evaluate(() => {
    try {
      localStorage.setItem('sftp-loadtest-workers-v1', JSON.stringify([
        { id: 'wk-test', url: 'http://10.0.0.5:8080', auth_user: '', auth_pass: '', enabled: true, addedAt: new Date().toISOString() },
      ]));
    } catch {}
  });
  await page.reload();
  // Wait for the heartbeat (3 s) to render.
  await expect(page.locator('[data-role="sidebar-workers"]')).toContainText('10.0.0.5:8080', { timeout: 5000 });
  await expect(page.locator('.cluster-distribute-status')).toContainText(/1 worker enabled/i, { timeout: 5000 });
});
