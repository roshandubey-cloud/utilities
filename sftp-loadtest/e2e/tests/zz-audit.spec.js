// Visual audit — take screenshots of EVERY view a real user clicks
// through. Catches what test assertions miss: cramped layouts,
// truncated text, broken column widths, ugly spacing.

import { test } from '@playwright/test';

test.describe('full visual audit', () => {
  test.use({ viewport: { width: 1400, height: 1000 } });

  test('workbench view (idle)', async ({ page }) => {
    await page.goto('/');
    await page.locator('[data-action="view"][data-view="workbench"]').click();
    await page.waitForTimeout(800);
    await page.screenshot({ path: 'playwright-report/audit-workbench.png', fullPage: true });
  });

  test('configure view', async ({ page }) => {
    await page.goto('/');
    await page.locator('[data-action="view"][data-view="configure"]').click();
    await page.waitForTimeout(800);
    await page.screenshot({ path: 'playwright-report/audit-configure.png', fullPage: true });
  });

  test('schedule view', async ({ page }) => {
    await page.goto('/');
    await page.locator('[data-action="view"][data-view="schedule"]').click();
    await page.waitForTimeout(800);
    await page.screenshot({ path: 'playwright-report/audit-schedule.png', fullPage: true });
  });

  test('runs view (plan + empty history)', async ({ page }) => {
    await page.goto('/');
    await page.locator('[data-action="view"][data-view="configure"]').click();
    await page.locator('#conn-host').fill('sftp.example.com');
    await page.locator('#conn-port').fill('22');
    await page.locator('#upload-folder').fill('inbox');
    await page.locator('#fpm').fill('600');
    const ta = page.locator('#normal_users');
    await ta.click();
    await ta.fill('user1,pass1,invoice*\nuser2,pass2,order*');
    await ta.blur();
    await page.locator('[data-action="view"][data-view="runs"]').click();
    await page.waitForTimeout(800);
    await page.screenshot({ path: 'playwright-report/audit-runs.png', fullPage: true });
  });

  test('cluster view (empty)', async ({ page }) => {
    await page.goto('/');
    await page.locator('[data-action="view"][data-view="cluster"]').click();
    await page.waitForTimeout(800);
    await page.screenshot({ path: 'playwright-report/audit-cluster.png', fullPage: true });
  });

  test('cluster view with workers', async ({ page }) => {
    await page.goto('/');
    await page.evaluate(() => {
      try {
        localStorage.setItem('sftp-loadtest-workers-v1', JSON.stringify([
          { id: 'wk-1', url: 'http://10.0.0.5:8080', auth_user: 'admin', auth_pass: '', enabled: true, addedAt: '2026-04-29T00:00:00Z' },
          { id: 'wk-2', url: 'http://10.0.0.6:8080', auth_user: '', auth_pass: '', enabled: false, addedAt: '2026-04-29T00:00:00Z' },
        ]));
      } catch {}
    });
    await page.reload();
    await page.locator('[data-action="view"][data-view="cluster"]').click();
    await page.waitForTimeout(800);
    await page.screenshot({ path: 'playwright-report/audit-cluster-with-workers.png', fullPage: true });
  });

  test('trust view', async ({ page }) => {
    await page.goto('/');
    await page.locator('[data-action="view"][data-view="trust"]').click();
    await page.waitForTimeout(800);
    await page.screenshot({ path: 'playwright-report/audit-trust.png', fullPage: true });
  });

  test('add worker modal opens', async ({ page }) => {
    await page.goto('/');
    await page.locator('[data-action="view"][data-view="cluster"]').click();
    await page.locator('[data-role="cluster-add"]').click();
    await page.waitForTimeout(500);
    await page.screenshot({ path: 'playwright-report/audit-add-worker-modal.png', fullPage: true });
  });

  test('add worker wizard — every step', async ({ page }) => {
    await page.goto('/');
    await page.locator('[data-action="view"][data-view="cluster"]').click();
    await page.locator('[data-role="cluster-add"]').click();
    await page.waitForTimeout(300);
    await page.screenshot({ path: 'playwright-report/audit-add-worker-step0-choice.png', fullPage: true });
    await page.locator('[data-role="choice-ssh"]').click();
    await page.waitForTimeout(200);
    await page.screenshot({ path: 'playwright-report/audit-add-worker-s1-where.png', fullPage: true });
    await page.locator('#ssh_host').fill('10.0.0.5');
    await page.locator('.modal-foot [data-role="primary"]').click();
    await page.waitForTimeout(200);
    await page.screenshot({ path: 'playwright-report/audit-add-worker-s2-who.png', fullPage: true });
    await page.locator('#ssh_user').fill('ec2-user');
    await page.locator('#ssh_password').fill('p');
    await page.locator('.modal-foot [data-role="primary"]').click();
    await page.waitForTimeout(200);
    await page.screenshot({ path: 'playwright-report/audit-add-worker-s3-how.png', fullPage: true });
    await page.locator('.modal-foot [data-role="primary"]').click();
    await page.waitForTimeout(200);
    await page.screenshot({ path: 'playwright-report/audit-add-worker-s4-install.png', fullPage: true });
  });
});
