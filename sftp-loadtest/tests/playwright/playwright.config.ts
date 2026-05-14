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
  // Bug regressions are easier to read when failures keep going,
  // so we run sequentially. Tests are fast enough.
  fullyParallel: false,
  workers: 1,
  retries: process.env.CI ? 1 : 0,
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
      `${BIN} -addr 127.0.0.1:${PORT} -reports-dir ${REPORTS_DIR}`,
    ].join(' && '),
    cwd: REPO_ROOT,
    url: `http://127.0.0.1:${PORT}/healthz`,
    timeout: 60_000,
    reuseExistingServer: !process.env.CI,
    stdout: 'pipe',
    stderr: 'pipe',
  },
});
