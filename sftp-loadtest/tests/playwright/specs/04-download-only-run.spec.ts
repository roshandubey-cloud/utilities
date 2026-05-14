// 04 — REGRESSION GUARD for: "Download-only loadtest never runs."
//
// Originally reported against Walmart MoFT — operator provided a
// list of download users with passwords, no upload load, and
// clicked Run. The server rejected the start with
// "enable at least one of normal-load or large-file-load" — but
// pull-only load tests are a perfectly valid workload (e.g.
// "how fast can my server serve files that are already there?").
//
// The fix relaxes RunConfig.Validate to ALSO accept a config that
// enables ONLY download.
//
// This spec hits /api/start directly with a download-only payload
// pointing at a non-existent server. We expect:
//   * 200 OK — validation accepted the config.
//   * The run starts (we don't wait for it to finish; we just
//     stop it after a moment to avoid wedging a real run).
import { test, expect } from '@playwright/test';
import { apiPost, apiGet, CSRF } from '../fixtures/server';

test('Validate accepts a download-only run config', async ({ request }) => {
  // Bogus host:port so the run will fail quickly on dial — but
  // validation runs BEFORE the dial, so we only need the
  // /api/start handler to accept the payload.
  // /api/start uses snake_case body keys (upload_folder, duration_hours,
  // parallel_streams, download_users_csv, etc.). The download_users
  // CSV format is "username,password" — one user per line.
  const body = {
    host: '127.0.0.1',
    port: 1,
    duration_hours: 0.01,
    parallel_streams: 1,
    download_enabled: true,
    download_parallel_streams: 1,
    download_folder: '/outgoing',
    download_match_mode: 'filename',
    download_sink: { kind: 'discard' },
    download_users_csv: 'moftuser1,Sft@1234\nmoftuser2,Sft@1234',
  };
  const r = await apiPost(request, '/api/start', body);
  const text = await r.text();
  // Validation passes ⇒ either 200 (run started against a real
  // server, won't happen in CI) or a downstream pre-flight error
  // about CONNECTING to the bogus host. The bug we're guarding
  // against was a VALIDATION error rejecting the config shape.
  // So we assert the response does NOT contain any of the legacy
  // validate error strings.
  const lower = text.toLowerCase();
  expect(lower, `validation should not have rejected download-only: ${text.slice(0, 200)}`)
    .not.toContain('upload folder is required');
  expect(lower, `validation should not have rejected download-only: ${text.slice(0, 200)}`)
    .not.toContain('enable at least one of normal-load or large-file-load');

  // Stop the run immediately so we don't leave it dialling a bogus
  // host for the duration of the suite.
  await apiPost(request, '/api/stop', {});
});

test('Validate still rejects a config with no load enabled at all', async ({ request }) => {
  const body = {
    host: '127.0.0.1',
    port: 1,
    folder: '/incoming',
    duration_hours: 0.01,
    parallel_streams: 1,
  };
  const r = await apiPost(request, '/api/start', body);
  expect(r.status(), 'no load enabled must still error').toBeGreaterThanOrEqual(400);
  const text = await r.text();
  expect(text.toLowerCase()).toMatch(/load|enable/);
});
