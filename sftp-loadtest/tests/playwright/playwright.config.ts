// playwright.config.ts — Playwright entry for the sftp-loadtest
// end-to-end suite. The webServer block boots a fresh server
// binary against a temporary reports directory before any spec
// runs, so every test sees a clean state: no saved runs, no vault,
// no diagnoses. Specs that need state plant it themselves.
//
// The binary is rebuilt from source on each `npm test` run via the
// preflight bash command — this is the moral equivalent of `go test`
// rebuilding before running. Slower than re-using a cached binary
// but it catches "tests passed on stale build" issues that bit us
// during v0.20.4–v0.20.7.

import { defineConfig, devices } from '@playwright/test';
import * as path from 'path';

const PORT = Number(process.env.SLT_E2E_PORT || 18290);
const REPORTS_DIR = process.env.SLT_E2E_REPORTS_DIR || path.join('/tmp', `slt-e2e-${process.pid}`);
const REPO_ROOT = path.resolve(__dirname, '..', '..');
const BIN = path.join(REPO_ROOT, 'tests', 'playwright', 'tmp', 'sftp-loadtest-e2e');

export default defineConfig({
  testDir: './specs',
  // v0.20.10 — globalSetup boots the mock SFTP + FTP servers so
  // per-protocol probe and run specs can drive REAL servers without
  // every spec re-spawning them. Teardown kills the spawned pids.
  globalSetup: require.resolve('./global-setup'),
  globalTeardown: require.resolve('./global-teardown'),
  // Bug regressions are easier to read when failures keep going,
  // so we run sequentially. Tests are fast enough.
  fullyParallel: false,
  workers: 1,
  retries: process.env.CI ? 1 : 0,
  // Reasonable ceiling per test — runs that drive a real load test
  // (specs 07-09) can take a few seconds to settle, plus the
  // wide-click-sweep can take 20-30s on a slow runner.
  timeout: 90_000,
  reporter: process.env.CI ? [['github'], ['html', { open: 'never' }]] : 'list',
  use: {
    baseURL: `http://127.0.0.1:${PORT}`,
    extraHTTPHeaders: {
      // The server enforces a CSRF gate that demands a fixed
      // X-Requested-With header on every state-changing request
      // (POST/PUT/DELETE).  See internal/web/security.go.
      'X-Requested-With': 'sftp-loadtest',
    },
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    video: 'retain-on-failure',
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
  // Build the server binary into tests/playwright/tmp/ before
  // boot, then start it on the chosen port.  webServer.command
  // runs from the repo root.
  webServer: {
    command: [
      `mkdir -p tests/playwright/tmp`,
      `go build -o ${BIN} .`,
      `rm -rf ${REPORTS_DIR}`,
      `mkdir -p ${REPORTS_DIR}`,
      // -insecure-host-key tells the SFTP layer to accept any
      // host key without consulting the global trust store. The
      // mock SFTP server generates a fresh key on each boot; the
      // store would flag it as a mismatch against any previously
      // stored fingerprint, blocking probes and runs.
      // -tls-hosts pins the FTPS leaf-cert store to a per-suite
      // temp file so the mock FTPS server's fresh cert doesn't
      // trip the "cert has changed" check against a stale entry
      // in the global ~/Library/Application Support store.
      // Both knobs combine to give the suite a clean trust state.
      `${BIN} -addr 127.0.0.1:${PORT} -reports-dir ${REPORTS_DIR} -insecure-host-key -tls-hosts ${REPORTS_DIR}/tls-hosts.json`,
    ].join(' && '),
    cwd: REPO_ROOT,
    url: `http://127.0.0.1:${PORT}/healthz`,
    timeout: 60_000,
    reuseExistingServer: !process.env.CI,
    stdout: 'pipe',
    stderr: 'pipe',
  },
});
