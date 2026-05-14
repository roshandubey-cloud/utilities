// 01-boot-smoke — quickest possible signal that the server boots
// AND the SPA hydrates without JS errors. If this fails the whole
// suite is suspect.
import { test, expect } from '@playwright/test';
import { apiGet } from '../fixtures/server';

test('healthz responds 200', async ({ request }) => {
  const r = await apiGet(request, '/healthz');
  expect(r.ok()).toBeTruthy();
});

test('SPA mounts the shell + sidebar without console errors', async ({ page }) => {
  const errors: string[] = [];
  page.on('pageerror', (e) => errors.push(String(e)));
  page.on('console', (msg) => {
    if (msg.type() === 'error') errors.push(msg.text());
  });
  await page.goto('/');
  // Shell topbar (brand mark + status pill) is the first thing
  // mounted by shell.js. If it isn't there the SPA didn't hydrate.
  await page.waitForSelector('.shell-topbar', { state: 'visible' });
  await page.waitForSelector('.shell-sidebar', { state: 'visible' });
  expect(errors, `Unexpected JS errors:\n${errors.join('\n')}`).toEqual([]);
});
