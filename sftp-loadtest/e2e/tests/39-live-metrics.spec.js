// 39-live-metrics.spec.js — the legacy "Live metrics" tile grid in
// Workbench (#m_elapsed, #m_files, #m_mb, #m_overall, etc.) MUST
// populate while a run is active. A previous regression silently
// removed elements that the legacy poll() depended on (h_net,
// sched_banner, p_cpu) — the silent catch in poll() then froze the
// tile updates at "—" / "0" while uploads ran. This spec drives a
// real run and asserts the tiles move past their initial values.

import { test, expect } from '@playwright/test';

test.describe('live metrics tiles update during an active run', () => {
  test.setTimeout(75_000);

  test('elapsed + files + overall MB/s reflect a live run', async ({ page }) => {
    await page.goto('/');
    await page.locator('[data-action="view"][data-view="configure"]').click();
    // The legacy host/port/folder inputs are hidden (their card is
    // suppressed by the shell). Set them straight via the DOM so the
    // legacy buildRequestBody reads the right values.
    await page.evaluate(() => {
      const set = (id, val) => {
        const el = document.getElementById(id);
        if (!el) return;
        el.value = String(val);
        el.dispatchEvent(new Event('input', { bubbles: true }));
        el.dispatchEvent(new Event('change', { bubbles: true }));
      };
      set('host', '127.0.0.1');
      set('port', '22020');
      set('folder', 'inbox');
      set('fpm', '60');
      set('nmin', '1');
      set('nmax', '1');
      set('duration', '0.01');
      set('poll', '1');
      set('timeout_min', '1');
    });
    const ta = page.locator('#normal_users');
    await ta.click();
    await ta.fill('u1,p1,probe*');
    await ta.blur();
    await page.locator('#startBtn').click();

    // Wait for tiles to leave their initial state. Initial: m_elapsed="—",
    // m_files="0", m_overall="0". After at least one poll tick during the
    // run, all three should change.
    const view = page.locator('.shell-main [data-view="workbench"]');
    await page.locator('[data-action="view"][data-view="workbench"]').click();
    const elapsed = view.locator('#m_elapsed');
    const files   = view.locator('#m_files');
    const overall = view.locator('#m_overall');
    await expect.poll(async () => (await elapsed.textContent()).trim(), { timeout: 30_000 })
      .not.toMatch(/^[—-]$/);
    // Files must end up >= 1.
    await expect.poll(async () => Number((await files.textContent()).trim()), { timeout: 30_000 })
      .toBeGreaterThan(0);
    // Overall MB/s must end up > 0.
    await expect.poll(async () => Number((await overall.textContent()).trim()), { timeout: 30_000 })
      .toBeGreaterThan(0);

    // The live_pill should have flipped from idle.
    const pill = view.locator('#live_pill');
    const txt = (await pill.textContent()).trim().toLowerCase();
    expect(['running', 'stopped']).toContain(txt);
  });
});
