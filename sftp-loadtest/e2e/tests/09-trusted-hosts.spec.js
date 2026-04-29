// Trusted hosts panel — list, forget. Adding hosts requires a real
// TOFU probe; we cover that via the run-flow test (which adds the mock
// to the trust store via the Start preflight).

import { test, expect } from '@playwright/test';

test('panel renders empty state on a fresh boot', async ({ page }) => {
  await page.goto('/');
  const root = page.locator('[data-component="trusted-hosts"]');
  await expect(root).toBeVisible();
  // Empty state OR file-mode notice — both are acceptable greetings.
  await expect(root).toContainText(/(no trusted hosts|managed externally)/i);
});

test('panel always visible regardless of wizard step', async ({ page }) => {
  // Regression: previously tagged data-step="review", causing the
  // panel to vanish during workload/schedule steps.
  await page.goto('/');
  const root = page.locator('[data-component="trusted-hosts"]');
  await expect(root).toBeVisible();
  // Click each wizard step (if the wizard is present) and confirm the
  // panel stays visible.
  const stepBtns = await page.locator('[data-component="wizard"] [data-step]').all();
  for (const btn of stepBtns) {
    await btn.click();
    await expect(root).toBeVisible();
  }
});
