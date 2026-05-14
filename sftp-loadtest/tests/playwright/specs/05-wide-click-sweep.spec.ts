// 05 — wide click sweep across every visible view. Promotes the
// throw-away /tmp/slt-click-validate.mjs script into a committed,
// CI-run test so a regression in any view's click wiring fails
// the build.
//
// For each VIEW we navigate, find every visible interactive
// surface (button, a[href], [role=button], summary, .seg-btn,
// sidebar rows), click it, and assert that SOMETHING observable
// changed (modal count, toast count, body HTML size, scroll,
// aria-selected/pressed fingerprints, theme attr, active view,
// sidebar/statusbar state, open <details> count). Anything that
// produces NO response is flagged as a "dead-end" click.
import { test, expect } from '@playwright/test';

const VIEWS = ['workbench', 'configure', 'schedule', 'runs', 'cluster', 'trust'] as const;
const SKIP_TEXTS = new Set([
  'run', 'stop', 'start run', 'start scheduled run',
  'download', 'download csv', 'download merged csv', 'aggregated json',
  'import config', 'import & run now',
  'upload key file', 'upload users csv', 'export config',
  'forget', 'delete', 'clear stored credentials',
  'add worker', '+ add worker',
]);
const shouldSkip = (text: string) => {
  const t = text.trim().toLowerCase();
  return [...SKIP_TEXTS].some((s) => t === s || t.startsWith(s + ' ') || t.endsWith(' ' + s));
};

test('every visible clickable surface produces an observable response', async ({ page }) => {
  const jsErrors: string[] = [];
  page.on('pageerror', (e) => jsErrors.push(String(e)));
  await page.goto('/', { waitUntil: 'networkidle' });
  await page.evaluate(() => document.documentElement.setAttribute('data-theme', 'dark'));

  const dead: { view: string; text: string }[] = [];
  let totalClicks = 0;

  for (const view of VIEWS) {
    await page.evaluate((v) => {
      const row = document.querySelector(`.shell-sidebar-row[data-view="${v}"]`);
      (row as HTMLElement | null)?.click();
    }, view);
    await page.waitForTimeout(400);

    const targets = await page.evaluate(() => {
      const sel = 'button:not([disabled]), a[href]:not([href=""]):not([href="#"]), [role="button"]:not([aria-disabled="true"]), summary, .seg-btn, .shell-sidebar-row[data-action]';
      const out: { text: string; role: string }[] = [];
      document.querySelectorAll(sel).forEach((el) => {
        const r = el.getBoundingClientRect();
        if (r.width === 0 || r.height === 0) return;
        if (r.bottom < 0 || r.top > innerHeight) return;
        const cs = getComputedStyle(el);
        if (cs.visibility === 'hidden' || cs.display === 'none' || cs.opacity === '0') return;
        const text = (el.textContent || '').trim().slice(0, 60);
        const aria = el.getAttribute('aria-label') || '';
        out.push({ text: text || aria, role: el.getAttribute('data-role') || '' });
      });
      return out.slice(0, 60);
    });

    for (const t of targets) {
      if (shouldSkip(t.text)) continue;

      const pre = await page.evaluate(() => ({
        modals: document.querySelectorAll('.modal-backdrop').length,
        toasts: document.querySelectorAll('.toast, [data-component="toast"]').length,
        bodyHTML: document.body.innerHTML.length,
        scrollY: window.scrollY,
        ariaSelectedFingerprint: [...document.querySelectorAll('[aria-selected]')].map((e) => e.getAttribute('aria-selected')).join('|'),
        ariaPressedFingerprint: [...document.querySelectorAll('[aria-pressed]')].map((e) => e.getAttribute('aria-pressed')).join('|'),
        theme: document.documentElement.getAttribute('data-theme') || '',
        activeView: document.body.dataset.activeView || '',
        sidebarState: (document.querySelector('.app-shell') as HTMLElement | null)?.dataset.sidebar || '',
        statusbarState: (document.querySelector('.app-shell') as HTMLElement | null)?.dataset.statusbar || '',
        openDetails: [...document.querySelectorAll('details[open]')].length,
      }));

      const clicked = await page.evaluate((spec: { text: string; role: string }) => {
        const sel = 'button:not([disabled]), a[href]:not([href=""]):not([href="#"]), [role="button"]:not([aria-disabled="true"]), summary, .seg-btn, .shell-sidebar-row[data-action]';
        for (const el of document.querySelectorAll(sel)) {
          const r = el.getBoundingClientRect();
          if (r.width === 0 || r.height === 0) continue;
          const text = (el.textContent || '').trim().slice(0, 60);
          const aria = el.getAttribute('aria-label') || '';
          const matchText = text || aria;
          if (matchText !== spec.text) continue;
          if (spec.role && el.getAttribute('data-role') !== spec.role) continue;
          try { (el as HTMLElement).click(); return true; } catch { return false; }
        }
        return false;
      }, t);
      if (!clicked) continue;
      totalClicks++;
      await page.waitForTimeout(150);

      const post = await page.evaluate(() => ({
        modals: document.querySelectorAll('.modal-backdrop').length,
        toasts: document.querySelectorAll('.toast, [data-component="toast"]').length,
        bodyHTML: document.body.innerHTML.length,
        scrollY: window.scrollY,
        ariaSelectedFingerprint: [...document.querySelectorAll('[aria-selected]')].map((e) => e.getAttribute('aria-selected')).join('|'),
        ariaPressedFingerprint: [...document.querySelectorAll('[aria-pressed]')].map((e) => e.getAttribute('aria-pressed')).join('|'),
        theme: document.documentElement.getAttribute('data-theme') || '',
        activeView: document.body.dataset.activeView || '',
        sidebarState: (document.querySelector('.app-shell') as HTMLElement | null)?.dataset.sidebar || '',
        statusbarState: (document.querySelector('.app-shell') as HTMLElement | null)?.dataset.statusbar || '',
        openDetails: [...document.querySelectorAll('details[open]')].length,
      }));

      const responded =
        post.modals !== pre.modals ||
        post.toasts !== pre.toasts ||
        Math.abs(post.bodyHTML - pre.bodyHTML) > 50 ||
        post.scrollY !== pre.scrollY ||
        post.ariaSelectedFingerprint !== pre.ariaSelectedFingerprint ||
        post.ariaPressedFingerprint !== pre.ariaPressedFingerprint ||
        post.theme !== pre.theme ||
        post.activeView !== pre.activeView ||
        post.sidebarState !== pre.sidebarState ||
        post.statusbarState !== pre.statusbarState ||
        post.openDetails !== pre.openDetails;

      if (post.modals > pre.modals) {
        await page.keyboard.press('Escape');
        await page.waitForTimeout(60);
      }
      if (!responded) dead.push({ view, text: t.text });
    }
  }

  expect(jsErrors, `Unexpected page errors:\n${jsErrors.join('\n')}`).toEqual([]);
  expect(dead,
    `Found ${dead.length} dead-end clicks: ${JSON.stringify(dead, null, 2)}`).toEqual([]);
  expect(totalClicks).toBeGreaterThan(40); // sanity floor — fewer than 40 means a view didn't render
});
