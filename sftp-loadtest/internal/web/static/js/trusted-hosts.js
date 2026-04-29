// trusted-hosts.js — Trusted SSH host keys panel.
//
// Lists every entry the tool currently trusts (one row per host:port) with
// fingerprint and key type, and a Forget button that removes the entry.
// Removing a host means the next connection to it triggers the same
// trust-on-first-use prompt /api/probe and /api/start already use, so the
// operator stays in full control of their host-key state from the UI.
//
// Pollable: refreshes every 8s so a probe-driven add or another tab's
// remove shows up promptly.

import { apiFetch } from './api.js';
import { pushToast } from './toast.js';

// Trusted hosts changes the moment an operator accepts a TOFU prompt
// or clicks Forget; an 8 s poll left the panel showing stale state for
// most of that interval. 3 s feels instantaneous to a human and the
// request rate is still trivial.
const REFRESH_MS = 3000;

export function mountTrustedHosts(rootSelector) {
  const root = document.querySelector(rootSelector);
  if (!root) return;
  const slot = root.querySelector('[data-role="content"]');
  const counter = root.querySelector('[data-role="count"]');

  // No wizard tag: trusted hosts is reference data the operator may
  // want to manage at any point (e.g. before clicking Start to forget a
  // server they no longer trust). Always visible.

  async function refresh() {
    try {
      const res = await apiFetch('/api/hostkeys');
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const j = await res.json();
      render(j);
    } catch {
      if (counter) counter.textContent = 'disconnected';
    } finally {
      setTimeout(refresh, REFRESH_MS);
    }
  }

  function render(j) {
    const hosts = Array.isArray(j.hosts) ? j.hosts : [];
    if (j.mode === 'file') {
      slot.innerHTML = `
        <div class="trusted-hosts-empty">
          <div class="body-secondary">Trust is managed externally via the OpenSSH known_hosts file at <span class="mono">${escapeHTML(j.path || '')}</span>.</div>
          <div class="body-small" style="color:var(--text-tertiary); margin-top:6px">Edit that file directly to add or remove entries; restart the tool for changes to take effect.</div>
        </div>`;
      if (counter) counter.textContent = 'file mode';
      return;
    }
    if (counter) {
      counter.textContent = hosts.length === 0 ? 'no trusted hosts yet'
        : (hosts.length === 1 ? '1 trusted host' : `${hosts.length} trusted hosts`);
    }
    if (hosts.length === 0) {
      slot.innerHTML = `
        <div class="trusted-hosts-empty">
          <div class="body-secondary">No host keys trusted yet.</div>
          <div class="body-small" style="color:var(--text-tertiary); margin-top:6px">The first time you connect to an SFTP server, you'll be prompted to verify and trust its key. Accepted keys appear here.</div>
        </div>`;
      return;
    }
    slot.innerHTML = `
      <ul class="trusted-hosts-list">
        ${hosts.map(rowMarkup).join('')}
      </ul>`;
    slot.querySelectorAll('[data-action="forget"]').forEach((btn) => {
      btn.addEventListener('click', async (ev) => {
        ev.preventDefault();
        const host = btn.dataset.host;
        const port = Number(btn.dataset.port);
        if (!confirm(`Forget the trusted key for ${host}:${port}?\n\nThe next connection will prompt you to verify and trust the new key.`)) return;
        try {
          const res = await apiFetch('/api/hostkeys/remove', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ host, port }),
          });
          if (!res.ok) {
            const txt = await res.text();
            pushToast(`Could not remove: ${txt}`, 'error');
            return;
          }
          pushToast(`Forgot ${host}:${port}`, 'info', { timeout: 4000 });
          refresh();
        } catch (e) {
          pushToast(`Network error: ${e.message}`, 'error');
        }
      });
    });
  }

  refresh();
}

function rowMarkup(e) {
  const fp = String(e.fingerprint || '').replace(/^SHA256:/, '');
  const added = e.added_at ? formatAdded(e.added_at) : '';
  const tag = e.added_by ? `<span class="trusted-host-source">${escapeHTML(e.added_by)}</span>` : '';
  return `
    <li class="trusted-host">
      <div class="trusted-host-main">
        <div class="trusted-host-id mono">${escapeHTML(e.host)}<span class="trusted-host-port">:${e.port}</span></div>
        <div class="trusted-host-fp body-small mono" title="SHA-256 fingerprint">${escapeHTML(e.key_type || '')} · SHA256:${escapeHTML(fp)}</div>
        ${added ? `<div class="trusted-host-added body-small">added ${escapeHTML(added)} ${tag}</div>` : ''}
      </div>
      <div class="trusted-host-actions">
        <button class="btn btn-sm btn-ghost" type="button" data-action="forget" data-host="${escapeAttr(e.host)}" data-port="${e.port}">Forget</button>
      </div>
    </li>`;
}

function formatAdded(iso) {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const diffMs = Date.now() - d.getTime();
  if (diffMs < 60_000) return 'just now';
  if (diffMs < 3_600_000) return `${Math.floor(diffMs / 60_000)} min ago`;
  if (diffMs < 86_400_000) return `${Math.floor(diffMs / 3_600_000)} h ago`;
  return d.toLocaleString();
}

function escapeHTML(s) { return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c])); }
function escapeAttr(s) { return String(s).replace(/"/g, '&quot;'); }
