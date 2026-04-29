// CSRF guard — every state-changing endpoint must reject requests
// missing the X-Requested-With header. Adjacent tests already use the
// header globally; this spec strips it deliberately to confirm the
// guard is on every path we care about.

import { test, expect, request as playwrightRequest } from '@playwright/test';

test('CSRF guard rejects POST without X-Requested-With', async ({ baseURL }) => {
  // Important: explicitly set extraHTTPHeaders: {} — the global use
  // block in playwright.config.js sets X-Requested-With on every
  // request, and a context created with only `{ baseURL }` would
  // inherit that. We want a CLEAN session here.
  const ctx = await playwrightRequest.newContext({ baseURL, extraHTTPHeaders: {} });
  for (const path of [
    '/api/start',
    '/api/stop',
    '/api/probe',
    '/api/hostkeys/remove',
    '/api/cluster/start',
    '/api/cluster/stop',
  ]) {
    const r = await ctx.post(path, { data: {} });
    expect(r.status(), `${path} should require CSRF header`).toBe(403);
  }
  await ctx.dispose();
});

test('CSRF guard allows GET without X-Requested-With (read-only paths)', async ({ baseURL }) => {
  const ctx = await playwrightRequest.newContext({ baseURL, extraHTTPHeaders: {} });
  for (const path of ['/healthz', '/api/runs', '/api/host', '/api/status', '/api/hostkeys', '/api/cluster/status']) {
    const r = await ctx.get(path);
    expect(r.ok(), `${path} GET should not need CSRF`).toBe(true);
  }
  await ctx.dispose();
});
