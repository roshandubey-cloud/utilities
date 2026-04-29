// global-teardown.js — stop processes started by global-setup.

import { rmSync } from 'node:fs';

export default async function globalTeardown() {
  const procs = globalThis.__SFTPL_PROCS;
  if (!procs) return;
  for (const [name, p] of [['web', procs.web], ['mock', procs.mock]]) {
    try {
      p.kill('SIGTERM');
    } catch { /* already gone */ }
    console.log(`[teardown] sent SIGTERM to ${name} pid=${p.pid}`);
  }
  if (procs.reportsDir) {
    try { rmSync(procs.reportsDir, { recursive: true, force: true }); } catch {}
  }
}
