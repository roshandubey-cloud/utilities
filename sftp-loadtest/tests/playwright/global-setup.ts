// global-setup.ts — boot the mock SFTP + FTP servers so per-protocol
// specs can drive real probes and real runs.
//
// We compile both mock binaries once at suite start, spawn them on
// known ports, and tear them down in global-teardown. The PIDs are
// stashed via a writable temp file so teardown can stop them
// reliably even when individual workers crash mid-run.
//
// Ports (chosen far from any real service):
//   * mock SFTP        127.0.0.1:22222
//   * mock FTP (plain) 127.0.0.1:22121
//   * mock FTP (impl)  127.0.0.1:22990 (TLS-from-byte-0)

import { execSync, spawn } from 'node:child_process';
import * as fs from 'node:fs';
import * as path from 'node:path';
import * as net from 'node:net';

const REPO_ROOT = path.resolve(__dirname, '..', '..');
const TMP_DIR   = path.join(__dirname, 'tmp');
const PID_FILE  = path.join(TMP_DIR, 'mocks.json');

export const MOCK_SFTP_PORT    = 22222;
export const MOCK_FTP_PORT     = 22121;
export const MOCK_FTPS_PORT    = 22990;

async function waitForPort(host: string, port: number, timeoutMs = 8_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const ok = await new Promise<boolean>((resolve) => {
      const s = net.createConnection({ host, port });
      s.once('connect', () => { s.destroy(); resolve(true); });
      s.once('error', () => { s.destroy(); resolve(false); });
    });
    if (ok) return;
    await new Promise((r) => setTimeout(r, 200));
  }
  throw new Error(`mock server on ${host}:${port} never accepted a TCP connection`);
}

export default async function globalSetup() {
  fs.mkdirSync(TMP_DIR, { recursive: true });
  const sftpBin = path.join(TMP_DIR, 'mocksftp');
  const ftpBin  = path.join(TMP_DIR, 'mockftp');
  execSync(`go build -o ${sftpBin} ./cmd/mockserver`,     { cwd: REPO_ROOT, stdio: 'inherit' });
  execSync(`go build -o ${ftpBin}  ./cmd/mockftpserver`,  { cwd: REPO_ROOT, stdio: 'inherit' });

  const sftpLog = fs.openSync(path.join(TMP_DIR, 'mocksftp.log'), 'a');
  const ftpLog  = fs.openSync(path.join(TMP_DIR, 'mockftp.log'),  'a');

  // -trackid-delay tuned to 200ms (vs 2s default) so download-with-
  // trackid run specs don't have to wait 5+ s per file.
  const sftp = spawn(sftpBin,
    [`-addr=127.0.0.1:${MOCK_SFTP_PORT}`, '-trackid-delay=200ms', '-persist-content'],
    { detached: true, stdio: ['ignore', sftpLog, sftpLog] });
  sftp.unref();

  const ftp = spawn(ftpBin,
    [`-addr=127.0.0.1:${MOCK_FTP_PORT}`,
     '-trackid-delay=200ms', '-persist-content',
     '-explicit-tls', '-implicit-tls',
     `-implicit-addr=127.0.0.1:${MOCK_FTPS_PORT}`],
    { detached: true, stdio: ['ignore', ftpLog, ftpLog] });
  ftp.unref();

  fs.writeFileSync(PID_FILE, JSON.stringify({ sftp: sftp.pid, ftp: ftp.pid }));

  await waitForPort('127.0.0.1', MOCK_SFTP_PORT);
  await waitForPort('127.0.0.1', MOCK_FTP_PORT);
  await waitForPort('127.0.0.1', MOCK_FTPS_PORT);
}

export function pidFile() { return PID_FILE; }
