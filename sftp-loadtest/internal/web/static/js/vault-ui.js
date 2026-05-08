// vault-ui.js — topbar status pill for the encrypted secret vault.
// Shows one of: "vault: not set up", "vault: locked",
// "vault: N secrets". Clicking the pill navigates to Trust → Vault
// (the canonical management surface) and auto-prompts unlock /
// create when needed. The actual list / delete / rotate / lock
// affordances live in vault-trust.js, so this file only owns the
// always-visible status indicator.
//
// Public surface:
//   mountVaultStatus()  — adds the pill + starts the status poll.
//
// Polls /api/vault/status every 5 s so a /lock from another
// surface (auto-lock timer, ⌘K command, Trust panel) reflects
// here without a reload.

import { vaultStatus } from './vault.js';
import { openVaultInTrust } from './vault-trust.js';

const POLL_MS = 5000;

export function mountVaultStatus() {
  const topbar = document.querySelector('.shell-topbar');
  if (!topbar) return;
  if (topbar.querySelector('[data-role="vault-pill"]')) return; // idempotent

  // Inject pill BEFORE the run-controls span so the order reads
  // "host · vault · status · run". Falls through to append when
  // run-controls aren't found (rare; only on partial mounts).
  const pill = document.createElement('button');
  pill.type = 'button';
  pill.className = 'shell-topbar-vault';
  pill.dataset.role = 'vault-pill';
  pill.title = 'Encrypted secret vault — click to unlock / view stored secrets';
  pill.innerHTML = `
    <svg viewBox="0 0 16 16" width="12" height="12" fill="none" stroke="currentColor"
         stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <rect x="3" y="7" width="10" height="7" rx="1.2"/>
      <path d="M5 7V5a3 3 0 016 0v2"/>
    </svg>
    <span data-role="vault-label">vault</span>`;
  const runControls = topbar.querySelector('.shell-topbar-run-controls');
  if (runControls) topbar.insertBefore(pill, runControls);
  else topbar.appendChild(pill);

  const labelEl = pill.querySelector('[data-role="vault-label"]');

  async function refresh() {
    let s;
    try { s = await vaultStatus(); }
    catch { s = { unsupported: true }; }

    if (s.unsupported) {
      pill.style.display = 'none';
      return;
    }
    pill.style.display = '';
    if (!s.exists) {
      pill.dataset.state = 'absent';
      labelEl.textContent = 'vault: not set up';
    } else if (!s.unlocked) {
      pill.dataset.state = 'locked';
      labelEl.textContent = `vault: locked`;
    } else {
      pill.dataset.state = 'open';
      const n = Number(s.count || 0);
      labelEl.textContent = `vault: ${n} secret${n === 1 ? '' : 's'}`;
    }
  }

  // v0.20.3 — pill click now navigates to Trust → Vault (the
  // canonical management surface) instead of opening a separate
  // modal. When the vault is locked / absent we hand `unlock: true`
  // to openVaultInTrust so the operator gets the unlock / create
  // prompt immediately on arrival, without an extra click.
  pill.addEventListener('click', async () => {
    const s = await vaultStatus();
    if (s.unsupported) return;
    await openVaultInTrust({ unlock: !s.unlocked });
    refresh();
  });

  refresh();
  setInterval(refresh, POLL_MS);
}
