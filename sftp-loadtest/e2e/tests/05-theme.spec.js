// Theme — auto / light / dark toggle. Verifies the buttons exist,
// switching produces a visible color change, and the choice persists.

import { test, expect } from '@playwright/test';

test.describe('theme toggle', () => {
  test('three theme buttons exist', async ({ page }) => {
    await page.goto('/');
    // The buttons live inside the masthead; the theme group is labelled
    // "Theme" via aria-label.
    const group = page.locator('[role="group"][aria-label*="Theme" i], .theme-toggle, [data-component="masthead"]');
    await expect(group).toBeVisible();
    for (const label of ['Auto', 'Light', 'Dark']) {
      await expect(page.getByRole('button', { name: label })).toBeVisible();
    }
  });

  test('switching to dark applies a dark color scheme', async ({ page }) => {
    await page.goto('/');
    await page.getByRole('button', { name: 'Dark' }).click();
    // The theme module sets html.dataset.theme or similar; assert that
    // the page background becomes a dark color.
    const isDark = await page.evaluate(() => {
      const bg = getComputedStyle(document.body).backgroundColor;
      // Parse rgb(r,g,b) and check brightness.
      const m = bg.match(/rgba?\((\d+),\s*(\d+),\s*(\d+)/);
      if (!m) return false;
      const [r, g, b] = m.slice(1).map(Number);
      const brightness = (r + g + b) / 3;
      return brightness < 80; // dark
    });
    expect(isDark).toBe(true);
  });

  test('theme choice persists across reload', async ({ page }) => {
    await page.goto('/');
    await page.getByRole('button', { name: 'Dark' }).click();
    await page.reload();
    const isDark = await page.evaluate(() => {
      const bg = getComputedStyle(document.body).backgroundColor;
      const m = bg.match(/rgba?\((\d+),\s*(\d+),\s*(\d+)/);
      if (!m) return false;
      const [r, g, b] = m.slice(1).map(Number);
      return (r + g + b) / 3 < 80;
    });
    expect(isDark, 'dark theme must survive reload').toBe(true);
  });
});
