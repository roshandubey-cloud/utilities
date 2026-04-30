// zz-readme-screenshots.spec.js — re-bakes the screenshot set under
// docs/screenshots/ (referenced by README.md and docs/howto.md). Gated
// behind CAPTURE=1; ignored by the regular suite via playwright.config.js.
//
// Each captured view is taken in BOTH dark and light themes so the README
// can show the modern Apple-TV-class workbench in either appearance. The
// theme is forced via ?theme=dark|light (theme.js persists the choice in
// localStorage on init), then we wait briefly for the deferred mounts
// (cluster-ui, sidebar heartbeat) to settle.
//
// Usage:
//   CAPTURE=1 ./node_modules/.bin/playwright test \
//     tests/zz-readme-screenshots.spec.js --reporter=list

import { test } from '@playwright/test';
import { mkdirSync } from 'node:fs';
import { join } from 'node:path';

const OUT_DIR = join(import.meta.dirname, '..', '..', 'docs', 'screenshots');
mkdirSync(OUT_DIR, { recursive: true });

const THEMES = ['dark', 'light'];
const VIEWPORT = { width: 1400, height: 1000 };

async function gotoTheme(page, theme, view) {
  await page.goto(`/?theme=${theme}`);
  // Force the view explicitly — Configure is the default, the others
  // require a sidebar nav click.
  if (view) {
    await page.locator(`[data-action="view"][data-view="${view}"]`).click();
  }
  // Heartbeat-driven mounts (sidebar workers, cluster-ui distribute row)
  // settle in <800 ms; status poll lands at 2 s. Wait long enough that
  // both have rendered and the screenshot is stable.
  await page.waitForTimeout(900);
}

test.describe('readme screenshots', () => {
  test.use({ viewport: VIEWPORT });
  test.describe.configure({ mode: 'serial' });

  for (const theme of THEMES) {
    test(`workbench-idle (${theme})`, async ({ page }) => {
      await gotoTheme(page, theme, 'workbench');
      await page.screenshot({ path: join(OUT_DIR, `workbench-idle-${theme}.png`), fullPage: true });
    });

    test(`workbench-active (${theme})`, async ({ page }) => {
      // Drive a real upload long enough for the topbar status pill, slim
      // chips, and Live activity rows to be populated when the screenshot
      // fires. We screenshot WHILE the run is still active and stop only
      // after the capture lands so the next theme run starts clean.
      await gotoTheme(page, theme, 'configure');
      await page.locator('#conn-host').fill('127.0.0.1');
      await page.locator('#conn-port').fill('22020');
      await page.locator('#upload-folder').fill('inbox');
      await page.locator('#parallel').fill('2');
      // 0.05 h = 3 minutes — plenty of headroom; we stop manually.
      await page.locator('#duration').fill('0.05');
      await page.locator('#poll').fill('1');
      await page.locator('#fpm').fill('600');
      await page.locator('#nmin').fill('1');
      await page.locator('#nmax').fill('1');
      const ta = page.locator('#normal_users');
      await ta.click(); await ta.fill('u1,p,probe*'); await ta.blur();
      await page.locator('#startBtn').click();
      // Wait until the topbar pill flips to active AND a few records have
      // landed so Live activity is genuinely populated.
      await page.locator('[data-role="status"][data-state="active"]').waitFor({ timeout: 15_000 });
      await page.waitForFunction(async () => {
        const r = await fetch('/api/status', { headers: { 'X-Requested-With': 'sftp-loadtest' } });
        const j = await r.json();
        return j.active === true && j.metrics && j.metrics.total_files >= 3;
      }, null, { timeout: 20_000, polling: 250 });
      await page.locator('[data-action="view"][data-view="workbench"]').click();
      // One more beat so the chart canvases redraw with the latest data.
      await page.waitForTimeout(800);
      await page.screenshot({ path: join(OUT_DIR, `workbench-active-${theme}.png`), fullPage: true });
      // Stop the run so subsequent screenshots start clean.
      try { await page.locator('[data-role="topbar-stop"]').click(); } catch {}
      await page.waitForFunction(async () => {
        const r = await fetch('/api/status', { headers: { 'X-Requested-With': 'sftp-loadtest' } });
        const j = await r.json();
        return j.active === false;
      }, null, { timeout: 15_000, polling: 250 });
    });

    test(`configure (${theme})`, async ({ page }) => {
      await gotoTheme(page, theme, 'configure');
      // Pre-fill so the slim run-summary chips show real values, not '—'.
      await page.locator('#conn-host').fill('sftp.example.com');
      await page.locator('#conn-port').fill('22');
      await page.locator('#upload-folder').fill('inbox');
      await page.locator('#fpm').fill('600');
      const ta = page.locator('#normal_users');
      await ta.click();
      await ta.fill('user1,pass1,invoice*\nuser2,pass2,order*');
      await ta.blur();
      await page.waitForTimeout(300);
      await page.screenshot({ path: join(OUT_DIR, `configure-${theme}.png`), fullPage: true });
    });

    test(`schedule (${theme})`, async ({ page }) => {
      await gotoTheme(page, theme, 'schedule');
      await page.screenshot({ path: join(OUT_DIR, `schedule-${theme}.png`), fullPage: true });
    });

    test(`runs (${theme})`, async ({ page }) => {
      await gotoTheme(page, theme, 'configure');
      // Pre-fill the form so the runs "About to run" plan section shows
      // realistic fields rather than empty placeholders.
      await page.locator('#conn-host').fill('sftp.example.com');
      await page.locator('#conn-port').fill('22');
      await page.locator('#upload-folder').fill('inbox');
      await page.locator('#fpm').fill('600');
      const ta = page.locator('#normal_users');
      await ta.click();
      await ta.fill('user1,pass1,invoice*\nuser2,pass2,order*');
      await ta.blur();
      await page.locator('[data-action="view"][data-view="runs"]').click();
      await page.waitForTimeout(800);
      await page.screenshot({ path: join(OUT_DIR, `runs-${theme}.png`), fullPage: true });
    });

    test(`cluster-with-workers (${theme})`, async ({ page }) => {
      await page.goto(`/?theme=${theme}`);
      await page.evaluate(() => {
        try {
          localStorage.setItem('sftp-loadtest-workers-v1', JSON.stringify([
            { id: 'wk-1', url: 'http://10.0.0.5:8080', auth_user: 'admin', auth_pass: '', enabled: true,  addedAt: '2026-04-29T00:00:00Z' },
            { id: 'wk-2', url: 'http://10.0.0.6:8080', auth_user: '',      auth_pass: '', enabled: false, addedAt: '2026-04-29T00:00:00Z' },
          ]));
        } catch {}
      });
      await page.reload();
      await page.locator('[data-action="view"][data-view="cluster"]').click();
      await page.waitForTimeout(900);
      await page.screenshot({ path: join(OUT_DIR, `cluster-with-workers-${theme}.png`), fullPage: true });
      // Reset for subsequent tests.
      await page.evaluate(() => { try { localStorage.removeItem('sftp-loadtest-workers-v1'); } catch {} });
    });

    test(`trust (${theme})`, async ({ page }) => {
      await gotoTheme(page, theme, 'trust');
      await page.screenshot({ path: join(OUT_DIR, `trust-${theme}.png`), fullPage: true });
    });

    test(`cmdk-palette-open (${theme})`, async ({ page }) => {
      await gotoTheme(page, theme, 'workbench');
      await page.keyboard.press('Meta+k');
      await page.locator('[data-component="command-palette"]').waitFor({ state: 'visible' });
      await page.locator('.cmdk-input').fill('theme');
      await page.waitForTimeout(150);
      await page.screenshot({ path: join(OUT_DIR, `cmdk-palette-open-${theme}.png`), fullPage: true });
    });

    test(`host-key-consent-modal (${theme})`, async ({ page }) => {
      // Stub a requires_consent probe so the inline accept/reject UI
      // renders without needing a server with rotating keys. The screenshot
      // captures the consent surface in the result strip of the
      // connection card — not a separate modal.
      await page.route('**/api/probe', (route) => {
        route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            ok: false,
            host: 'sftp.example.com',
            port: 22,
            stage: 'ssh_or_sftp',
            requires_consent: true,
            captured_fingerprint: 'SHA256:DEMOFINGERPRINTAAAAAAAAAAAAAAAAAAAAAAA',
            captured_for_host: 'sftp.example.com',
            error: 'Server presented a new host key. Verify the fingerprint and accept to continue.',
          }),
        });
      });
      await gotoTheme(page, theme, 'configure');
      await page.locator('#conn-host').fill('sftp.example.com');
      await page.locator('#conn-port').fill('22');
      await page.locator('[data-component="connection"] [data-role="submit"]').click();
      await page.locator('[data-component="connection"] [data-role="result"][data-state="consent"]')
        .waitFor({ state: 'visible' });
      await page.waitForTimeout(200);
      await page.screenshot({ path: join(OUT_DIR, `host-key-consent-modal-${theme}.png`), fullPage: true });
    });
  }
});
