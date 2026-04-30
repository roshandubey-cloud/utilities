// Trusted hosts panel — list, forget. Adding hosts requires a real
// TOFU probe; we cover that via the run-flow test (which adds the mock
// to the trust store via the Start preflight).

import { test, expect } from '@playwright/test';

test('Trust view renders the panel with empty state on a fresh boot', async ({ page }) => {
  await page.goto('/');
  await page.locator('[data-action="view"][data-view="trust"]').click();
  const root = page.locator('[data-component="trusted-hosts"]');
  await expect(root).toBeVisible();
  // Empty state OR file-mode notice — both are acceptable greetings.
  await expect(root).toContainText(/(no trusted hosts|managed externally)/i);
});

test('Trust nav row is always reachable from the sidebar', async ({ page }) => {
  // Regression-class: the previous design hid the trusted-hosts panel
  // behind a wizard step. Now it's a top-level sidebar nav entry that
  // stays visible across every other view.
  await page.goto('/');
  for (const v of ['workbench', 'configure', 'runs', 'cluster']) {
    await page.locator(`[data-action="view"][data-view="${v}"]`).click();
    await expect(page.locator('[data-action="view"][data-view="trust"]')).toBeVisible();
  }
});
