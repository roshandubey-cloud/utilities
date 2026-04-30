// global-setup.js — start the web binary + mock SFTP server before tests run.
//
// We build the binary on each setup so the suite is always exercising the
// current source. The processes are recorded on globalThis so global-
// teardown can stop them. PIDs are also written to .e2e-pids for ad-hoc
// debugging when a setup fails.

import { spawn, spawnSync } from 'node:child_process';
import { mkdtempSync, writeFileSync, rmSync, existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';
import { generateKeyPairSync } from 'node:crypto';

const ROOT = join(import.meta.dirname, '..');
const BIN_WEB = join(import.meta.dirname, '.bin/sftp-loadtest');
const BIN_MOCK = join(import.meta.dirname, '.bin/mockserver');
const BIN_MOCK_FTP = join(import.meta.dirname, '.bin/mockftpserver');
const REPORTS_DIR = mkdtempSync(join(tmpdir(), 'sftpl-e2e-reports-'));
const PIDS_FILE = join(import.meta.dirname, '.e2e-pids');

function buildBinary(name, pkg, outPath) {
  console.log(`[setup] building ${name}...`);
  const r = spawnSync('go', ['build', '-o', outPath, pkg], {
    cwd: ROOT,
    stdio: 'inherit',
    env: process.env,
  });
  if (r.status !== 0) {
    throw new Error(`go build ${name} failed`);
  }
}

async function waitForHealth(url, timeoutMs = 10_000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const r = await fetch(url);
      if (r.ok) return;
    } catch { /* retry */ }
    await sleep(150);
  }
  throw new Error(`server at ${url} did not become ready in ${timeoutMs}ms`);
}

async function waitForTcp(host, port, timeoutMs = 5_000) {
  const { Socket } = await import('node:net');
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const ok = await new Promise((resolve) => {
      const s = new Socket();
      s.setTimeout(500);
      s.once('connect', () => { s.destroy(); resolve(true); });
      s.once('timeout', () => { s.destroy(); resolve(false); });
      s.once('error', () => { resolve(false); });
      s.connect(port, host);
    });
    if (ok) return;
    await sleep(150);
  }
  throw new Error(`tcp ${host}:${port} did not become ready`);
}

// generateTestKey returns a freshly-minted ed25519 keypair encoded as
// PKCS8 PEM. golang.org/x/crypto/ssh.ParsePrivateKey accepts both PKCS8
// and OpenSSH-format ed25519 keys, so PKCS8 from Node's crypto module
// is interoperable with the Go server without shelling out to ssh-keygen.
// We deliberately avoid the ssh-keygen path so the test rig works on
// machines that don't ship it (CI containers, locked-down boxes).
function generateTestKey() {
  const { privateKey } = generateKeyPairSync('ed25519', {
    privateKeyEncoding: { type: 'pkcs8', format: 'pem' },
    publicKeyEncoding: { type: 'spki', format: 'pem' },
  });
  const path = join(mkdtempSync(join(tmpdir(), 'sftpl-e2e-key-')), 'id_ed25519');
  writeFileSync(path, privateKey, { mode: 0o600 });
  return { pem: privateKey, path };
}

export default async function globalSetup() {
  buildBinary('mockserver', './cmd/mockserver', BIN_MOCK);
  buildBinary('mockftpserver', './cmd/mockftpserver', BIN_MOCK_FTP);
  buildBinary('web', '.', BIN_WEB);

  // Generate a per-suite ed25519 keypair so the new key-auth specs have
  // a well-formed PEM to feed the probe / start endpoints. The mock
  // server accepts ANY public key, so we don't need to register the
  // public half anywhere — only the client-side PEM matters.
  const testKey = generateTestKey();

  console.log('[setup] starting mock SFTP on 127.0.0.1:22020...');
  const mock = spawn(BIN_MOCK, ['-addr', '127.0.0.1:22020', '-trackid-delay', '50ms'], {
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  mock.stdout.on('data', (b) => process.stdout.write(`[mock] ${b}`));
  mock.stderr.on('data', (b) => process.stderr.write(`[mock] ${b}`));
  await waitForTcp('127.0.0.1', 22020);

  // Plain FTP + AUTH-TLS (explicit FTPS) on 127.0.0.1:22021. The
  // implicit-FTPS listener piggy-backs on the same process at 22022 so
  // the e2e suite has all three v0.13.0 transports without a third
  // child process.
  console.log('[setup] starting mock FTP/FTPS on 127.0.0.1:22021 (+ implicit on 22022)...');
  const mockftp = spawn(
    BIN_MOCK_FTP,
    [
      '-addr', '127.0.0.1:22021',
      '-trackid-delay', '50ms',
      '-explicit-tls',
      '-implicit-tls',
      '-implicit-addr', '127.0.0.1:22022',
    ],
    { stdio: ['ignore', 'pipe', 'pipe'] }
  );
  mockftp.stdout.on('data', (b) => process.stdout.write(`[mockftp] ${b}`));
  mockftp.stderr.on('data', (b) => process.stderr.write(`[mockftp] ${b}`));
  await waitForTcp('127.0.0.1', 22021);
  await waitForTcp('127.0.0.1', 22022);

  console.log('[setup] starting web on 127.0.0.1:18080...');
  const web = spawn(
    BIN_WEB,
    ['-addr', '127.0.0.1:18080', '-reports-dir', REPORTS_DIR, '-insecure-host-key'],
    { stdio: ['ignore', 'pipe', 'pipe'] }
  );
  web.stdout.on('data', (b) => process.stdout.write(`[web] ${b}`));
  web.stderr.on('data', (b) => process.stderr.write(`[web] ${b}`));
  await waitForHealth('http://127.0.0.1:18080/healthz');

  globalThis.__SFTPL_PROCS = { mock, mockftp, web, reportsDir: REPORTS_DIR };
  globalThis.__SFTPL_TESTKEY = testKey;
  // Forward the PEM into the per-test process via env. globalThis vars
  // set in global-setup don't reach worker processes — Playwright forks
  // a worker per file — so specs read process.env.SFTPL_TESTKEY_PEM.
  process.env.SFTPL_TESTKEY_PEM = testKey.pem;
  process.env.SFTPL_TESTKEY_PATH = testKey.path;
  writeFileSync(PIDS_FILE, `mock=${mock.pid}\nmockftp=${mockftp.pid}\nweb=${web.pid}\nreports=${REPORTS_DIR}\nkey=${testKey.path}\n`);
  console.log(`[setup] reports dir: ${REPORTS_DIR}`);
  console.log('[setup] ready.');
}
