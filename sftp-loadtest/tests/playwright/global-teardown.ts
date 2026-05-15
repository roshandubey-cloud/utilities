// global-teardown.ts — kill the mock SFTP + FTP servers we spawned
// in global-setup so the suite doesn't leave stray processes
// listening between runs.

import * as fs from 'node:fs';
import * as path from 'node:path';

const PID_FILE = path.join(__dirname, 'tmp', 'mocks.json');

export default async function globalTeardown() {
  if (!fs.existsSync(PID_FILE)) return;
  const { sftp, ftp } = JSON.parse(fs.readFileSync(PID_FILE, 'utf-8'));
  for (const pid of [sftp, ftp]) {
    if (!pid) continue;
    try { process.kill(pid, 'SIGTERM'); } catch { /* already gone */ }
  }
  // Give them a moment to release ports.
  await new Promise((r) => setTimeout(r, 200));
  fs.unlinkSync(PID_FILE);
}
