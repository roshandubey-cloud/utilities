// Live charts (α3) — throughput + latency sparklines mount above
// the records panel and accept live samples.

import { test, expect } from '@playwright/test';

test('throughput + latency chart panels render in Workbench view', async ({ page }) => {
  await page.goto('/');
  // Charts live in the Workbench view; default is Configure now.
  await page.locator('[data-action="view"][data-view="workbench"]').click();
  const charts = page.locator('[data-component="live-charts"]');
  await expect(charts).toBeVisible();
  // Two chart panels, each with a title and a KPI row.
  await expect(charts).toContainText(/throughput/i);
  await expect(charts).toContainText(/upload latency/i);
  // Throughput KPIs.
  await expect(charts).toContainText('now');
  await expect(charts).toContainText('peak');
  await expect(charts).toContainText('fpm');
  // Latency KPIs.
  await expect(charts).toContainText('p50');
  await expect(charts).toContainText('p95');
  await expect(charts).toContainText('p99');
});

test('chart SVGs have a path element ready to receive samples', async ({ page }) => {
  await page.goto('/');
  await page.locator('[data-action="view"][data-view="workbench"]').click();
  const lines = page.locator('[data-component="live-charts"] svg path[data-role="line"]');
  await expect(lines.first()).toBeAttached();
  await expect(lines.nth(1)).toBeAttached();
});
