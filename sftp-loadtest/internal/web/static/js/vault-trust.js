// vault-trust.js — encrypted-vault management surface inside the
// Trust view. The canonical home for all secret operations.
//
// Public surface:
//   mountVaultTrust(selector)   — fills [data-component="vault-trust"]
//                                 with status, action buttons, secrets list
//   openVaultInTrust({ unlock }) — programmatic navigator: switches to
//                                 the Trust view, scrolls the vault
//                                 panel into view, and (optionally)
//                                 immediately prompts for unlock if
//                                 the vault is locked.
//
// Both helpers are also published on `window.__sftpl_openVault` so any
// non-module surface (legacy.js, ad-hoc HTML "Trust → Vault" hint
// links) can call them without an import. Topbar pill, password-field
// hints, and modal "Manage in Trust" footers all funnel through
// this entry point so the panel below is the one true place for
// vault management.

import {
  vaultStatus,
  unlockVault,
  lockVault,
  listRefs,
  deleteSecret,
  changeMasterPassphrase,
  scanMigrations,
  applyMigrations,
} from './vault.js';
import { confirm as confirmModal } from './modal.js';
import { pushToast } from './toast.js';

const POLL_MS = 5000;

let mounted = false;

export function mountVaultTrust(selector) {
  const root = document.querySelector(selector);
  if (!root) return;
  if (mounted) return;
  mounted = true;

  const body = root.querySelector('[data-role="content"]');
  const badge = root.querySelector('[data-role="vault-state-badge"]');
  if (!body) return;

  body.innerHTML = `
    <div class="vault-trust-status" data-role="status-row">
      <div class="vault-trust-state">
        <span class="vault-trust-led" data-role="state-led" aria-hidden="true"></span>
        <div class="vault-trust-state-text">
          <div class="vault-trust-state-headline" data-role="state-headline">Checking vault…</div>
          <div class="vault-trust-state-detail" data-role="state-detail">Probing /api/vault/status</div>
        </div>
      </div>
      <div class="vault-trust-actions" data-role="primary-actions"></div>
    </div>

    <div class="vault-trust-migration" data-role="migration-banner" hidden>
      <div class="vault-trust-migration-text" data-role="migration-text"></div>
      <div class="vault-trust-migration-actions">
        <button type="button" class="btn btn-ghost btn-sm" data-role="migration-dismiss">Not now</button>
        <button type="button" class="btn btn-primary btn-sm" data-role="migration-apply">Move to vault</button>
      </div>
    </div>

    <div class="vault-trust-secrets" data-role="secrets-block" hidden>
      <div class="vault-trust-secrets-head">
        <span>Stored secrets</span>
        <span class="hint mono" data-role="secrets-count">0</span>
      </div>
      <div class="vault-ref-list" data-role="ref-list"></div>
      <div class="vault-trust-secrets-foot">
        <button type="button" class="btn btn-ghost btn-sm" data-role="rotate">Rotate master passphrase…</button>
        <button type="button" class="btn btn-ghost btn-sm" data-role="lock">Lock vault</button>
      </div>
    </div>

    <p class="hint vault-trust-foot">
      Forgot the master passphrase? There is no recovery — by design. Delete the vault file
      from <code class="mono" data-role="vault-path">your reports directory</code> to start a new
      one (this revokes every saved secret in it).
    </p>
  `;

  const ledEl = body.querySelector('[data-role="state-led"]');
  const headlineEl = body.querySelector('[data-role="state-headline"]');
  const detailEl = body.querySelector('[data-role="state-detail"]');
  const actionsEl = body.querySelector('[data-role="primary-actions"]');
  const secretsBlock = body.querySelector('[data-role="secrets-block"]');
  const refList = body.querySelector('[data-role="ref-list"]');
  const secretsCount = body.querySelector('[data-role="secrets-count"]');
  const migrationBanner = body.querySelector('[data-role="migration-banner"]');
  const migrationText = body.querySelector('[data-role="migration-text"]');
  const pathEl = body.querySelector('[data-role="vault-path"]');

  body.querySelector('[data-role="lock"]').addEventListener('click', async () => {
    await lockVault();
    pushToast('Vault locked.', 'success');
    refresh();
  });
  body.querySelector('[data-role="rotate"]').addEventListener('click', async () => {
    const ok = await changeMasterPassphrase();
    if (ok) pushToast('Master passphrase rotated.', 'success');
    refresh();
  });
  body.querySelector('[data-role="migration-dismiss"]').addEventListener('click', () => {
    migrationBanner.hidden = true;
  });
  body.querySelector('[data-role="migration-apply"]').addEventListener('click', async () => {
    const r = await applyMigrations();
    const moved = Number(r.migrated || 0);
    const failed = Array.isArray(r.failed) ? r.failed.length : 0;
    if (moved > 0 && !failed) pushToast(`${moved} secrets migrated to vault.`, 'success');
    else if (moved > 0) pushToast(`${moved} migrated; ${failed} failed (see console).`, 'warn');
    else pushToast('Migration could not move any secrets.', 'error');
    if (failed) console.warn('vault migration failures:', r.failed);
    migrationBanner.hidden = true;
    refresh();
  });

  function setBadge(label, state) {
    if (!badge) return;
    badge.textContent = label;
    badge.dataset.state = state;
  }

  async function refresh() {
    let s;
    try { s = await vaultStatus(); }
    catch { s = { unsupported: true }; }

    if (s.unsupported) {
      ledEl.dataset.state = 'unsupported';
      headlineEl.textContent = 'Vault unavailable on this server';
      detailEl.textContent = 'The server was started without a writable reports directory, so no vault path is configured. Restart with -reports-dir set to enable the encrypted vault.';
      actionsEl.innerHTML = '';
      secretsBlock.hidden = true;
      migrationBanner.hidden = true;
      setBadge('unavailable', 'absent');
      return;
    }

    if (pathEl && s.path) pathEl.textContent = s.path;

    if (!s.exists) {
      ledEl.dataset.state = 'absent';
      headlineEl.textContent = 'No vault yet — set one up';
      detailEl.textContent = 'Pick a master passphrase and an encrypted vault file is created in your reports directory. Argon2id derives the encryption key — Argon2id memory cost 64 MiB, 3 iterations, parallelism 4.';
      actionsEl.innerHTML = `<button type="button" class="btn btn-primary" data-role="create">Create vault…</button>`;
      actionsEl.querySelector('[data-role="create"]').addEventListener('click', async () => {
        const ok = await unlockVault({ allowCreate: true });
        if (ok) {
          pushToast('Vault created and unlocked.', 'success');
          await maybeOfferMigration();
        }
        refresh();
      });
      secretsBlock.hidden = true;
      migrationBanner.hidden = true;
      setBadge('not set up', 'absent');
      return;
    }

    if (!s.unlocked) {
      ledEl.dataset.state = 'locked';
      headlineEl.textContent = 'Vault is locked';
      detailEl.textContent = `Vault file present at ${s.path || 'reports directory'}. Enter the master passphrase to unlock — auto-locks again after the configured idle window.`;
      actionsEl.innerHTML = `<button type="button" class="btn btn-primary" data-role="unlock">Unlock vault…</button>`;
      actionsEl.querySelector('[data-role="unlock"]').addEventListener('click', async () => {
        const ok = await unlockVault({ allowCreate: false });
        if (ok) {
          pushToast('Vault unlocked.', 'success');
          await maybeOfferMigration();
        }
        refresh();
      });
      secretsBlock.hidden = true;
      migrationBanner.hidden = true;
      setBadge('locked', 'locked');
      return;
    }

    // Unlocked — show secrets.
    ledEl.dataset.state = 'open';
    const n = Number(s.count || 0);
    headlineEl.textContent = `Vault is open — ${n} secret${n === 1 ? '' : 's'} stored`;
    detailEl.textContent = `Encrypted-at-rest with ChaCha20-Poly1305. Auto-locks after the server-side idle timeout. Refs below are pointer-only — actual plaintext stays on the server.`;
    actionsEl.innerHTML = '';
    secretsBlock.hidden = false;
    secretsCount.textContent = `${n} stored`;
    setBadge(`${n} stored`, 'open');
    const refs = await listRefs();
    renderRefs(refs);
    await maybeOfferMigration();
  }

  function renderRefs(items) {
    if (!items || items.length === 0) {
      refList.innerHTML = `<div class="hint vault-trust-empty">No secrets stored yet. Save a connection or schedule with a password and pick "Encrypted vault" — the ref will appear here.</div>`;
      return;
    }
    refList.innerHTML = items.map((ref) => `
      <div class="vault-ref-row" data-ref="${escapeAttr(ref)}">
        <span class="mono vault-ref-name">${escapeHTML(ref)}</span>
        <button type="button" class="btn btn-sm btn-ghost" data-action="delete">Delete</button>
      </div>`).join('');
    refList.querySelectorAll('[data-action="delete"]').forEach((btn) => {
      btn.addEventListener('click', async () => {
        const row = btn.closest('.vault-ref-row');
        const ref = row.dataset.ref;
        const ok = await confirmModal({
          title: 'Delete this secret?',
          message: `This permanently removes "${ref}" from the vault. Any saved connection / schedule that referenced it will fall back to the operator typing the password again.`,
          okLabel: 'Delete',
          cancelLabel: 'Cancel',
          danger: true,
        });
        if (!ok) return;
        await deleteSecret(ref);
        refresh();
      });
    });
  }

  async function maybeOfferMigration() {
    const candidates = await scanMigrations();
    if (!candidates || candidates.length === 0) {
      migrationBanner.hidden = true;
      return;
    }
    const fields = [...new Set(candidates.map((c) => c.field))].slice(0, 5).join(', ');
    const moreCount = Math.max(0, candidates.length - 5);
    migrationText.innerHTML = `<strong>${candidates.length} plaintext credential${candidates.length === 1 ? '' : 's'}</strong> still live in schedule files (${escapeHTML(fields)}${moreCount ? `, +${moreCount} more` : ''}). Move them into the encrypted vault — schedules then reference opaque <code class="mono">$vault:</code> markers and plaintext leaves disk.`;
    migrationBanner.hidden = false;
  }

  refresh();
  setInterval(refresh, POLL_MS);

  // Re-refresh whenever vault state changes elsewhere (topbar pill,
  // saved-connection store, modal flows). Listeners hold on to the
  // outer `refresh` closure so any caller can dispatch the event
  // without importing the module.
  document.addEventListener('sftpl:vault-changed', refresh);
}

// openVaultInTrust — single canonical "take me to my vault" entry
// point. Switches to the Trust view, scrolls the panel into view,
// and (optionally) auto-prompts for unlock if the vault is locked.
// Used by the topbar pill, the password-field "Trust → Vault" hints,
// the saved-connection post-save prompt, and the legacy fallback.
export async function openVaultInTrust({ unlock = false } = {}) {
  // Switch view via the existing sidebar wiring (no private API).
  const trustRow = document.querySelector('.shell-sidebar-row[data-view="trust"]');
  if (trustRow) trustRow.click();

  // Wait one frame so the view transition + reparenting settles.
  await new Promise((r) => requestAnimationFrame(r));

  const panel = document.querySelector('[data-component="vault-trust"]');
  if (panel && panel.scrollIntoView) {
    panel.scrollIntoView({ behavior: 'smooth', block: 'start' });
    panel.classList.add('vault-trust-flash');
    setTimeout(() => panel.classList.remove('vault-trust-flash'), 1400);
  }

  if (unlock) {
    let s;
    try { s = await vaultStatus(); } catch { s = null; }
    if (s && !s.unsupported && (!s.exists || !s.unlocked)) {
      const ok = await unlockVault({ allowCreate: !s.exists });
      if (ok) document.dispatchEvent(new CustomEvent('sftpl:vault-changed'));
    }
  }
}

// Wire any [data-role="open-vault"] link in the document to navigate
// here. Runs once at module load. Safe to call from anywhere — newly
// added links pick up via event delegation on document.
function wireVaultLinks() {
  document.addEventListener('click', (ev) => {
    const link = ev.target.closest('[data-role="open-vault"]');
    if (!link) return;
    ev.preventDefault();
    openVaultInTrust({ unlock: true });
  });
}
wireVaultLinks();

// Publish on window for non-module callers (legacy.js, dynamic
// HTML inserts, palette commands).
window.__sftpl_openVault = openVaultInTrust;

function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}
function escapeAttr(s) { return escapeHTML(s); }
