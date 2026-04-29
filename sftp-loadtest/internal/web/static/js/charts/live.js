// live.js — α3 of the v0.9.0 redesign.
//
// Adds two real-time charts above the records panel: rolling throughput
// (achieved MB/s vs target file rate) and latency percentiles
// (p50/p95/p99 with COR overlay). Both consume /api/status — no backend
// changes required.
//
// The charts are visible whenever the user is on the main pane;
// historically the same data was text-only ("0 OVERALL MBPS" stat
// blocks). Putting it on a chart turns sftp-loadtest from "rows of
// numbers" into a real-time monitor.

import { apiFetch } from './../api.js';
import { createSparkline } from './sparkline.js';

const POLL_MS = 1000; // 1 Hz feels live; the API tolerates this trivially

export function mountLiveCharts(parentSelector) {
  const parent = document.querySelector(parentSelector);
  if (!parent) return;
  if (parent.querySelector('.workbench-charts')) return; // already mounted

  const container = document.createElement('div');
  container.className = 'workbench-charts';
  container.dataset.component = 'live-charts';

  // Throughput: MB/s — last sample, average over rolling window, peak.
  const throughput = createSparkline({
    title: 'Throughput',
    valueLabel: 'MB/s',
    color: 'var(--accent)',
    kpis: [
      { name: 'now',  label: 'now' },
      { name: 'peak', label: 'peak' },
      { name: 'fpm',  label: 'fpm' },
      { name: 'skips', label: 'skipped' },
    ],
  });

  // Latency: p99 plotted (the most user-visible signal) with p95/p50 in
  // KPI cells. p99.9 and COR-corrected appear too.
  const latency = createSparkline({
    title: 'Upload latency',
    valueLabel: 'ms',
    color: 'var(--info)',
    kpis: [
      { name: 'p50',  label: 'p50' },
      { name: 'p95',  label: 'p95' },
      { name: 'p99',  label: 'p99' },
      { name: 'p999', label: 'p99.9' },
      { name: 'cor',  label: 'COR p99' },
    ],
  });

  container.appendChild(throughput.element);
  container.appendChild(latency.element);
  // Insert the charts ABOVE the records panel so they're the first thing
  // the operator sees during a run.
  parent.insertBefore(container, parent.firstChild);

  // Track peak so the KPI persists across the rolling window.
  let throughputPeak = 0;
  let lastActive = false;

  async function tick() {
    try {
      const r = await apiFetch('/api/status');
      if (!r.ok) throw new Error();
      const j = await r.json();

      const m = j.metrics || {};
      const live = Number(m.last_minute_mbps || m.overall_mbps || 0);
      const overall = Number(m.overall_mbps || 0);
      const fpm = m.per_minute && m.per_minute.length
        ? m.per_minute[m.per_minute.length - 1].files
        : 0;

      // Reset peak when a new run starts.
      if (j.active && !lastActive) {
        throughputPeak = 0;
        throughput.reset();
        latency.reset();
      }
      lastActive = !!j.active;
      throughputPeak = Math.max(throughputPeak, live);

      throughput.push(Date.now(), live);
      throughput.setKPI('now',  `${live.toFixed(2)}`);
      throughput.setKPI('peak', `${throughputPeak.toFixed(2)}`);
      throughput.setKPI('fpm',  `${fpm}`);
      throughput.setKPI('skips', String(j.dispatch_skips || 0));

      const lat = j.latency || {};
      const up = lat.upload || null;
      const cor = lat.upload_cor || null;
      if (up) {
        const p99ms = (up.p99_ns || 0) / 1e6;
        latency.push(Date.now(), p99ms);
        latency.setKPI('p50',  formatMs(up.p50_ns));
        latency.setKPI('p95',  formatMs(up.p95_ns));
        latency.setKPI('p99',  formatMs(up.p99_ns));
        latency.setKPI('p999', formatMs(up.p999_ns));
      } else {
        latency.push(Date.now(), 0);
        latency.setKPI('p50', '—');
        latency.setKPI('p95', '—');
        latency.setKPI('p99', '—');
        latency.setKPI('p999', '—');
      }
      latency.setKPI('cor', cor ? formatMs(cor.p99_ns) : '—');

      // Hint at overall vs window divergence — the existing legacy code
      // already shows overall_mbps separately, so we're not duplicating.
      throughput.element.dataset.overallMbps = overall.toFixed(2);
    } catch {
      // network blip — keep the previous frame on screen
    } finally {
      setTimeout(tick, POLL_MS);
    }
  }
  tick();
}

function formatMs(ns) {
  if (ns == null) return '—';
  const ms = ns / 1e6;
  if (ms >= 1000) return `${(ms / 1000).toFixed(2)}s`;
  if (ms >= 100)  return `${ms.toFixed(0)}ms`;
  return `${ms.toFixed(1)}ms`;
}
