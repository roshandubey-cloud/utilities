// Playwright config for the sftp-loadtest UI E2E suite.
//
// The web binary is started by globalSetup with a freshly-built reportsDir
// so each run starts with a clean history panel. The mock SFTP server is
// also booted there so tests can drive a real upload flow end-to-end.
//
// We pin to Chromium for now — a headless evergreen Chrome is the lowest-
// noise way to validate the layout. WebKit / Firefox runs can be added
// later once the suite is stable.

import { defineConfig, devices } from '@playwright/test';

// The README-screenshots spec ships separately from the regular suite —
// it captures docs assets, takes ~30 s, and is only useful when re-baking
// the README artwork. Gate it behind CAPTURE=1 so the normal run stays
// fast and deterministic.
const captureMode = process.env.CAPTURE === '1';

export default defineConfig({
  testDir: './tests',
  outputDir: './test-results',
  testIgnore: captureMode ? [] : ['**/zz-readme-screenshots.spec.js'],
  fullyParallel: false,        // single shared server / mock — keep tests serial
  forbidOnly: true,
  retries: 0,
  workers: 1,
  reporter: [
    ['list'],
    ['html', { outputFolder: 'playwright-report', open: 'never' }],
    ['json', { outputFile: 'test-results/results.json' }],
  ],
  use: {
    baseURL: process.env.SFTPL_BASE_URL || 'http://127.0.0.1:18080',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    video: 'retain-on-failure',
    actionTimeout: 10_000,
    navigationTimeout: 15_000,
    extraHTTPHeaders: {
      // The web layer's CSRFGuard requires this on every state-changing
      // POST. Sending it on GETs is harmless; setting once globally is
      // simpler than per-test plumbing.
      'X-Requested-With': 'sftp-loadtest',
    },
  },
  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'] } },
  ],
  globalSetup: './global-setup.js',
  globalTeardown: './global-teardown.js',
});
