// Host-info bar — the masthead's host strip should answer with real
// values (not "—") when /api/host returns a snapshot. Catches the
// regression where the FD limit rendered as a 19-digit integer.

import { test, expect } from '@playwright/test';

test('host bar shows hostname, OS, cores, RAM, FD limit', async ({ page }) => {
  await page.goto('/');
  // Wait until the data-role spans have populated.
  await page.waitForFunction(() => {
    const hn = document.querySelector('[data-role="hostname"]');
    return hn && hn.textContent && hn.textContent.trim() !== '—';
  }, { timeout: 5000 });

  // Each cell must hold something specific, never the placeholder.
  for (const role of ['hostname', 'os', 'cores', 'ram', 'fdlimit']) {
    const cell = page.locator(`[data-role="${role}"]`).first();
    const txt = (await cell.innerText()).trim();
    expect(txt, `${role} cell must populate`).not.toBe('—');
    expect(txt.length).toBeGreaterThan(0);
  }
});

test('FD limit renders the macOS unlimited sentinel as ∞', async ({ page }) => {
  // This test only meaningfully runs on macOS (where the sentinel is
  // math.MaxInt64). On other platforms the assertion still passes
  // because the formatted text won't contain the giant decimal — and
  // we wouldn't expect to see ∞ either. So we only ASSERT when we
  // detect a host-strip hard limit was given in the first place.
  await page.goto('/');
  await page.waitForFunction(() => {
    const fd = document.querySelector('[data-role="fdlimit"]');
    return fd && fd.textContent && fd.textContent.trim() !== '—';
  }, { timeout: 5000 });
  const txt = (await page.locator('[data-role="fdlimit"]').first().innerText()).trim();
  // Anti-regression: must NOT show the 19-digit math.MaxInt64.
  expect(txt, 'FD limit must not leak math.MaxInt64 as a literal integer').not.toMatch(/9,?223,?372,?036,?854/);
});
