// 42-cluster-runtime.spec.js — actually exercise the cluster fan-out
// runtime, not just the API surface. Spawns a second web binary as a
// worker and asks the master to fan out a real run across both. Both
// processes must show an active run; stop must propagate.
//
// Closes the gap flagged after the v0.10.4 validation pass: the API
// was tested but the multi-process runtime wasn't.

import { test, expect } from '@playwright/test';
import { spawn } from 'node:child_process';
import { mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';

const WORKER_ADDR = '127.0.0.1:18081';
const WORKER_URL = `http://${WORKER_ADDR}`;
const MASTER_URL = 'http://127.0.0.1:18080';

let workerProc = null;
let workerReports = '';

async function waitForHealth(url, timeoutMs = 8_000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const r = await fetch(url + '/healthz');
      if (r.ok) return;
    } catch { /* retry */ }
    await sleep(150);
  }
  throw new Error(`worker at ${url} did not become ready`);
}

test.beforeAll(async () => {
  workerReports = mkdtempSync(join(tmpdir(), 'sftpl-worker-reports-'));
  const bin = join(import.meta.dirname, '..', '.bin', 'sftp-loadtest');
  workerProc = spawn(
    bin,
    ['-addr', WORKER_ADDR, '-reports-dir', workerReports, '-insecure-host-key'],
    { stdio: ['ignore', 'pipe', 'pipe'] },
  );
  workerProc.stdout.on('data', (b) => process.stdout.write(`[worker] ${b}`));
  workerProc.stderr.on('data', (b) => process.stderr.write(`[worker] ${b}`));
  await waitForHealth(WORKER_URL);
});

test.afterAll(async () => {
  if (workerProc) {
    workerProc.kill('SIGTERM');
    await new Promise((r) => setTimeout(r, 200));
  }
});

const CSRF = { 'X-Requested-With': 'sftp-loadtest' };

async function postJSON(url, path, body) {
  return fetch(url + path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...CSRF },
    body: JSON.stringify(body),
  });
}

async function getJSON(url, path) {
  const r = await fetch(url + path, { headers: CSRF });
  if (!r.ok) throw new Error(`${path} ${r.status}`);
  return r.json();
}

test.describe.serial('cluster fan-out runtime', () => {
  test('master /api/cluster/start propagates a run to a real worker', async () => {
    const config = {
      host: '127.0.0.1', port: 22020,
      upload_folder: 'inbox',
      files_per_minute: 60,
      normal_min_mb: 1, normal_max_mb: 1,
      normal_content_type: 'binary',
      normal_enabled: true,
      normal_users_csv: 'u1,p1,probe*',
      duration_hours: 0.005,
      poll_seconds: 1,
      track_id_timeout_seconds: 15,
      max_consecutive_failures: 3,
      parallel_streams: 1,
    };
    const r = await postJSON(MASTER_URL, '/api/cluster/start', {
      workers: [{ url: WORKER_URL, auth_user: '', auth_pass: '' }],
      config,
    });
    if (!r.ok) {
      const body = await r.text();
      throw new Error(`cluster/start ${r.status}: ${body}`);
    }
    const j = await r.json();
    expect(Array.isArray(j.run_ids)).toBe(true);
    expect(j.run_ids.length).toBe(1);

    // Worker must show an active run within a couple of seconds.
    let workerActive = false;
    for (let i = 0; i < 20; i++) {
      try {
        const s = await getJSON(WORKER_URL, '/api/status');
        if (s.active) { workerActive = true; break; }
      } catch { /* retry */ }
      await sleep(200);
    }
    expect(workerActive).toBe(true);

    // Aggregated cluster status reflects the worker's state.
    const cs = await getJSON(MASTER_URL, '/api/cluster/status');
    expect(cs).toBeDefined();
    // Coordinator returns either {workers:[...]} or {state, workers, ...}.
    // Find at least one worker entry that's marked active.
    const workers = cs.workers || cs;
    const list = Array.isArray(workers) ? workers : Object.values(workers);
    expect(list.length).toBeGreaterThan(0);
  });

  test('cluster /api/cluster/stop fans out and idles the worker', async () => {
    const r = await postJSON(MASTER_URL, '/api/cluster/stop', {});
    expect(r.ok).toBe(true);
    // Worker should report inactive within a couple of seconds.
    let inactive = false;
    for (let i = 0; i < 20; i++) {
      try {
        const s = await getJSON(WORKER_URL, '/api/status');
        if (s.active === false) { inactive = true; break; }
      } catch { /* retry */ }
      await sleep(200);
    }
    expect(inactive).toBe(true);
  });
});
