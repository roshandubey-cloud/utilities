// Layout — every panel the user expects on the landing screen must
// be present and labelled. These are the "first-glance" sanity checks:
// wrong heading, missing affordance, swapped order all surface here.

import { test, expect } from '@playwright/test';

test.beforeEach(async ({ page }) => {
  await page.goto('/');
});

test('masthead carries product name + nav', async ({ page }) => {
  // The shell topbar carries the product brand. Must read as "SFTP Load Test".
  const brand = page.locator('[data-role="brand"]');
  await expect(brand).toBeVisible();
  await expect(brand).toContainText(/sftp\s*load\s*test/i);
});

test('quick checks panel visible in Configure view', async ({ page }) => {
  await page.locator('[data-action="view"][data-view="configure"]').click();
  await expect(page.locator('[data-component="connection"]')).toBeVisible();
  await expect(page.locator('#conn-host')).toBeVisible();
  await expect(page.locator('#conn-port')).toBeVisible();
  await expect(page.locator('#conn-folder')).toBeVisible();
});

test('topbar Run button is the primary call to action', async ({ page }) => {
  // The legacy hero-run "Run a load test" panel is now suppressed by the
  // shell — the topbar carries the primary Run / Stop. We assert the
  // topbar variant is reachable and rendered as the primary tone.
  const tbRun = page.locator('[data-role="topbar-run"]');
  await expect(tbRun).toBeVisible();
  await expect(tbRun).toHaveAttribute('data-variant', 'primary');
});

test('records / live activity panel exists in Workbench view', async ({ page }) => {
  await page.locator('[data-action="view"][data-view="workbench"]').click();
  await expect(page.locator('[data-component="records"]')).toBeVisible();
});

test('runs-history panel renders in History view', async ({ page }) => {
  await page.locator('[data-action="view"][data-view="history"]').click();
  await expect(page.locator('[data-component="runs-history"]')).toBeVisible();
});

test('trusted-hosts panel renders the list (or empty state) in Trust view', async ({ page }) => {
  await page.locator('[data-action="view"][data-view="trust"]').click();
  await expect(page.locator('[data-component="trusted-hosts"]')).toBeVisible();
  const root = page.locator('[data-component="trusted-hosts"]');
  await expect(root).toContainText(/(trusted|no trusted hosts|managed externally)/i);
});

test('legacy run config card is attached (host/port/folder source-of-truth fields)', async ({ page }) => {
  // The legacy form is the runner's input; visual layout has moved
  // through the Configure view but the IDs remain the source of truth.
  await expect(page.locator('#host')).toBeAttached();
  await expect(page.locator('#port')).toBeAttached();
  await expect(page.locator('#folder')).toBeAttached();
});

test('start button has a clear, non-cosmetic label', async ({ page }) => {
  const btn = page.locator('#startBtn');
  await expect(btn).toBeVisible();
  const text = (await btn.innerText()).trim();
  // Anti-regression: button text used to flicker through cosmetic
  // variants. Must match a deliberate Start-Run phrasing.
  expect(text.length).toBeGreaterThan(0);
  expect(/start/i.test(text)).toBe(true);
});

test('top-level page has no obvious console errors at load', async ({ page }) => {
  const errs = [];
  page.on('pageerror', (e) => errs.push(`pageerror: ${e.message}`));
  page.on('console', (m) => {
    if (m.type() === 'error') errs.push(`console.error: ${m.text()}`);
  });
  await page.goto('/');
  // Give the deferred mounts (legacy, wizard, ceiling-banner) a chance
  // to attach; any thrown promise / unbound variable shows up here.
  await page.waitForLoadState('networkidle');
  expect(errs, errs.join('\n')).toEqual([]);
});
