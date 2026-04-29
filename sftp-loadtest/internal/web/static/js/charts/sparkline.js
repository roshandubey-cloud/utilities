// sparkline.js — pure-SVG, dependency-free time-series renderer.
//
// Each chart is a self-contained DOM block: title row + KPI row + 60-second
// rolling SVG sparkline. We avoid Canvas (loses crispness on hi-DPI without
// special handling), avoid third-party charting libraries (build size,
// cross-version surface), and avoid DOM-per-data-point (rerenders OK at
// 1 fps with 100 ms paint budget).
//
// API:
//   const ch = createSparkline({ title, kpis, color, range })
//   ch.push(timestampMs, value)         // append a sample
//   ch.setKPI(name, formattedString)    // update a stat in the gutter
//   ch.element                          // attach this somewhere
//
// Range is auto-scaled with a sticky max so a one-time spike doesn't keep
// dwarfing a steady-state rate forever — the chart breathes.

const VIEW_W = 600;     // logical viewBox width — SVG scales to fit
const VIEW_H = 80;
const MAX_POINTS = 120; // samples retained; at 1s polling = 2 minutes

export function createSparkline({ title, kpis = [], color = 'var(--accent)', valueLabel = '' } = {}) {
  const root = document.createElement('div');
  root.className = 'workbench-chart';
  root.innerHTML = `
    <div class="workbench-chart-head">
      <div class="workbench-chart-title">${escapeHTML(title || '')}</div>
      <div class="workbench-chart-kpis" data-role="kpis"></div>
    </div>
    <div class="workbench-chart-body">
      <svg viewBox="0 0 ${VIEW_W} ${VIEW_H}" preserveAspectRatio="none" data-role="svg" aria-hidden="true">
        <path data-role="area" fill="${color}" fill-opacity="0.12"></path>
        <path data-role="line" stroke="${color}" stroke-width="1.5" fill="none" vector-effect="non-scaling-stroke"></path>
      </svg>
      <div class="workbench-chart-now" data-role="now"></div>
    </div>`;
  const kpiRoot = root.querySelector('[data-role="kpis"]');
  for (const k of kpis) {
    const cell = document.createElement('div');
    cell.className = 'workbench-chart-kpi';
    cell.dataset.kpi = k.name;
    cell.innerHTML = `<span class="kpi-label">${escapeHTML(k.label)}</span><span class="kpi-value" data-role="value">—</span>`;
    kpiRoot.appendChild(cell);
  }
  const linePath = root.querySelector('[data-role="line"]');
  const areaPath = root.querySelector('[data-role="area"]');
  const nowEl = root.querySelector('[data-role="now"]');

  const samples = []; // { t, v }
  let stickyMax = 0;
  const STICKY_DECAY = 0.992; // ~120s half-life

  function paint() {
    if (samples.length === 0) {
      linePath.setAttribute('d', '');
      areaPath.setAttribute('d', '');
      return;
    }
    // Y range: 0 to stickyMax (with a 12% headroom). Decay slowly so a
    // single peak doesn't pin the axis forever.
    const live = samples[samples.length - 1].v;
    const observedMax = samples.reduce((m, s) => Math.max(m, s.v), 0);
    if (observedMax > stickyMax) stickyMax = observedMax;
    else stickyMax = Math.max(observedMax, stickyMax * STICKY_DECAY);
    const maxY = Math.max(stickyMax * 1.12, 1);

    // X: oldest sample at x=0, newest at x=VIEW_W. Indexed evenly so
    // missing samples don't squish the timeline — operator perceives
    // "the recent N seconds" not absolute time.
    const n = samples.length;
    const dx = n > 1 ? VIEW_W / (n - 1) : 0;
    const points = samples.map((s, i) => {
      const x = i * dx;
      const y = VIEW_H - (s.v / maxY) * (VIEW_H - 4) - 2;
      return [x, Math.max(2, Math.min(VIEW_H - 2, y))];
    });
    let d = `M ${points[0][0].toFixed(1)} ${points[0][1].toFixed(1)}`;
    for (let i = 1; i < points.length; i++) {
      d += ` L ${points[i][0].toFixed(1)} ${points[i][1].toFixed(1)}`;
    }
    linePath.setAttribute('d', d);
    // Area = same path closed at the bottom of the viewBox.
    const last = points[points.length - 1];
    const first = points[0];
    areaPath.setAttribute('d', `${d} L ${last[0].toFixed(1)} ${VIEW_H} L ${first[0].toFixed(1)} ${VIEW_H} Z`);

    // "Now" badge sits at the right edge.
    nowEl.textContent = `${formatLive(live)}${valueLabel ? ' ' + valueLabel : ''}`;
  }

  function push(t, v) {
    if (typeof v !== 'number' || !isFinite(v) || v < 0) v = 0;
    samples.push({ t, v });
    if (samples.length > MAX_POINTS) samples.shift();
    paint();
  }

  function setKPI(name, formatted) {
    const cell = kpiRoot.querySelector(`[data-kpi="${name}"] [data-role="value"]`);
    if (cell) cell.textContent = formatted;
  }

  function reset() {
    samples.length = 0;
    stickyMax = 0;
    paint();
  }

  return { element: root, push, setKPI, reset };
}

function formatLive(v) {
  if (v >= 1000) return v.toFixed(0);
  if (v >= 100)  return v.toFixed(1);
  return v.toFixed(2);
}
function escapeHTML(s) { return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c])); }
