// host.js — collapsed one-line host capacity bar.
// Pulls /api/host once on load + every 30 s. Renders into the elements with
// data-role="hostname|os|cores|ram|fdlimit|net" inside the host-bar component.

import { apiFetch } from './api.js';

const REFRESH_MS = 30_000;

export function mountHostBar(rootSelector) {
  const root = document.querySelector(rootSelector);
  if (!root) return;

  const set = (role, value) => {
    const el = root.querySelector(`[data-role="${role}"]`);
    if (el) el.textContent = value;
  };

  async function refresh() {
    try {
      const res = await apiFetch('/api/host');
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const j = await res.json();
      set('hostname', j.hostname || '—');
      set('os', `${j.os || '?'}/${j.arch || '?'}`);
      set('cores', j.num_cpu ? String(j.num_cpu) : '—');
      set('ram', formatRam(j.total_ram_mb));
      set('fdlimit', formatFD(j.fd_limit_soft, j.fd_limit_hard));

      // Optional: best NIC link speed (Linux exposes this; macOS/Windows often don't).
      const link = pickBestLink(j.network_interfaces);
      const netEl = root.querySelector('[data-role="net"]');
      if (netEl) {
        if (link) {
          netEl.textContent = `${formatLink(link)} link`;
          netEl.removeAttribute('hidden');
        } else {
          netEl.textContent = '';
          netEl.setAttribute('hidden', '');
        }
      }
    } catch (e) {
      set('hostname', '— offline');
    } finally {
      setTimeout(refresh, REFRESH_MS);
    }
  }
  refresh();
}

function formatRam(mb) {
  if (!mb) return '—';
  return mb >= 1024 ? `${(mb / 1024).toFixed(1)} GB` : `${mb} MB`;
}
// macOS reports the "unlimited" hard limit as math.MaxInt64
// (9,223,372,036,854,775,807). Anything above ~1 billion is effectively
// no limit; format that as ∞ so the host strip stops showing a 19-digit
// integer next to a comfortable 10,240.
const FD_UNLIMITED_THRESHOLD = 1_000_000_000;
function formatFD(soft, hard) {
  if (!soft) return '—';
  const fmt = (n) => (n >= FD_UNLIMITED_THRESHOLD ? '∞' : n.toLocaleString());
  if (hard && hard !== soft) return `${fmt(soft)} / ${fmt(hard)}`;
  return fmt(soft);
}
function pickBestLink(ifs) {
  if (!Array.isArray(ifs)) return null;
  let best = 0;
  for (const i of ifs) {
    if (i.link_mbps && i.link_mbps > best && i.link_mbps < 1e9) best = i.link_mbps;
  }
  return best || null;
}
function formatLink(mbps) {
  if (mbps >= 1000) return `${(mbps / 1000).toFixed(0)} Gb/s`;
  return `${mbps} Mb/s`;
}
