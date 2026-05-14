// 03 — REGRESSION GUARD for: "Probe details expand but I can't
// minimize them back to original size."
//
// The probe result block renders a <details class="probe-details">
// after each completed probe. Native <details> elements are
// supposed to be click-to-toggle on their <summary>, but the
// affordance was unclear (no chevron marker) so operators couldn't
// tell where to click. Fix: visible chevron + verified toggle.
import { test, expect } from '@playwright/test';
import { gotoConfigure, probeAndWait } from '../fixtures/server';

test('probe details disclosure is collapsible after expanding', async ({ page }) => {
  await gotoConfigure(page);
  await probeAndWait(page);

  const details = page.locator('[data-component="connection"] details[data-role="probe-details"]');
  await expect(details).toHaveCount(1, { timeout: 5_000 });

  // Initially closed.
  expect(await details.evaluate((el: HTMLDetailsElement) => el.open)).toBeFalsy();

  // Click the summary to expand.
  await details.locator('summary').first().click();
  expect(await details.evaluate((el: HTMLDetailsElement) => el.open)).toBeTruthy();

  // Click the summary again to collapse — this is the bit operators
  // reported broken.
  await details.locator('summary').first().click();
  expect(await details.evaluate((el: HTMLDetailsElement) => el.open),
    'second click on summary must collapse the disclosure').toBeFalsy();
});

test('probe details summary has a visible chevron affordance', async ({ page }) => {
  await gotoConfigure(page);
  await probeAndWait(page);

  // The fix ships a CSS-rendered chevron via ::before on the
  // summary so the click target is glanceable. Read the computed
  // ::before content; if it's empty, the affordance is missing.
  const chevronContent = await page.evaluate(() => {
    const sum = document.querySelector(
      '[data-component="connection"] details[data-role="probe-details"] > summary'
    );
    if (!sum) return null;
    return window.getComputedStyle(sum, '::before').content;
  });
  expect(chevronContent, 'summary must have a visible ::before chevron').not.toBeNull();
  expect(chevronContent).not.toBe('none');
  expect(chevronContent).not.toBe('"none"');
  // Should be something printable; "▶" / "▾" / "›" all qualify.
  expect(chevronContent!.length).toBeGreaterThan(2);
});
