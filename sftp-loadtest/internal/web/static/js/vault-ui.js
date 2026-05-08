// vault-ui.js — visible affordances for the encrypted secret vault.
// Mounts a status pill in the topbar (between host info and run
// controls) showing one of: "vault: not used", "vault locked",
// "vault open · N secrets". Click → opens a panel with the ref
// list + delete buttons + lock + change-passphrase actions.
//
// Public surface:
//   mountVaultStatus()  — adds the topbar pill + binds the panel.
//
// Polls /api/vault/status every 5 s so a /lock from another
// surface (auto-lock timer, ⌘K command) reflects here without a
// reload.

import { vaultStatus, unlockVault, lockVault, listRefs, deleteSecret, changeMasterPassphrase, scanMigrations, applyMigrations } from './vault.js';
import { confirm as confirmModal } from './modal.js';
import { pushToast } from './toast.js';

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

  pill.addEventListener('click', async () => {
    const s = await vaultStatus();
    if (s.unsupported) return;
    if (!s.unlocked) {
      const ok = await unlockVault({ allowCreate: !s.exists });
      refresh();
      if (ok) await maybeOfferMigration();
      return;
    }
    // Already unlocked — open the management panel.
    openPanel();
  });

  // After every unlock, peek at the migration scan. If schedule
  // files still carry plaintext credentials, offer to move them
  // into the now-unlocked vault. One-shot per unlock; no
  // re-prompt on re-clicks.
  async function maybeOfferMigration() {
    const candidates = await scanMigrations();
    if (!candidates || candidates.length === 0) return;
    const fields = [...new Set(candidates.map((c) => c.field))].slice(0, 5).join(', ');
    const moreCount = Math.max(0, candidates.length - 5);
    const msg = `Found ${candidates.length} plaintext credential${candidates.length === 1 ? '' : 's'} in schedule files (${fields}${moreCount ? `, +${moreCount} more` : ''}). Move them into the encrypted vault now?\n\nAfter migration, schedules carry only opaque ${'$'}vault: refs; the actual credential lives encrypted-at-rest.`;
    const go = await confirmModal({
      title: 'Migrate plaintext schedule credentials?',
      message: msg,
      okLabel: `Move ${candidates.length} to vault`,
      cancelLabel: 'Not now',
    });
    if (!go) return;
    const r = await applyMigrations();
    const failed = r.failed && r.failed.length;
    if (r.migrated > 0 && !failed) {
      pushToast(`${r.migrated} secrets migrated to vault.`, 'success');
    } else if (r.migrated > 0 && failed) {
      pushToast(`${r.migrated} migrated; ${failed} failed (see console).`, 'warn');
      console.warn('vault migration failures:', r.failed);
    } else {
      pushToast('Migration could not move any secrets.', 'error');
      console.warn('vault migration failures:', r.failed || []);
    }
    refresh();
  }

  refresh();
  setInterval(refresh, POLL_MS);

  async function openPanel() {
    const refs = await listRefs();
    const bd = document.createElement('div');
    bd.className = 'modal-backdrop';
    bd.innerHTML = `
      <div class="modal-panel modal-panel-wide" role="dialog" aria-modal="true" aria-label="Stored secrets">
        <div class="modal-head">Stored secrets</div>
        <div class="modal-body">
          <p class="hint" style="margin:0 0 12px">Refs only — values stay encrypted on the server. Delete to revoke. Rotate the master passphrase whenever a teammate leaves the project.</p>
          <div class="vault-ref-list" data-role="ref-list"></div>
        </div>
        <div class="modal-foot" style="justify-content:space-between">
          <div style="display:inline-flex;gap:6px">
            <button type="button" class="btn btn-ghost" data-role="rotate">Rotate passphrase…</button>
            <button type="button" class="btn btn-ghost" data-role="lock">Lock vault</button>
          </div>
          <button type="button" class="btn btn-secondary" data-role="close">Close</button>
        </div>
      </div>`;
    document.body.appendChild(bd);

    const list = bd.querySelector('[data-role="ref-list"]');
    function renderList(items) {
      if (items.length === 0) {
        list.innerHTML = '<div class="hint" style="text-align:center;padding:24px 0;color:var(--text-tertiary)">No secrets stored yet. Save a connection or schedule with a password to populate the vault.</div>';
        return;
      }
      list.innerHTML = items.map((ref) => `
        <div class="vault-ref-row" data-ref="${escapeAttr(ref)}">
          <span class="mono vault-ref-name">${escapeHTML(ref)}</span>
          <button type="button" class="btn btn-sm btn-ghost" data-action="delete">Delete</button>
        </div>`).join('');
      list.querySelectorAll('[data-action="delete"]').forEach((btn) => {
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
          renderList((await listRefs()) || []);
          refresh();
        });
      });
    }
    renderList(refs);

    const close = () => { bd.remove(); refresh(); };
    bd.querySelector('[data-role="close"]').addEventListener('click', close);
    bd.querySelector('[data-role="lock"]').addEventListener('click', async () => {
      await lockVault();
      close();
    });
    bd.querySelector('[data-role="rotate"]').addEventListener('click', async () => {
      const ok = await changeMasterPassphrase();
      if (ok) {
        await confirmModal({
          title: 'Passphrase rotated',
          message: 'The vault has been re-encrypted under the new passphrase. Make sure you have it written down safely — there is no recovery.',
          okLabel: 'OK',
          cancelLabel: '',
        });
      }
    });
  }
}

function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}
function escapeAttr(s) { return escapeHTML(s); }
