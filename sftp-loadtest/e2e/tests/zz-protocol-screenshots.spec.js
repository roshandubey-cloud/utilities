// zz-protocol-screenshots.spec.js — 33-frame live capture sweep
// covering EVERY feature surface with real runtime data where possible
// and explicit configuration shots where it isn't (cluster, alerts).
//
// Output: docs/screenshots/protocol-runs/<NN>-<scope>-<phase>.png
//
// Coverage map (33 frames, no duplicates):
//   01-09  Protocol live-runs across SFTP / FTP / FTPS (3 each:
//          configure → test connection → workbench live)
//   10-12  Reports: multi-protocol runs list, run detail with
//          latency tiles, per-file records table
//   13-17  Cluster + workers: empty state, add-worker form,
//          sidebar entry, distribute toggle, cluster panel
//   18-21  Schedule: empty state, create form, queued, fired
//   22-23  Trust panel: SSH host keys + FTPS leaf certs
//   24-25  Alerts: config form, test fired
//   26-30  Advanced disclosures: bastion, SSH key, expert mode,
//          step-load ramp, source picker
//   31-33  Download sink + verify-SHA, Cmd+K, concurrent runs
//
// Gated behind CAPTURE=1 (matches zz-readme-screenshots).

import { test, expect } from '@playwright/test';
import { mkdirSync } from 'node:fs';
import { join } from 'node:path';

const OUT_DIR = join(import.meta.dirname, '..', '..', 'docs', 'screenshots', 'protocol-runs');
mkdirSync(OUT_DIR, { recursive: true });

const VIEWPORT = { width: 1600, height: 1000 };

// ----- helpers -------------------------------------------------------

let frameNo = 0;
async function shoot(page, label, opts = {}) {
  frameNo++;
  await page.screenshot({
    path: join(OUT_DIR, `${String(frameNo).padStart(2,'0')}-${label}.png`),
    fullPage: !!opts.fullPage,
  });
}

async function gotoView(page, view) {
  await page.locator(`[data-action="view"][data-view="${view}"]`).click();
  await page.waitForTimeout(400);
}

// Set host/port/folder via evaluate so the protocol-picker port-snap
// can't race our values. Same trick the runtime tests use.
async function setTarget(page, host, port, folder) {
  await page.evaluate(({ h, p, f }) => {
    const fire = (el) => { if (!el) return;
      el.dispatchEvent(new Event('input',  { bubbles: true }));
      el.dispatchEvent(new Event('change', { bubbles: true })); };
    const setIf = (id, v) => { const el = document.getElementById(id);
      if (el) { el.value = String(v); fire(el); } };
    setIf('conn-host', h);
    setIf('host', h);
    setIf('conn-port', p);
    setIf('port', p);
    if (f != null) {
      setIf('upload-folder', f);
      setIf('folder', f);
    }
  }, { h: host, p: port, f: folder });
}

async function configureRun(page, t) {
  await gotoView(page, 'configure');
  await page.locator(`.seg-btn[data-value="${t.protocol}"]`).first().click();
  await page.waitForTimeout(300);
  if (t.tlsMode === 'explicit') {
    const skip = page.locator('#tls_skip_verify');
    if (await skip.count()) await skip.check({ force: true });
  }
  await setTarget(page, t.host, t.port, 'inbox');
  await page.fill('#conn-user', t.user);
  await page.fill('#conn-pass', t.pass);
  await page.evaluate((line) => {
    if (typeof window.setCsvRaw === 'function') window.setCsvRaw('normal_users', line);
    else { const ta = document.getElementById('normal_users'); if (ta) ta.value = line; }
  }, `${t.user},${t.pass},f-*`);
  await page.fill('#fpm', '60');
  await page.fill('#nmin', '1');
  await page.fill('#nmax', '1');
  await page.fill('#duration', '0.02');
}

async function startRun(page, t) {
  const result = await page.evaluate(async (target) => {
    const body = window.buildRequestBody ? window.buildRequestBody() : null;
    if (!body) return { ok: false };
    if (target.protocol === 'ftps') {
      body.tls_insecure_skip_verify = true;
      body.tls_trust_on_first_use = true;
    }
    const r = await fetch('/api/start', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'sftp-loadtest' },
      body: JSON.stringify(body),
    });
    return { ok: r.ok, status: r.status, text: r.ok ? '' : await r.text() };
  }, t);
  // Wait for status pill to flip to running.
  const deadline = Date.now() + 8000;
  while (Date.now() < deadline) {
    const txt = (await page.textContent('[data-role="status-text"]').catch(() => '')) || '';
    if (/running|active|live/i.test(txt)) break;
    await page.waitForTimeout(300);
  }
  return result;
}

async function stopRun(page) {
  await page.evaluate(() => fetch('/api/stop', {
    method: 'POST',
    headers: { 'X-Requested-With': 'sftp-loadtest' },
  }));
  // Wait for active=false.
  const deadline = Date.now() + 5000;
  while (Date.now() < deadline) {
    try {
      const r = await page.evaluate(() => fetch('/api/status').then(x => x.json()));
      if (!r.active) break;
    } catch {}
    await page.waitForTimeout(300);
  }
}

// ----- the sweep -----------------------------------------------------

test.describe.configure({ mode: 'serial' });

test('capture: 33-frame live coverage sweep', async ({ page }) => {
  test.setTimeout(420_000); // 7 min cap

  await page.setViewportSize(VIEWPORT);
  await page.goto('/?theme=dark');
  await page.waitForTimeout(800);

  const TARGETS = [
    { protocol: 'sftp', port: 22020, host: '127.0.0.1', tlsMode: '', user: 'up1', pass: 'p', label: 'sftp' },
    { protocol: 'ftp',  port: 22021, host: '127.0.0.1', tlsMode: '', user: 'up1', pass: 'p', label: 'ftp'  },
    { protocol: 'ftps', port: 22021, host: '127.0.0.1', tlsMode: 'explicit', user: 'up1', pass: 'p', label: 'ftps' },
  ];

  // ============ TIER 1: protocol live-runs (frames 1-9) ============
  for (const t of TARGETS) {
    await configureRun(page, t);
    await page.waitForTimeout(500);
    await shoot(page, `${t.label}-configure`);

    // Probe — captures the test-connection success state.
    const probeBtn = page.locator('[data-role="submit"]').first();
    if (await probeBtn.count()) {
      await probeBtn.click();
      await page.waitForTimeout(2500);
    }
    await shoot(page, `${t.label}-test-connection`);

    // Run + capture the live workbench at peak (~5s in, after ramp).
    await startRun(page, t);
    await page.waitForTimeout(2500);
    await gotoView(page, 'workbench');
    await page.waitForTimeout(4500); // accumulate live data
    await shoot(page, `${t.label}-workbench-live`);

    await stopRun(page);
    await page.waitForTimeout(500);
  }

  // ============ TIER 2: reports (frames 10-12) ============
  await gotoView(page, 'runs');
  await page.waitForTimeout(1200);
  await shoot(page, 'runs-list-multiprotocol');
  // Click the first run's "Open" or "View records" so detail expands.
  const openRow = page.locator('button:has-text("View records"), button:has-text("Open")').first();
  if (await openRow.count()) {
    try { await openRow.click({ timeout: 2000 }); } catch {}
    await page.waitForTimeout(1000);
  }
  await shoot(page, 'run-detail-latency');
  // Scroll to records table area for frame 12.
  await page.evaluate(() => window.scrollTo(0, 600));
  await page.waitForTimeout(400);
  await shoot(page, 'run-records-table');
  await page.evaluate(() => window.scrollTo(0, 0));

  // ============ TIER 3: cluster + workers (frames 13-17) ============
  await gotoView(page, 'cluster');
  await page.waitForTimeout(800);
  await shoot(page, 'workers-empty-state');

  // Add-worker wizard / form. Mount sidebar's WORKERS panel +
  // also the cluster pane should expose an "Add" affordance.
  const addBtn = page.locator('[data-role="worker-add"], button:has-text("Add worker"), [data-action="add-worker"]').first();
  if (await addBtn.count()) {
    await addBtn.click();
    await page.waitForTimeout(800);
    // Fill the wizard's URL field if present.
    const urlInput = page.locator('input[name="worker_url"], #worker_url, [data-role="worker-url"]').first();
    if (await urlInput.count()) await urlInput.fill('http://worker-1.lab.local:8080');
    const userInput = page.locator('input[name="worker_user"], #worker_user').first();
    if (await userInput.count()) await userInput.fill('ops');
  }
  await shoot(page, 'worker-add-form');

  // Inject a synthetic worker into localStorage so the sidebar +
  // cluster panel render with a populated worker for the next 3
  // frames. The UI treats localStorage as the source of truth for
  // worker config — this is the same shape produced by the real
  // wizard. NOT a live worker; the screenshots accurately depict
  // post-onboarding UI state.
  await page.evaluate(() => {
    const workers = [
      { id: 'wk-demo01', url: 'http://worker-eu-1:18080', auth_user: 'ops',
        auth_pass: '', enabled: true,
        source: 'manual', spawn_id: '' },
    ];
    localStorage.setItem('sftp-loadtest-workers-v1', JSON.stringify(workers));
    // Fire a storage event so cluster-ui's heartbeat picks it up.
    window.dispatchEvent(new StorageEvent('storage', { key: 'sftp-loadtest-workers-v1', newValue: JSON.stringify(workers) }));
  });
  // Close any open modal from the previous step.
  const closeAddModal = page.locator('[data-role="modal-close"], .modal-close, [aria-label="Close"]').first();
  if (await closeAddModal.count()) {
    try { await closeAddModal.click({ timeout: 1000 }); } catch {}
  }
  await page.waitForTimeout(600);
  // Reload so the sidebar re-reads localStorage on mount, then
  // navigate back to cluster.
  await page.reload();
  await page.waitForTimeout(1200);
  await gotoView(page, 'cluster');
  await page.waitForTimeout(800);
  await shoot(page, 'workers-onboarded-sidebar');

  // Distribute toggle — switch to Configure so the upload card's
  // distribute row shows the worker count badge instead of "no
  // workers configured".
  await gotoView(page, 'configure');
  await page.waitForTimeout(800);
  // Scroll to upload card so the distribute row is in frame.
  const upload = page.locator('#normalCard, [data-component="upload"]').first();
  if (await upload.count()) await upload.scrollIntoViewIfNeeded();
  await page.waitForTimeout(400);
  await shoot(page, 'distribute-toggle-ready');

  await gotoView(page, 'cluster');
  await page.waitForTimeout(800);
  await shoot(page, 'cluster-panel-with-worker');

  // ============ TIER 4: schedule (frames 18-21) ============
  // Empty schedule view first — clear any existing schedules.
  await page.evaluate(() => fetch('/api/schedules').then(r => r.json()).then(j =>
    Promise.all((j.schedules || []).map(s =>
      fetch('/api/schedule/cancel', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'sftp-loadtest' },
        body: JSON.stringify({ id: s.id }),
      })
    ))
  ));
  await gotoView(page, 'schedule');
  await page.waitForTimeout(800);
  await shoot(page, 'schedule-empty');

  // Fill the schedule form. Date = ~10 seconds from now so it
  // fires during this spec — gives us a real "queued → fired"
  // capture.
  const fireAt = new Date(Date.now() + 10_000);
  // datetime-local input expects YYYY-MM-DDTHH:MM (local time).
  const local = (d) => {
    const pad = (n) => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
  };
  const schedAt = page.locator('#sched_at');
  if (await schedAt.count()) {
    await schedAt.fill(local(fireAt));
  }
  // Configure a fresh SFTP target so the schedule has a runnable config.
  await configureRun(page, TARGETS[0]);
  await gotoView(page, 'schedule');
  await page.waitForTimeout(400);
  await shoot(page, 'schedule-create-form');

  // Click Schedule run button.
  const schedBtn = page.locator('#scheduleBtn').first();
  if (await schedBtn.count()) await schedBtn.click({ force: true });
  await page.waitForTimeout(1000);
  await shoot(page, 'schedule-queued');

  // Wait for the schedule to fire (cron sweep is ~5s; we set the
  // run for +10s, so allow up to 30s).
  const fireDeadline = Date.now() + 30_000;
  while (Date.now() < fireDeadline) {
    const j = await page.evaluate(() => fetch('/api/runs').then(x => x.json()).catch(() => ({ runs: [] })));
    const sched = (j.runs || []).find(r => r.started_by === 'schedule');
    if (sched) break;
    await page.waitForTimeout(2000);
  }
  // Wait a bit longer for the run to finish + record.
  await page.waitForTimeout(8000);
  // Stop any run still active.
  await stopRun(page);
  await page.waitForTimeout(800);
  await gotoView(page, 'runs');
  await page.waitForTimeout(1200);
  await shoot(page, 'schedule-fired-with-sched-badge');

  // ============ TIER 5: trust panel (frames 22-23) ============
  // The runs above pinned host keys + leaf certs into the trust
  // store. Visit Trust to capture the real receipts.
  await gotoView(page, 'trust');
  await page.waitForTimeout(1000);
  await shoot(page, 'trust-ssh-host-keys');
  // Trust panel shows both sections; scroll for FTPS certs.
  await page.evaluate(() => window.scrollTo(0, 600));
  await page.waitForTimeout(400);
  await shoot(page, 'trust-ftps-leaf-certs');
  await page.evaluate(() => window.scrollTo(0, 0));

  // ============ TIER 6: alerts (frames 24-25) ============
  await gotoView(page, 'configure');
  await page.waitForTimeout(500);
  // The alerts card lives inside the legacy .grid wrapper which the
  // redesign collapses via `.configure-legacy-residue { display:none }`
  // so it isn't visible by default in the modern shell. Temporarily
  // un-hide the legacy residue + scroll to the alerts card so the
  // capture shows the actual functional UI; the full Alerts API
  // (/api/alerts) is wired through legacy.js whether or not the
  // redesign surfaces the panel.
  await page.evaluate(() => {
    document.querySelectorAll('.configure-legacy-residue').forEach(el => {
      el.classList.remove('configure-legacy-residue');
      el.style.display = ''; // belt-and-suspenders
    });
    const card = document.querySelector('[data-component="alerts"]');
    if (!card) return;
    card.querySelectorAll('details').forEach(d => { d.open = true; });
    card.scrollIntoView({ block: 'center' });
  });
  await page.waitForTimeout(500);
  await page.waitForTimeout(300);
  // Use evaluate with optional setIf so a missing input doesn't kill
  // the spec — better than fill() which fails on hidden/missing.
  await page.evaluate((vals) => {
    const setIf = (id, v) => {
      const el = document.getElementById(id);
      if (el) {
        el.value = v;
        el.dispatchEvent(new Event('input',  { bubbles: true }));
        el.dispatchEvent(new Event('change', { bubbles: true }));
      }
    };
    Object.entries(vals).forEach(([k, v]) => setIf(k, v));
  }, {
    alert_slack:        'https://hooks.slack.com/services/T-DEMO/B-DEMO/redacted',
    alert_webhook:      'https://obs.example.com/sftp-loadtest/events',
    alert_smtp_host:    'smtp.mail.example.com',
    alert_smtp_port:    '587',
    alert_smtp_user:    'load-tests@example.com',
    alert_email_from:   'load-tests@example.com',
    alert_email_to:     'oncall@example.com',
    alert_p99_ms:       '500',
    alert_err_pct:      '5',
  });
  // Save alert config so the Test endpoint has something to fan-out.
  const saveAlerts = page.locator('#alertsSaveBtn').first();
  if (await saveAlerts.count()) {
    try { await saveAlerts.click({ timeout: 2000 }); } catch {}
    await page.waitForTimeout(500);
  }
  await shoot(page, 'alerts-config-filled');

  // Click Test alert to fire a synthetic event.
  const testAlerts = page.locator('#alertsTestBtn').first();
  if (await testAlerts.count()) {
    try { await testAlerts.click({ timeout: 2000 }); } catch {}
    await page.waitForTimeout(1500);
  }
  await shoot(page, 'alerts-test-fired');

  // ============ TIER 7: advanced disclosures (frames 26-30) ============
  await gotoView(page, 'configure');
  await page.waitForTimeout(400);

  // 26: bastion disclosure expanded.
  const bastion = page.locator('[data-role="bastion-disclosure"]').first();
  if (await bastion.count()) {
    await bastion.evaluate(el => el.open = true);
    await bastion.scrollIntoViewIfNeeded();
    await page.waitForTimeout(300);
  }
  // Fill demo bastion fields so the screenshot shows real-looking values.
  await page.fill('#bastion_host', 'jump.lab.example.com');
  await page.fill('#bastion_port', '22');
  await page.fill('#bastion_user', 'jump-svc');
  await shoot(page, 'bastion-disclosure-expanded');

  // Reset bastion fields BEFORE closing the disclosure (page.fill
  // refuses hidden inputs with a 10s timeout). Close after.
  await page.evaluate(() => {
    ['bastion_host','bastion_user','bastion_pass','bastion_pem','bastion_passphrase'].forEach(id => {
      const el = document.getElementById(id);
      if (el) { el.value = ''; el.dispatchEvent(new Event('change', { bubbles: true })); }
    });
  });
  if (await bastion.count()) await bastion.evaluate(el => el.open = false);
  await page.waitForTimeout(300);

  // 27: SSH key disclosure expanded.
  const keyDis = page.locator('[data-role="key-disclosure"]').first();
  if (await keyDis.count()) {
    await keyDis.evaluate(el => el.open = true);
    await keyDis.scrollIntoViewIfNeeded();
    await page.waitForTimeout(300);
  }
  // Insert a sample PEM placeholder so the textarea shows content.
  const pkSample = `-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW
QyNTUxOQAAACBLgo0xK4tVDcRSgQ3VK7Lf4y7UVWiLg0xK4tVDcRSgQwAAAJjGPTrYxj06
... (sample for screenshot only) ...
-----END OPENSSH PRIVATE KEY-----`;
  await page.fill('#conn-private-key', pkSample);
  await shoot(page, 'ssh-key-disclosure-expanded');
  // Clear BEFORE closing the disclosure (avoid hidden-input fill).
  await page.evaluate(() => {
    const el = document.getElementById('conn-private-key');
    if (el) { el.value = ''; el.dispatchEvent(new Event('change', { bubbles: true })); }
  });
  if (await keyDis.count()) await keyDis.evaluate(el => el.open = false);

  // 28: Expert mode (Run controls) disclosure expanded.
  const expert = page.locator('.cfg-limits-expert').first();
  if (await expert.count()) {
    await expert.evaluate(el => el.open = true);
    await expert.scrollIntoViewIfNeeded();
    await page.waitForTimeout(300);
  }
  // Set a slowdown threshold so the values are visible.
  await page.fill('#speed_floor_percent', '50');
  await page.fill('#speed_floor_breach_sec', '90');
  await shoot(page, 'expert-mode-slowdown-threshold');

  // 29: Step-load ramp config visible. The ramp lives inside a
  // <details data-role="ramp-disclosure"> in the Upload card —
  // OPEN the details, set the toggle + values, then scroll to it.
  await page.evaluate(() => {
    const ramp = document.querySelector('[data-role="ramp-disclosure"]');
    if (ramp) ramp.open = true;
    const cb = document.getElementById('ramp_enabled');
    if (cb && !cb.checked) {
      cb.checked = true;
      cb.dispatchEvent(new Event('change', { bubbles: true }));
    }
    const setIf = (id, v) => { const el = document.getElementById(id);
      if (el) { el.value = v; el.dispatchEvent(new Event('input',  { bubbles: true })); el.dispatchEvent(new Event('change', { bubbles: true })); } };
    setIf('ramp_start',   '60');
    setIf('ramp_step',    '20');
    setIf('ramp_every',   '300');
    setIf('ramp_ceiling', '600');
    const rampArea = document.querySelector('[data-role="ramp-disclosure"]');
    if (rampArea) rampArea.scrollIntoView({ block: 'center' });
  });
  await page.waitForTimeout(500);
  await shoot(page, 'step-load-ramp-config');

  // 30: Source picker on the Upload card. The disclosure is
  // [data-role="source-disclosure"][data-kind="normal"]; open it
  // and scroll into view.
  await page.evaluate(() => {
    const src = document.querySelector('[data-role="source-disclosure"][data-kind="normal"]');
    if (src) {
      src.open = true;
      src.scrollIntoView({ block: 'center' });
    }
  });
  await page.waitForTimeout(500);
  await shoot(page, 'source-picker-expanded');

  // ============ TIER 8: misc proof (frames 31-33) ============
  // 31: Download card with sink + verify-SHA-256 toggle.
  await page.evaluate(() => {
    // Enable the Download workload toggle so the card body opens.
    const cb = document.getElementById('download_enabled');
    if (cb && !cb.checked) { cb.checked = true; cb.dispatchEvent(new Event('change', { bubbles: true })); }
  });
  await page.waitForTimeout(400);
  // Tick verify_hashes (now lives inside Download card per v0.18.3).
  const verify = page.locator('#verify_hashes').first();
  if (await verify.count()) await verify.check({ force: true });
  // Open the sink disclosure to reveal local-disk template field.
  const sinkDis = page.locator('[data-role="sink-disclosure"]').first();
  if (await sinkDis.count()) {
    await sinkDis.evaluate(el => el.open = true);
    await sinkDis.scrollIntoViewIfNeeded();
    await page.waitForTimeout(300);
  }
  await shoot(page, 'download-sink-and-verify-sha256');

  // 32: Cmd+K palette open. The topbar trigger may be obscured by a
  // toast / disclosure left over from earlier frames — use force
  // and a short timeout to avoid the 10s wait if the click is
  // intercepted; fall back to the keyboard shortcut.
  let cmdkOpen = false;
  try {
    await page.locator('[data-role="topbar-cmdk"]').first().click({ force: true, timeout: 2000 });
    cmdkOpen = true;
  } catch {}
  if (!cmdkOpen) {
    await page.keyboard.press('Meta+K').catch(() => {});
  }
  await page.waitForTimeout(800);
  await shoot(page, 'cmdk-palette-open');
  await page.keyboard.press('Escape').catch(() => {});
  await page.waitForTimeout(300);

  // 33: Concurrent runs — start two against the same SFTP target,
  // navigate to Runs, capture the multi-active state.
  for (let i = 0; i < 2; i++) {
    await configureRun(page, TARGETS[0]);
    await page.evaluate(async () => {
      const body = window.buildRequestBody ? window.buildRequestBody() : null;
      if (!body) return;
      await fetch('/api/start?force=true', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'sftp-loadtest' },
        body: JSON.stringify(body),
      });
    });
    await page.waitForTimeout(1500);
  }
  await gotoView(page, 'runs');
  await page.waitForTimeout(1500);
  await shoot(page, 'concurrent-runs-multi-active');

  // Cleanup: stop any active runs.
  await stopRun(page);
  await stopRun(page); // catch the second one if multi-active

  expect(frameNo).toBe(33);
});
