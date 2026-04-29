// Command palette (α4) — Cmd+K opens, search filters, Enter fires.

import { test, expect } from '@playwright/test';

test('Cmd+K opens the palette', async ({ page }) => {
  await page.goto('/');
  await page.keyboard.press('Meta+k');
  await expect(page.locator('[data-component="command-palette"]')).toBeVisible();
  // Default empty query shows built-in commands.
  const palette = page.locator('[data-component="command-palette"]');
  await expect(palette).toContainText(/start run/i);
  await expect(palette).toContainText(/test connection/i);
  await expect(palette).toContainText(/theme/i);
});

test('Esc closes the palette', async ({ page }) => {
  await page.goto('/');
  await page.keyboard.press('Meta+k');
  await expect(page.locator('[data-component="command-palette"]')).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(page.locator('[data-component="command-palette"]')).toHaveCount(0);
});

test('typing filters the result list', async ({ page }) => {
  await page.goto('/');
  await page.keyboard.press('Meta+k');
  const input = page.locator('.cmdk-input');
  await input.fill('theme');
  // Result should narrow to theme commands.
  const results = page.locator('.cmdk-result');
  const count = await results.count();
  expect(count).toBeGreaterThan(0);
  for (let i = 0; i < count; i++) {
    const text = (await results.nth(i).innerText()).toLowerCase();
    expect(text).toContain('theme');
  }
});

test('arrow + enter selects and fires', async ({ page }) => {
  await page.goto('/');
  await page.keyboard.press('Meta+k');
  await page.locator('.cmdk-input').fill('theme → dark');
  await page.keyboard.press('Enter');
  // After firing, palette closes and dark theme applies.
  await expect(page.locator('[data-component="command-palette"]')).toHaveCount(0);
  await page.waitForFunction(() => document.documentElement.dataset.theme === 'dark');
});

test('save preset → load preset round-trip via palette', async ({ page }) => {
  await page.goto('/');
  // Configure something distinctive.
  await page.locator('#conn-host').fill('preset-host.example');
  await page.locator('#fpm').fill('999');

  // Save preset via palette.
  await page.keyboard.press('Meta+k');
  await page.locator('.cmdk-input').fill('save current config');
  page.once('dialog', async (d) => {
    expect(d.type()).toBe('prompt');
    await d.accept('e2e-preset');
  });
  await page.keyboard.press('Enter');

  // Reload to clear the form, then restore via palette.
  await page.reload();
  await page.evaluate(() => {
    document.getElementById('conn-host').value = '';
    document.getElementById('host').value = '';
    document.getElementById('fpm').value = '';
  });
  await page.keyboard.press('Meta+k');
  await page.locator('.cmdk-input').fill('load preset → e2e-preset');
  await page.keyboard.press('Enter');

  // The legacy host field is the source of truth for /api/start.
  await page.waitForFunction(() => document.getElementById('host').value === 'preset-host.example');
  expect(await page.locator('#host').inputValue()).toBe('preset-host.example');
  expect(await page.locator('#fpm').inputValue()).toBe('999');
});
