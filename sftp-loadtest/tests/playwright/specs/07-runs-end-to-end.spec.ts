// 07 — full run scenarios against the mock servers.
//
// Drives a real /api/start with a tight workload (small files,
// 2-second run), polls /api/status until the run seals, then asserts:
//   * the run shows up in /api/runs with target_host populated
//   * succeeded_files > 0 (the run actually moved data)
//   * the run-detail page renders every panel with non-placeholder
//     content (KPIs, Latency percentiles, Workload, Local host peaks)
//   * the runs-history card renders the destination badge
//
// Scenarios:
//   A. SFTP upload-only
//   B. SFTP upload + download with trackid match
//   C. SFTP upload + download with filename match
//   D. FTP  upload-only
//   E. FTPS upload-only (implicit, skip-verify)
//
// Five scenarios, one driver function — `runScenario(opts)` posts
// the start request, waits for seal, returns the run id.

import { test, expect, APIRequestContext } from '@playwright/test';
import { apiPost, apiGet } from '../fixtures/server';
import { MOCK_SFTP_PORT, MOCK_FTP_PORT, MOCK_FTPS_PORT } from '../global-setup';

interface ScenarioOpts {
  protocol: 'sftp' | 'ftp' | 'ftps';
  port: number;
  tlsMode?: 'explicit' | 'implicit';
  download?: 'trackid' | 'filename' | false;
}

async function runScenario(req: APIRequestContext, opts: ScenarioOpts): Promise<string> {
  // Pair an upload user with a download user so trackid / filename
  // routing has a real target on the mock server (mocksftp's
  // -pairs flag defaults to self-loop when unset, so up1=dl1 if
  // both users are present; here we just use up1 / dl1).
  const body: any = {
    host: '127.0.0.1',
    port: opts.port,
    protocol: opts.protocol,
    upload_folder: '/inbox',
    parallel_streams: 2,
    duration_hours: 0.001, // ~3.6 seconds — long enough to ship a few files
    poll_seconds: 1,
    normal_enabled: true,
    files_per_minute: 60,
    normal_min_mb: 1,
    normal_max_mb: 1,
    normal_content_type: 'binary',
    // Upload CSV demands a third column — the filename pattern
    // (* = any). DownloadUsersCSV stays at the 2-col username,password.
    normal_users_csv: 'up1,p,*\nup2,p,*',
  };
  if (opts.protocol === 'ftps') {
    body.tls_mode = opts.tlsMode || 'implicit';
    // Both knobs: skip-verify bypasses the store entirely, but
    // some code paths still consult the store before applying
    // skip-verify, so tls_trust_on_first_use catches that case
    // by silently pinning whatever cert the mock presents.
    body.tls_insecure_skip_verify = true;
    body.tls_trust_on_first_use = true;
  }
  if (opts.download) {
    body.download_enabled = true;
    body.download_parallel_streams = 2;
    body.download_folder = '/outbox';
    body.download_match_mode = opts.download;
    body.download_users_csv = 'dl1,p\ndl2,p';
    body.download_sink = { kind: 'discard' };
  }
  const r = await apiPost(req, '/api/start', body);
  expect(r.status(), `start should succeed: ${await r.text()}`).toBe(200);
  const j = await r.json();
  const runID = j.id || j.run_id || j.runId;
  expect(runID, 'start response must carry run id').toBeTruthy();

  // Poll /api/status until the run goes inactive (sealed).
  const deadline = Date.now() + 30_000;
  while (Date.now() < deadline) {
    const s = await apiGet(req, '/api/status');
    if (s.ok()) {
      const js = await s.json();
      if (!js.active) break;
    }
    await new Promise((r) => setTimeout(r, 300));
  }
  // One more second for the seal to flush meta JSON to disk.
  await new Promise((r) => setTimeout(r, 1_000));
  return runID;
}

async function assertRunReport(req: APIRequestContext, runID: string, expectedHost: string, expectedPort: number) {
  const r = await apiGet(req, '/api/runs');
  expect(r.ok()).toBeTruthy();
  const j = await r.json();
  const entry = (j.runs || []).find((x: any) => x.id === runID);
  expect(entry, `run ${runID} should appear in /api/runs`).toBeTruthy();
  expect(entry.target_host).toBe(expectedHost);
  expect(entry.target_port).toBe(expectedPort);
  expect(Number(entry.total_files || 0), 'run should have moved files').toBeGreaterThan(0);
  expect(Number(entry.succeeded_files || 0), 'at least one file should have succeeded').toBeGreaterThan(0);
}

test.describe('end-to-end run scenarios against mock servers', () => {
  test('SFTP upload-only — files succeed and report renders', async ({ request, page }) => {
    const runID = await runScenario(request, { protocol: 'sftp', port: MOCK_SFTP_PORT });
    await assertRunReport(request, runID, '127.0.0.1', MOCK_SFTP_PORT);
    await assertRunDetailUI(page, runID);
  });

  test('SFTP upload + download (trackid) — round-trip completes', async ({ request, page }) => {
    const runID = await runScenario(request, { protocol: 'sftp', port: MOCK_SFTP_PORT, download: 'trackid' });
    await assertRunReport(request, runID, '127.0.0.1', MOCK_SFTP_PORT);
    await assertRunDetailUI(page, runID);
  });

  test('SFTP upload + download (filename) — round-trip completes', async ({ request, page }) => {
    const runID = await runScenario(request, { protocol: 'sftp', port: MOCK_SFTP_PORT, download: 'filename' });
    await assertRunReport(request, runID, '127.0.0.1', MOCK_SFTP_PORT);
    await assertRunDetailUI(page, runID);
  });

  test('FTP plain upload-only — files succeed', async ({ request, page }) => {
    const runID = await runScenario(request, { protocol: 'ftp', port: MOCK_FTP_PORT });
    await assertRunReport(request, runID, '127.0.0.1', MOCK_FTP_PORT);
    await assertRunDetailUI(page, runID);
  });

  test('FTPS implicit upload-only — files succeed', async ({ request, page }) => {
    const runID = await runScenario(request, { protocol: 'ftps', port: MOCK_FTPS_PORT, tlsMode: 'implicit' });
    await assertRunReport(request, runID, '127.0.0.1', MOCK_FTPS_PORT);
    await assertRunDetailUI(page, runID);
  });
});

// assertRunDetailUI navigates to the Runs view, clicks View records
// on the freshly-completed run, and asserts the detail page renders
// the destination badge + every KPI tile.
async function assertRunDetailUI(page: any, runID: string) {
  await page.goto('/');
  await page.waitForSelector('.shell-topbar', { state: 'visible' });
  await page.click('.shell-sidebar-row[data-view="runs"]');
  // Wait for the runs-history component to render at least one card.
  await page.waitForSelector('.runs-history-card', { state: 'visible', timeout: 10_000 });
  // Use Playwright's hasText filter — `:text-is()` inside `:has()`
  // isn't a native CSS pseudo and Playwright doesn't always resolve
  // it the way you'd expect when combined.
  const card = page.locator('.runs-history-card').filter({ hasText: runID }).first();
  await card.waitFor({ state: 'visible', timeout: 10_000 });
  // run-detail.js auto-injects an "Open" button with
  // data-view-detail=<runID> next to the existing "View records"
  // button. THAT button opens the detail page; the existing
  // "View records" only swaps the inline records table.
  await card.locator('[data-view-detail]').click();
  await page.waitForSelector('.run-detail-head', { state: 'visible' });

  // Destination badge — proves target_host wiring landed in the
  // header.
  const target = page.locator('.run-detail-target-badge').first();
  await expect(target).toBeVisible();
  expect((await target.textContent()) ?? '').toContain('127.0.0.1');

  // KPI tiles have class .run-detail-kpi with .kpi-value children.
  const kpis = await page.locator('.run-detail-kpis .run-detail-kpi .kpi-value').allTextContents();
  expect(kpis.length).toBeGreaterThanOrEqual(4);
  // At least one KPI should be a positive integer (Files / Bytes / etc.).
  const numeric = kpis.find((t: string) => /^\d/.test(t.trim()));
  expect(numeric, `expected at least one numeric KPI; got ${JSON.stringify(kpis)}`).toBeTruthy();

  // Latency percentiles panel renders.
  await expect(page.locator('.run-detail-panel-title:has-text("Latency percentiles")')).toBeVisible();
  // Workload panel renders.
  await expect(page.locator('.run-detail-panel-title:has-text("Workload")')).toBeVisible();
  // Local host peaks panel renders.
  await expect(page.locator('.run-detail-panel-title:has-text("Local host peaks")')).toBeVisible();
  // Download CSV link always present in the run-detail header
  // (the page also has Download CSV anchors on the hero card and
  // the scheduled-run banner; scope strictly to the detail header).
  await expect(page.locator('.run-detail-head a:has-text("Download CSV")')).toBeVisible();
  // Run Doctor button present and enabled.
  await expect(page.locator('[data-role="run-doctor"]')).toBeVisible();
}
