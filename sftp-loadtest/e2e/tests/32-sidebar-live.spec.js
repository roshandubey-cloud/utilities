// Sidebar live data (α5) — Connections / Saved configs / Recent runs /
// Trusted hosts populate from real sources.

import { test, expect } from '@playwright/test';

test('sidebar sections render with empty states on a fresh boot', async ({ page }) => {
  await page.goto('/');
  const sb = page.locator('.shell-sidebar');
  await expect(sb).toBeVisible();
  // Primary nav rows.
  await expect(sb).toContainText(/workbench/i);
  await expect(sb).toContainText(/configure/i);
  await expect(sb).toContainText(/runs/i);
  await expect(sb).toContainText(/cluster/i);
  await expect(sb).toContainText(/trust/i);
  // Library sections.
  await expect(sb).toContainText(/connections/i);
  await expect(sb).toContainText(/saved configs/i);
  await expect(sb).toContainText(/recent runs/i);
  await expect(sb).toContainText(/workers/i);
});

test('saving a preset adds a row in Saved configs', async ({ page }) => {
  await page.goto('/');
  await page.evaluate(() => { try { localStorage.removeItem('sftp-loadtest-saved-configs-v1'); } catch {} });
  await page.reload();

  await page.locator('#conn-host').fill('preset-target.example');
  await page.keyboard.press('Meta+k');
  await page.locator('.cmdk-input').fill('save current config');
  await page.keyboard.press('Enter');
  // The Save-preset path now uses the in-DOM modal (cross-Wails compat),
  // not window.prompt. Type the name + submit.
  const modal = page.locator('.modal-panel');
  await expect(modal).toBeVisible();
  await modal.locator('input[name="name"]').fill('sidebar-test');
  await modal.locator('[data-role="primary"]').click();

  const slot = page.locator('[data-role="sidebar-configs"]');
  await expect(slot).toContainText('sidebar-test', { timeout: 5000 });
});

test('clicking a connection chip in the sidebar fills Quick Checks', async ({ page }) => {
  await page.goto('/');
  await page.evaluate(() => {
    try {
      localStorage.setItem('sftp-loadtest-conn-history-v1',
        JSON.stringify([{ host: 'sidebar-host.example', port: 2244 }]));
    } catch {}
  });
  await page.reload();
  const slot = page.locator('[data-role="sidebar-connections"]');
  // Wait for the heartbeat to render the row.
  await expect(slot).toContainText('sidebar-host.example', { timeout: 5000 });
  await slot.locator('[data-action="conn"]').first().click();
  await expect(page.locator('#conn-host')).toHaveValue('sidebar-host.example');
  await expect(page.locator('#conn-port')).toHaveValue('2244');
});

test('sidebar collapse hides labels but keeps icons', async ({ page }) => {
  await page.goto('/');
  const shell = page.locator('.app-shell');
  await expect(shell).toHaveAttribute('data-sidebar', 'open');
  await page.locator('[data-role="sidebar-toggle"]').click();
  await expect(shell).toHaveAttribute('data-sidebar', 'collapsed');
  // Section headers in collapsed mode are hidden.
  const header = page.locator('.shell-sidebar-section-header').first();
  await expect(header).toBeHidden();
});
