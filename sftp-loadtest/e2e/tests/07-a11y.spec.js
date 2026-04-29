// Accessibility — basic-but-strict checks. Doesn't run a full WCAG
// audit (no axe-core dep yet) but enforces the cheap-to-fix things
// we'd be embarrassed to ship: every form input must have a label,
// every interactive button must have either a text or aria-label,
// every focusable element must be reachable via keyboard.

import { test, expect } from '@playwright/test';

test('every visible form input has an associated label', async ({ page }) => {
  await page.goto('/');
  // Open the download card so its inputs are visible too.
  const dl = page.locator('#download_enabled');
  if (!await dl.isChecked()) await dl.check();

  const issues = await page.evaluate(() => {
    const issues = [];
    document.querySelectorAll('input, select, textarea').forEach((el) => {
      // Hidden / off-screen inputs (legacy connCard, etc.) don't need
      // labels — they're not user-facing.
      const r = el.getBoundingClientRect();
      if (r.width === 0 && r.height === 0) return;
      if (getComputedStyle(el).display === 'none') return;

      // Submit buttons / hidden / radio in a fieldset are OK without an
      // explicit <label>.
      if (el.type === 'hidden') return;

      // Has aria-label?
      if (el.getAttribute('aria-label')) return;
      if (el.getAttribute('aria-labelledby')) return;

      // Wrapped in a <label>?
      let p = el.parentElement;
      while (p) {
        if (p.tagName === 'LABEL') return;
        p = p.parentElement;
      }

      // Has a <label for="id">?
      if (el.id) {
        const lbl = document.querySelector(`label[for="${el.id}"]`);
        if (lbl) return;
      }

      issues.push({
        tag: el.tagName,
        type: el.type,
        id: el.id,
        name: el.name,
        placeholder: el.placeholder,
      });
    });
    return issues;
  });
  expect(issues, `inputs without a label: ${JSON.stringify(issues, null, 2)}`).toEqual([]);
});

test('every visible button has accessible text', async ({ page }) => {
  await page.goto('/');
  const issues = await page.evaluate(() => {
    const issues = [];
    document.querySelectorAll('button, [role="button"]').forEach((el) => {
      const r = el.getBoundingClientRect();
      if (r.width === 0 && r.height === 0) return;
      if (getComputedStyle(el).display === 'none') return;
      if (getComputedStyle(el).visibility === 'hidden') return;

      const txt = (el.innerText || '').trim();
      const aria = el.getAttribute('aria-label');
      const title = el.getAttribute('title');
      if (txt || aria || title) return;
      issues.push({ tag: el.tagName, id: el.id, classes: el.className });
    });
    return issues;
  });
  expect(issues, `buttons without text/aria-label/title: ${JSON.stringify(issues, null, 2)}`).toEqual([]);
});

test('start button is reachable via keyboard tab order', async ({ page }) => {
  await page.goto('/');
  // Tab through up to N elements until #startBtn is focused.
  let focused = '';
  for (let i = 0; i < 100; i++) {
    await page.keyboard.press('Tab');
    focused = await page.evaluate(() => (document.activeElement && document.activeElement.id) || '');
    if (focused === 'startBtn') break;
  }
  expect(focused, 'start button must be reachable via tab').toBe('startBtn');
});
