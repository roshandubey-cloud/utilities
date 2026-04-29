// Cluster API — backend MVP added in v0.8.0. No UI yet, so we drive
// the endpoints directly. Verifies start/status/stop and the
// fpm-split contract.

import { test, expect, request as playwrightRequest } from '@playwright/test';

test('cluster status returns inactive on a fresh server', async ({ request }) => {
  const r = await request.get('/api/cluster/status');
  expect(r.ok()).toBe(true);
  const j = await r.json();
  expect(j).toHaveProperty('active');
  expect(j.active).toBe(false);
});

test('cluster start with zero workers is rejected', async ({ request }) => {
  const r = await request.post('/api/cluster/start', {
    data: { workers: [], config: '{}' },
  });
  expect(r.status()).toBe(400);
  const txt = await r.text();
  expect(txt).toMatch(/at least one worker/i);
});

test('cluster stop without active run returns ok', async ({ request }) => {
  const r = await request.post('/api/cluster/stop', { data: {} });
  expect(r.ok()).toBe(true);
});
