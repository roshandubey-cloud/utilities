// Narrow viewport — operators on smaller windows, split-screen,
// or running the app in a sidecar should still be able to reach
// every primary control. We don't promise mobile, but we don't
// want a 1024px window to lose the Start button or have horizontal
// scrolling that hides content.

import { test, expect } from '@playwright/test';

test.use({ viewport: { width: 1024, height: 768 } });

test('start button still visible at 1024×768', async ({ page }) => {
  await page.goto('/');
  const btn = page.locator('#startBtn');
  await expect(btn).toBeVisible();
  // No horizontal scroll: the page width should not exceed the viewport.
  const overflow = await page.evaluate(() => ({
    docWidth: document.documentElement.scrollWidth,
    viewportWidth: window.innerWidth,
  }));
  expect(overflow.docWidth, `page overflows viewport: doc=${overflow.docWidth} vp=${overflow.viewportWidth}`).toBeLessThanOrEqual(overflow.viewportWidth + 1);
});

test('quick checks form remains reachable at narrow width', async ({ page }) => {
  await page.goto('/');
  const host = page.locator('#conn-host');
  await expect(host).toBeVisible();
  await host.fill('127.0.0.1');
  await expect(host).toHaveValue('127.0.0.1');
});

test.describe('very narrow', () => {
  test.use({ viewport: { width: 800, height: 600 } });

  test('layout still functional at 800px wide', async ({ page }) => {
    await page.goto('/');
    // Hard requirement: the page must not have horizontal overflow even
    // at the lowest target width.
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    expect(overflow, 'horizontal overflow at 800px').toBeLessThanOrEqual(2);
  });
});
