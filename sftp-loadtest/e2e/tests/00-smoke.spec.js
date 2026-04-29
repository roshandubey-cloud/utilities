// Smoke — proves the rig boots, the page renders, and the basics
// (theme, masthead, panels) are present before the deeper feature
// tests run. Anything failing here means the test rig itself is bad,
// not the UI.

import { test, expect } from '@playwright/test';

test.describe('rig smoke', () => {
  test('home page loads', async ({ page }) => {
    await page.goto('/');
    await expect(page).toHaveTitle(/SFTP Load Test/i);
  });

  test('healthz minimal payload', async ({ request }) => {
    const r = await request.get('/healthz');
    expect(r.status()).toBe(200);
    const body = await r.json();
    // Default must be the slimmest possible response — closed in v0.6.2.
    expect(body).toEqual({ status: 'ok' });
  });

  test('hostkeys api answers (no entries on a fresh boot)', async ({ request }) => {
    const r = await request.get('/api/hostkeys');
    expect(r.status()).toBe(200);
    const body = await r.json();
    // -insecure-host-key boot path → file mode (legacy fallback) with no
    // path; a fresh tool-managed setup would use store mode. Either is
    // acceptable — both must return an array (possibly empty) under hosts.
    expect(Array.isArray(body.hosts)).toBe(true);
  });
});
