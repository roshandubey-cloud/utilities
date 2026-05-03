// zz-protocol-screenshots.spec.js — live-data screenshot capture across
// all three transport protocols (SFTP / FTP / FTPS). Runs a fresh
// load-test against each mock target sequentially, takes 8 screenshots
// per protocol covering the lifecycle (configure, probe, mid-run,
// peak-run, completion, runs-list, detail), for 24 total.
//
// Output: docs/screenshots/protocol-runs/<NN>-<protocol>-<phase>.png
//
// Gated behind CAPTURE=1 (same convention as zz-readme-screenshots).
//
// Usage:
//   CAPTURE=1 ./node_modules/.bin/playwright test \
//     tests/zz-protocol-screenshots.spec.js --reporter=list

import { test, expect } from '@playwright/test';
import { mkdirSync } from 'node:fs';
import { join } from 'node:path';

const OUT_DIR = join(import.meta.dirname, '..', '..', 'docs', 'screenshots', 'protocol-runs');
mkdirSync(OUT_DIR, { recursive: true });

const VIEWPORT = { width: 1600, height: 1000 };

// One descriptor per protocol. tlsMode is non-empty only for FTPS.
// duration_hours is short — we want a clean run that completes
// within the spec's timeout but produces enough rows for the live
// chart to render meaningful data.
const TARGETS = [
  {
    protocol: 'sftp',  port: 22020, host: '127.0.0.1', tlsMode: '',
    user: 'up1', pass: 'p',
    label: 'sftp',
  },
  {
    protocol: 'ftp',   port: 22021, host: '127.0.0.1', tlsMode: '',
    user: 'up1', pass: 'p',
    label: 'ftp',
  },
  {
    protocol: 'ftps',  port: 22021, host: '127.0.0.1', tlsMode: 'explicit',
    user: 'up1', pass: 'p',
    label: 'ftps',
  },
];

async function shoot(page, frame) {
  // full-page=false so we capture the visible viewport (chart frames
  // include axes + legend that disappear when the page scrolls); the
  // workbench and configure views are designed to fit one viewport.
  await page.screenshot({ path: join(OUT_DIR, `${frame}.png`), fullPage: false });
}

async function gotoView(page, view) {
  // Sidebar nav buttons — the Configure / Workbench / Runs / Trust panes.
  await page.locator(`[data-action="view"][data-view="${view}"]`).click();
  await page.waitForTimeout(400);
}

async function configureRun(page, t) {
  await gotoView(page, 'configure');

  // Protocol picker — segmented control. The click triggers a port
  // auto-snap (22 / 21 / 21|990 depending on protocol+TLS mode) via
  // the picker handler. EVERY click that resets userEditedPort to
  // false (protocol picker, TLS-mode picker) MUST happen before our
  // port override — otherwise the snap re-runs after our override
  // and the run dials the default port.
  await page.locator(`.seg-btn[data-value="${t.protocol}"]`).first().click();
  await page.waitForTimeout(400);

  // FTPS-only: skip cert verification against the self-signed mock.
  // Done BEFORE the port-set so the toggle's mutations (which don't
  // touch the port) settle first. Explicit-TLS is the default mode
  // so we DON'T re-click the TLS-mode segmented picker (each click
  // resets userEditedPort and re-snaps the port to 21, which is
  // exactly the bug we're avoiding).
  if (t.tlsMode === 'explicit') {
    const skip = page.locator('#tls_skip_verify');
    if (await skip.count()) await skip.check({ force: true });
  }

  // Set host/port directly via evaluate so the visible Quick-checks
  // input AND the legacy hidden input agree, no race against any
  // remaining picker handlers. Done AFTER all picker clicks above.
  await page.evaluate(({ host, port }) => {
    const fire = (el) => {
      if (!el) return;
      el.dispatchEvent(new Event('input',  { bubbles: true }));
      el.dispatchEvent(new Event('change', { bubbles: true }));
    };
    const setIf = (id, val) => {
      const el = document.getElementById(id);
      if (el) { el.value = String(val); fire(el); }
    };
    setIf('conn-host', host);
    setIf('host', host);
    setIf('conn-port', port);
    setIf('port', port);
  }, { host: t.host, port: t.port });
  await page.fill('#conn-user', t.user);
  await page.fill('#conn-pass', t.pass);

  // Upload folder — the visible field is #upload-folder (injected by
  // upload-restructure.js); it mirrors into the legacy hidden #folder
  // via input event. /api/start rejects an empty upload_folder, so
  // type it explicitly — the placeholder "inbox" is not a value.
  await page.fill('#upload-folder', 'inbox');

  // Normal load: 60 fpm × 1 MB, single user CSV pointing at our user.
  await page.fill('#fpm', '60');
  await page.fill('#nmin', '1');
  await page.fill('#nmax', '1');

  // Users CSV — the form has a textarea masked behind the table editor.
  // Use the JS API the form exposes via setCsvRaw (window-global).
  await page.evaluate((line) => {
    if (typeof window.setCsvRaw === 'function') {
      window.setCsvRaw('normal_users', line);
    } else {
      const ta = document.getElementById('normal_users');
      if (ta) ta.value = line;
    }
  }, `${t.user},${t.pass},f-*`);

  // Run length — 0.02 hours = 72 seconds — long enough that our
  // 4 workbench captures all land while the dispatcher is active and
  // the live chart has accumulated rows to display.
  await page.fill('#duration', '0.02');
}

async function startRun(page, t) {
  const result = await page.evaluate(async (target) => {
    const body = window.buildRequestBody ? window.buildRequestBody() : null;
    if (!body) return { ok: false, reason: 'buildRequestBody not exposed' };
    // Belt-and-suspenders for FTPS: the mock cert is self-signed,
    // so the runner needs either skip-verify OR auto-TOFU. The form
    // checkbox propagation under our automation is flaky; force both
    // flags here so the run actually starts. In production, the
    // operator's UI choice still drives the request body — this only
    // overrides for the screenshot-capture spec.
    if (target && target.protocol === 'ftps') {
      body.tls_insecure_skip_verify = true;
      body.tls_trust_on_first_use   = true;
    }
    const r = await fetch('/api/start', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'X-Requested-With': 'sftp-loadtest',
      },
      body: JSON.stringify(body),
    });
    return {
      ok: r.ok, status: r.status, text: r.ok ? '' : await r.text(),
      bodySent: { protocol: body.protocol, host: body.host, port: body.port, folder: body.upload_folder },
    };
  }, t);
  if (!result.ok) {
    console.log('startRun: /api/start failed', JSON.stringify(result, null, 2));
  } else {
    console.log('startRun: ok', JSON.stringify(result.bodySent));
  }
  // Wait for the topbar status pill to flip from "Idle" to "running".
  const deadline = Date.now() + 8000;
  while (Date.now() < deadline) {
    const txt = (await page.textContent('[data-role="status-text"]').catch(() => '')) || '';
    if (/running|active|live/i.test(txt)) break;
    await page.waitForTimeout(300);
  }
}

async function waitForLiveData(page, timeoutMs = 12_000) {
  // Wait until /api/status reports total_files > 0 OR records_in_memory > 0.
  // Polls the badge in the run header.
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const txt = await page.textContent('body').catch(() => '');
    // Heuristic: any non-zero "files" badge means the run produced rows.
    const filesMatch = txt && txt.match(/(\d+)\s+files/);
    if (filesMatch && parseInt(filesMatch[1], 10) > 0) return;
    await page.waitForTimeout(500);
  }
}

async function stopRun(page) {
  // Topbar Stop button is the redesigned action; legacy #stopBtn is
  // a fallback (kept in the legacy DOM for compat).
  const topbarStop = page.locator('[data-role="topbar-stop"]');
  if (await topbarStop.count() && !(await topbarStop.isDisabled())) {
    await topbarStop.click();
  } else {
    const legacyStop = page.locator('#stopBtn');
    if (await legacyStop.count() && await legacyStop.isEnabled()) {
      await legacyStop.click();
    }
  }
  // Wait for the run to actually finish (badge clears) — poll until
  // the status pill reads "Idle" again, max 6s.
  const deadline = Date.now() + 6000;
  while (Date.now() < deadline) {
    const txt = (await page.textContent('[data-role="status-text"]').catch(() => '')) || '';
    if (/idle|finished|completed|stopped/i.test(txt)) break;
    await page.waitForTimeout(300);
  }
}

test.describe.configure({ mode: 'serial' });

test('capture: live-data screenshots across SFTP / FTP / FTPS', async ({ page }) => {
  test.setTimeout(180_000); // 3 min cap for the whole sweep

  await page.setViewportSize(VIEWPORT);
  await page.goto('/?theme=dark');
  // Settle the deferred-mount components (cluster-ui, sidebar heartbeat).
  await page.waitForTimeout(800);

  let frameNo = 0;
  for (const t of TARGETS) {
    // ---- Frame 1: Configure panel filled with this protocol's target.
    await configureRun(page, t);
    await page.waitForTimeout(400);
    frameNo++;
    await shoot(page, `${String(frameNo).padStart(2,'0')}-${t.label}-configure`);

    // ---- Frame 2: Test connection — capture the "verified" toast/state.
    const probeBtn = page.locator('[data-role="submit"]').first();
    if (await probeBtn.count()) {
      await probeBtn.click();
      await page.waitForTimeout(2500);
      frameNo++;
      await shoot(page, `${String(frameNo).padStart(2,'0')}-${t.label}-test-connection`);
    } else {
      frameNo++; // keep numbering consistent
    }

    // ---- Frame 3: Run started — early workbench (chart just starting).
    await startRun(page, t);
    await page.waitForTimeout(2500);
    await gotoView(page, 'workbench');
    await page.waitForTimeout(800);
    frameNo++;
    await shoot(page, `${String(frameNo).padStart(2,'0')}-${t.label}-workbench-early`);

    // ---- Frame 4: Workbench at peak — live activity table populated.
    await waitForLiveData(page);
    await page.waitForTimeout(3500); // let the chart accumulate samples
    frameNo++;
    await shoot(page, `${String(frameNo).padStart(2,'0')}-${t.label}-workbench-peak`);

    // ---- Frame 5: Latency percentiles populated — scroll to that panel.
    const latency = page.locator('[data-component="latency"], #latency, .latency-card').first();
    if (await latency.count()) {
      await latency.scrollIntoViewIfNeeded();
      await page.waitForTimeout(400);
    }
    frameNo++;
    await shoot(page, `${String(frameNo).padStart(2,'0')}-${t.label}-workbench-latency`);

    // ---- Frame 6: Records / activity table — scroll back to the records panel.
    const records = page.locator('[data-component="records"], #records, .records-card').first();
    if (await records.count()) {
      await records.scrollIntoViewIfNeeded();
      await page.waitForTimeout(400);
    }
    frameNo++;
    await shoot(page, `${String(frameNo).padStart(2,'0')}-${t.label}-workbench-records`);

    // ---- Frame 7: Stop the run, navigate to the Runs list.
    await stopRun(page);
    await gotoView(page, 'runs');
    await page.waitForTimeout(1200);
    frameNo++;
    await shoot(page, `${String(frameNo).padStart(2,'0')}-${t.label}-runs-list`);

    // ---- Frame 8: Run detail / CSV download row.
    const firstRow = page.locator('#runs_body tr, [data-role="run-row"]').first();
    if (await firstRow.count()) {
      const viewBtn = firstRow.locator('button, a').first();
      if (await viewBtn.count()) {
        try { await viewBtn.click({ timeout: 2000 }); } catch {}
      }
    }
    await page.waitForTimeout(900);
    frameNo++;
    await shoot(page, `${String(frameNo).padStart(2,'0')}-${t.label}-runs-detail`);

    // Reset to Configure for the next protocol so the form is ready.
    await gotoView(page, 'configure');
    await page.waitForTimeout(300);
  }

  expect(frameNo).toBe(24);
});
