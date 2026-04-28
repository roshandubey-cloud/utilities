// start-preflight.js — wrap the legacy #startBtn so an unknown OR changed
// SSH host key never causes a Start Run to fail silently.
//
// Flow when the user clicks Start Run:
//   1. Capture-phase listener intercepts the click before the legacy handler.
//   2. POST /api/probe with trust_on_first_use=true (silent TOFU). Most cases
//      are "key already trusted" — the server returns ok:true and we proceed.
//   3. If the server replies requires_renewal (host key changed), show a
//      high-friction confirm() dialog with both fingerprints. On Accept,
//      POST /api/probe again with accept_changed=true so the server
//      overwrites the previous known_hosts entry and TOFU-adds the new key.
//   4. Once the probe succeeds, mark this host:port as cleared for the
//      session and re-trigger the original Start click — the legacy
//      handler now POSTs /api/start which dials cleanly.
//
// Without this wrapper the legacy Start path failed with "knownhosts: key
// is unknown" / "host key has changed" with no UI remediation.

import { apiPostJSON } from './api.js';
import { pushToast } from './toast.js';

const cleared = new Set(); // host:port pairs already pre-flighted this session
let inProgress = false;

export function mountStartPreflight() {
  const startBtn = document.getElementById('startBtn');
  if (!startBtn || startBtn.dataset.preflightWrapped) return;
  startBtn.dataset.preflightWrapped = '1';
  startBtn.addEventListener('click', onStartClick, true);
}

async function onStartClick(ev) {
  const startBtn = ev.currentTarget;
  if (inProgress) {
    ev.preventDefault();
    ev.stopImmediatePropagation();
    return;
  }
  const host = (document.getElementById('host')?.value || '').trim();
  const port = parseInt(document.getElementById('port')?.value || '0', 10);
  if (!host || !port) {
    // Let legacy handler surface the validation error its own way.
    return;
  }
  const key = `${host}:${port}`;
  if (cleared.has(key)) {
    // Already pre-flighted this combo this session; legacy handler proceeds.
    return;
  }
  // Block the legacy bubble-phase listener while we run the probe.
  ev.preventDefault();
  ev.stopImmediatePropagation();
  inProgress = true;
  startBtn.disabled = true;
  const previousLabel = startBtn.textContent;
  startBtn.textContent = 'Checking host key…';
  try {
    const cred = firstCredential();
    const probeBody = {
      host, port,
      username: cred.user,
      password: cred.pass,
      trust_on_first_use: true,
    };
    const res = await apiPostJSON('/api/probe', probeBody);
    if (res.ok) {
      cleared.add(key);
      restore(startBtn, previousLabel);
      // Re-trigger the click; this time the capture listener sees `cleared`
      // and lets the legacy handler run.
      startBtn.click();
      return;
    }
    if (res.requires_renewal) {
      const ok = window.confirm(
        `Host key has CHANGED for ${host}:${port}.\n\n` +
        `New fingerprint: ${res.captured_fingerprint}\n` +
        `Old fingerprint: ${res.captured_previous_fingerprint}\n\n` +
        `If this is a legitimate server rebuild or key rotation, click OK to overwrite the previous key.\n\n` +
        `If you cannot verify the new fingerprint out-of-band, click Cancel — accepting could allow a man-in-the-middle attack.`
      );
      if (!ok) {
        pushToast('Host key change rejected — run not started.', 'warn');
        restore(startBtn, previousLabel);
        return;
      }
      const renewed = await apiPostJSON('/api/probe', { ...probeBody, accept_changed: true });
      if (renewed.ok) {
        cleared.add(key);
        pushToast('New host key accepted.', 'success');
        restore(startBtn, previousLabel);
        startBtn.click();
        return;
      }
      pushToast(renewed.error || 'Failed to accept renewed host key', 'error');
      restore(startBtn, previousLabel);
      return;
    }
    if (res.requires_consent) {
      // TOFU=true was already sent; server still says consent needed.
      // Surface the message — usually means -known-hosts wasn't configured.
      pushToast(res.error || 'Host key consent required', 'warn');
      restore(startBtn, previousLabel);
      return;
    }
    // Other error — auth, refused, timeout, etc. Let the operator know
    // and keep the run from starting against a broken target.
    pushToast(res.error || 'Pre-flight check failed', 'error');
    restore(startBtn, previousLabel);
  } catch (e) {
    pushToast(`Pre-flight error: ${e.message || e}`, 'error');
    restore(startBtn, previousLabel);
  } finally {
    inProgress = false;
  }
}

function restore(btn, label) {
  btn.disabled = false;
  btn.textContent = label || 'Start run';
  inProgress = false;
}

// firstCredential picks the first user/password from any of the legacy CSV
// textareas — normal, large, or download. The pre-flight probe needs a real
// SSH credential to complete the handshake; if the operator hasn't filled
// any user list yet, we send an empty username and the probe stops at the
// TCP stage (which still flushes any new host key into the response).
function firstCredential() {
  const fields = ['normal_users', 'large_users', 'download_users'];
  for (const id of fields) {
    const ta = document.getElementById(id);
    if (!ta) continue;
    const raw = ta.dataset.editing === '1' ? ta.value : (ta.dataset.raw || ta.value || '');
    for (const line of raw.split('\n')) {
      const t = line.trim();
      if (!t) continue;
      const parts = t.split(',');
      if (parts.length >= 2 && parts[0].trim()) {
        return { user: parts[0].trim(), pass: parts[1] };
      }
    }
  }
  return { user: '', pass: '' };
}
