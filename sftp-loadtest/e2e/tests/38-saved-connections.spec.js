// 38-saved-connections.spec.js — user-curated named connections.
//
// The Save… button on the Quick checks card lets the user persist
// host/port/username/password as a named entry in localStorage. The
// sidebar's Connections section shows the curated list above the
// auto-tracked recent host:port history; clicking a saved entry fills
// all four credential fields. Hover-x deletes after a confirm.

import { test, expect } from '@playwright/test';

const SAVED_KEY = 'sftp-loadtest-saved-conns-v1';

test.beforeEach(async ({ page }) => {
  await page.goto('/');
  await page.evaluate((k) => localStorage.removeItem(k), SAVED_KEY);
  await page.evaluate(() => localStorage.removeItem('sftp-loadtest-conn-history-v1'));
  await page.locator('[data-action="view"][data-view="configure"]').click();
  await page.waitForTimeout(300);
});

test('Save… is reachable on the Quick checks card', async ({ page }) => {
  const btn = page.locator('[data-component="connection"] [data-role="save-conn"]');
  await expect(btn).toBeVisible();
  await expect(btn).toContainText(/save/i);
});

test('saving a connection writes the localStorage entry and renders in the sidebar', async ({ page }) => {
  // Fill the connection card.
  await page.locator('#conn-host').fill('sftp.acme.test');
  await page.locator('#conn-port').fill('22');
  await page.locator('#conn-user').fill('alice');
  // Open the save modal (no password, so the modal asks only for a name).
  await page.locator('[data-component="connection"] [data-role="save-conn"]').click();
  const modal = page.locator('.modal-panel');
  await expect(modal).toBeVisible();
  await modal.locator('input[name="name"]').fill('acme-prod');
  await modal.locator('[data-role="primary"]').click();
  // Storage shape.
  const stored = await page.evaluate((k) => JSON.parse(localStorage.getItem(k) || '[]'), SAVED_KEY);
  expect(stored).toHaveLength(1);
  expect(stored[0].name).toBe('acme-prod');
  expect(stored[0].host).toBe('sftp.acme.test');
  expect(stored[0].port).toBe(22);
  expect(stored[0].username).toBe('alice');
  expect(stored[0].password).toBe('');
  expect(stored[0].has_password).toBe(false);
  // Sidebar reflects within the heartbeat window (3 s) — we trigger a
  // synthetic storage event in saveEntry() so the render is immediate.
  const sidebar = page.locator('[data-role="sidebar-connections"]');
  await expect(sidebar.locator('.shell-sidebar-row-saved')).toContainText('acme-prod', { timeout: 4000 });
});

test('opting in stores the password; opting out leaves it blank', async ({ page }) => {
  await page.locator('#conn-host').fill('sftp.acme.test');
  await page.locator('#conn-port').fill('2222');
  await page.locator('#conn-user').fill('bob');
  await page.locator('#conn-pass').fill('s3cret');
  await page.locator('[data-component="connection"] [data-role="save-conn"]').click();
  const modal = page.locator('.modal-panel');
  await modal.locator('input[name="name"]').fill('staging-with-pw');
  await modal.locator('input[name="savePassword"]').fill('yes');
  await modal.locator('[data-role="primary"]').click();
  const stored = await page.evaluate((k) => JSON.parse(localStorage.getItem(k) || '[]'), SAVED_KEY);
  expect(stored[0].has_password).toBe(true);
  expect(stored[0].password).toBe('s3cret');
});

test('clicking a saved entry refills the connection card with creds', async ({ page }) => {
  await page.evaluate((k) => localStorage.setItem(k, JSON.stringify([{
    id: 'c-test-1', name: 'preset-prod', host: 'prod.example', port: 22,
    username: 'carol', password: 'pw1', has_password: true, saved_at: new Date().toISOString(),
  }])), SAVED_KEY);
  await page.reload();
  await page.locator('[data-action="view"][data-view="configure"]').click();
  // Wait for sidebar to render the row.
  const row = page.locator('[data-role="sidebar-connections"] .shell-sidebar-row-saved').first();
  await expect(row).toBeVisible({ timeout: 5000 });
  // Clear current values first.
  await page.locator('#conn-host').fill('');
  await page.locator('#conn-port').fill('');
  await page.locator('#conn-user').fill('');
  await page.locator('#conn-pass').fill('');
  await row.click();
  await expect(page.locator('#conn-host')).toHaveValue('prod.example');
  await expect(page.locator('#conn-port')).toHaveValue('22');
  await expect(page.locator('#conn-user')).toHaveValue('carol');
  await expect(page.locator('#conn-pass')).toHaveValue('pw1');
});

test('the inline forget × removes the entry after confirmation', async ({ page }) => {
  await page.evaluate((k) => localStorage.setItem(k, JSON.stringify([{
    id: 'c-test-1', name: 'transient', host: 'x.test', port: 22,
    username: '', password: '', has_password: false, saved_at: new Date().toISOString(),
  }])), SAVED_KEY);
  await page.reload();
  await page.locator('[data-action="view"][data-view="configure"]').click();
  const row = page.locator('[data-role="sidebar-connections"] .shell-sidebar-row-saved').first();
  await expect(row).toBeVisible({ timeout: 5000 });
  // Hover to reveal × (the button is opacity:0 until hover).
  await row.hover();
  await row.locator('[data-role="forget"]').click();
  // Confirm dialog.
  const confirm = page.locator('.modal-panel');
  await expect(confirm).toBeVisible();
  await confirm.locator('[data-role="primary"]').click();
  const stored = await page.evaluate((k) => JSON.parse(localStorage.getItem(k) || '[]'), SAVED_KEY);
  expect(stored).toHaveLength(0);
});
