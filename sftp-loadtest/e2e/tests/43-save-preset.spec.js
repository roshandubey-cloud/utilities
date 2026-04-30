// 43-save-preset.spec.js — saving a workload config as a named
// preset directly from the Configure view. Previously the only path
// was Cmd+K → "Save current config…", which most operators never
// discovered. New affordance: a "Save preset…" pill in the Configure
// prelude (next to Import config). Uses the in-DOM modal so it works
// in Wails desktop where window.prompt is blocked.

import { test, expect } from '@playwright/test';

const PRESETS_KEY = 'sftp-loadtest-saved-configs-v1';

test.beforeEach(async ({ page }) => {
  await page.goto('/');
  await page.evaluate((k) => localStorage.removeItem(k), PRESETS_KEY);
  await page.locator('[data-action="view"][data-view="configure"]').click();
  await page.waitForTimeout(300);
});

test('Save preset… is reachable in the Configure prelude', async ({ page }) => {
  const btn = page.locator('.cfg-prelude [data-role="save-preset"]');
  await expect(btn).toBeVisible();
  await expect(btn).toContainText(/save preset/i);
});

test('clicking Save preset… persists the form to localStorage and updates the sidebar', async ({ page }) => {
  // Edit something on the form so the saved config is non-trivial.
  await page.evaluate(() => {
    const set = (id, val) => {
      const el = document.getElementById(id);
      if (!el) return;
      el.value = String(val);
      el.dispatchEvent(new Event('input', { bubbles: true }));
      el.dispatchEvent(new Event('change', { bubbles: true }));
    };
    set('host', 'sftp.acme.test');
    set('port', '2222');
    set('fpm', '120');
  });
  await page.locator('.cfg-prelude [data-role="save-preset"]').click();
  const modal = page.locator('.modal-panel');
  await expect(modal).toBeVisible();
  await modal.locator('input[name="name"]').fill('soak-prod');
  await modal.locator('[data-role="primary"]').click();
  // Storage carries it.
  const stored = await page.evaluate((k) => JSON.parse(localStorage.getItem(k) || '[]'), PRESETS_KEY);
  expect(stored).toHaveLength(1);
  expect(stored[0].name).toBe('soak-prod');
  expect(stored[0].config.host).toBe('sftp.acme.test');
  expect(stored[0].config.files_per_minute).toBe(120);
  // Sidebar Saved configs section reflects it without waiting for the
  // 3 s heartbeat (saved-configs fires a synthetic storage event).
  const sidebarRow = page.locator('[data-role="sidebar-configs"]').getByText('soak-prod', { exact: false });
  await expect(sidebarRow).toBeVisible({ timeout: 4000 });
});

test('saving with the same name overwrites the existing preset (no duplicates)', async ({ page }) => {
  await page.locator('.cfg-prelude [data-role="save-preset"]').click();
  await page.locator('.modal-panel input[name="name"]').fill('reused');
  await page.locator('.modal-panel [data-role="primary"]').click();
  // Edit the form, save again under the same name.
  await page.evaluate(() => {
    const e = document.getElementById('fpm');
    e.value = '600';
    e.dispatchEvent(new Event('change', { bubbles: true }));
  });
  await page.locator('.cfg-prelude [data-role="save-preset"]').click();
  await page.locator('.modal-panel input[name="name"]').fill('reused');
  await page.locator('.modal-panel [data-role="primary"]').click();
  const stored = await page.evaluate((k) => JSON.parse(localStorage.getItem(k) || '[]'), PRESETS_KEY);
  expect(stored).toHaveLength(1);
  expect(stored[0].config.files_per_minute).toBe(600);
});

test('Cancel closes the modal without saving anything', async ({ page }) => {
  await page.locator('.cfg-prelude [data-role="save-preset"]').click();
  await expect(page.locator('.modal-panel')).toBeVisible();
  await page.locator('.modal-panel [data-role="cancel"]').click();
  await expect(page.locator('.modal-panel')).toHaveCount(0);
  const stored = await page.evaluate((k) => JSON.parse(localStorage.getItem(k) || '[]'), PRESETS_KEY);
  expect(stored).toHaveLength(0);
});
