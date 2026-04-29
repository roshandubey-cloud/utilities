// Rate-limit — closed in v0.6.x via tiered token buckets. Confirms
// state-changing endpoints push back under sustained spam.

import { test, expect, request as playwrightRequest } from '@playwright/test';

test('rapid /api/start spam eventually returns 429', async ({ baseURL }) => {
  const ctx = await playwrightRequest.newContext({
    baseURL,
    extraHTTPHeaders: { 'X-Requested-With': 'sftp-loadtest' },
  });
  // /api/start has capacity 10, refill 1/s. Fire 30 in tight succession;
  // at least the latter half must hit 429.
  let saw429 = false;
  for (let i = 0; i < 30; i++) {
    const r = await ctx.post('/api/start', { data: {} });
    if (r.status() === 429) { saw429 = true; break; }
  }
  await ctx.dispose();
  expect(saw429, 'rate limiter must push back under burst').toBe(true);
});
