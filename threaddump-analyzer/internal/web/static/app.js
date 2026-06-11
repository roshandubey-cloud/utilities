// app.js — three-step UI: create session, upload artefacts (dumps + GC log
// + CPU sample), render findings + raw analysis. Everything goes through
// apiFetch() so the CSRF header is consistent. No frameworks.

const $ = (id) => document.getElementById(id);
let sessionID = null;

async function apiFetch(url, init) {
  init = init || {};
  init.headers = Object.assign({
    'X-Requested-With': 'threaddump-analyzer',
    'Accept': 'application/json',
  }, init.headers || {});
  return fetch(url, init);
}

// Tab switcher (paste / file pickers for dumps / GC log / CPU)
document.querySelectorAll('.tabs button').forEach(btn => {
  btn.addEventListener('click', () => {
    document.querySelectorAll('.tabs button').forEach(b => b.classList.toggle('on', b === btn));
    document.querySelectorAll('[data-pane]').forEach(p => {
      p.style.display = (p.getAttribute('data-pane') === btn.dataset.tab) ? '' : 'none';
    });
  });
});

$('newSession').addEventListener('click', async () => {
  const label = $('label').value.trim();
  const r = await apiFetch('/api/session', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ label }),
  });
  if (!r.ok) { setMsg('uploadMsg', `failed: ${await r.text()}`, 'err'); return; }
  const j = await r.json();
  sessionID = j.id;
  $('sessLabel').textContent = `session ${j.label} (${j.id.slice(0, 8)}…)`;
  $('uploadCard').style.display = '';
});

$('uploadText').addEventListener('click', () => uploadDumpText());
$('uploadFile').addEventListener('click', () => uploadDumpFiles());
$('uploadGclog').addEventListener('click', () => uploadAux('gclog', $('gclogText').value, 'gclogMsg'));
$('uploadCpu').addEventListener('click', () => uploadAux('cpu', $('cpuText').value, 'cpuMsg'));
$('uploadGclogFile').addEventListener('click', async () => {
  const f = ($('gclogFile').files || [])[0];
  if (!f) { setMsg('gclogMsg', 'pick a file first', 'err'); return; }
  await uploadAux('gclog', await f.text(), 'gclogMsg');
  $('gclogFile').value = '';
});
$('uploadCpuFile').addEventListener('click', async () => {
  const f = ($('cpuFile').files || [])[0];
  if (!f) { setMsg('cpuMsg', 'pick a file first', 'err'); return; }
  await uploadAux('cpu', await f.text(), 'cpuMsg');
  $('cpuFile').value = '';
});

async function uploadDumpText() {
  if (!sessionID) return;
  const text = $('dumpText').value;
  if (!text.trim()) { setMsg('uploadMsg', 'paste a dump first', 'err'); return; }
  await uploadDump(text);
  $('dumpText').value = '';
}

async function uploadDumpFiles() {
  if (!sessionID) return;
  const files = Array.from($('dumpFile').files || []);
  if (!files.length) { setMsg('uploadMsg', 'pick at least one file', 'err'); return; }
  for (const f of files) await uploadDump(await f.text(), f.name);
  $('dumpFile').value = '';
}

async function uploadDump(text, filename) {
  setMsg('uploadMsg', `uploading${filename ? ' ' + filename : ''}…`, 'meta');
  const r = await apiFetch(`/api/session/${sessionID}/upload`, {
    method: 'POST',
    headers: { 'Content-Type': 'text/plain' },
    body: text,
  });
  if (!r.ok) { setMsg('uploadMsg', `upload failed: ${await r.text()}`, 'err'); return; }
  const j = await r.json();
  setMsg('uploadMsg', `uploaded${filename ? ' ' + filename : ''}: ${j.threads} threads parsed (${j.dumps} dump(s) in session)`, 'ok');
  await refreshAnalysis();
}

async function uploadAux(kind, text, msgId) {
  if (!sessionID) return;
  if (!text.trim()) { setMsg(msgId, 'nothing to upload', 'err'); return; }
  setMsg(msgId, 'uploading…', 'meta');
  const path = kind === 'gclog' ? 'upload-gclog' : 'upload-cpu';
  const r = await apiFetch(`/api/session/${sessionID}/${path}`, {
    method: 'POST',
    headers: { 'Content-Type': 'text/plain' },
    body: text,
  });
  if (!r.ok) { setMsg(msgId, `failed: ${await r.text()}`, 'err'); return; }
  const j = await r.json();
  if (kind === 'gclog') {
    setMsg(msgId, `parsed ${j.stats.pauses} GC pauses; total ${(j.stats.total_duration/1e9).toFixed(2)}s; max ${(j.stats.max_duration/1e6).toFixed(1)}ms`, 'ok');
  } else {
    setMsg(msgId, `parsed ${j.rows} threads from ${j.source}`, 'ok');
  }
  await refreshAnalysis();
}

function setMsg(id, text, kind) {
  const el = $(id);
  if (!el) return;
  el.textContent = text;
  el.className = kind || 'meta';
}

async function refreshAnalysis() {
  const fr = await apiFetch(`/api/session/${sessionID}/findings`).then(r => r.json()).catch(() => ({ findings: [] }));
  const ar = await apiFetch(`/api/session/${sessionID}/analysis`).then(r => r.json()).catch(() => null);
  $('analyseCard').style.display = '';
  renderFindings(fr.findings || []);
  if (ar) renderAnalysis(ar);
}

function renderFindings(fs) {
  $('dumpCount').textContent = fs.length === 0 ? 'no findings yet' : '';
  const root = $('findings');
  root.innerHTML = '';
  for (const f of fs) {
    const card = document.createElement('div');
    card.className = 'card';
    const sev = `sev-${f.severity}`;
    const evi = (f.evidence || []).map(e => `<li><code>${escapeHTML(e)}</code></li>`).join('');
    card.innerHTML = `
      <div class="head">
        <span><span class="${sev}">${f.severity.toUpperCase()}</span> · <span class="meta">${f.kind}</span> · <span class="meta">confidence ${f.confidence}%</span></span>
        <span class="meta">impact: ${f.impact_count}</span>
      </div>
      <h3 style="margin:6px 0 4px">${escapeHTML(f.headline)}</h3>
      <p style="margin:6px 0">${escapeHTML(f.detail || '')}</p>
      ${f.remediation ? `<p style="margin:6px 0"><strong>Remediation:</strong> ${escapeHTML(f.remediation)}</p>` : ''}
      ${evi ? `<details><summary class="meta">evidence (${(f.evidence || []).length} item(s))</summary><ul>${evi}</ul></details>` : ''}
    `;
    root.appendChild(card);
  }
}

function renderAnalysis(a) {
  const root = $('analysis');
  root.innerHTML = '';

  if (a.states && a.states.length) {
    root.appendChild(section('Thread state histogram', tableRows(['State', 'Count'], a.states.map(s => [s.state, s.count]))));
  }
  if (a.deadlocks && a.deadlocks.length) {
    const cards = a.deadlocks.map((c, i) => `
      <div class="card">
        <strong>Cycle ${i + 1}:</strong> ${c.threads.map(escapeHTML).join(' → ')}
        <pre>${c.locks.map(l => `${l.id}  (${l.class})`).map(escapeHTML).join('\n')}</pre>
      </div>`).join('');
    root.appendChild(section('Deadlocks', html(cards)));
  }
  if (a.predictions && a.predictions.length) {
    const cards = a.predictions.map(p => `
      <div class="card">
        <strong>Chain:</strong> ${p.chain.map(escapeHTML).join(' → ')}
        ${p.closer ? `<div class="meta">candidate closer thread: <b>${escapeHTML(p.closer)}</b></div>` : '<div class="meta">no closer candidate yet</div>'}
        <pre>${p.locks.map(l => `${l.id}  (${l.class})`).map(escapeHTML).join('\n')}</pre>
      </div>`).join('');
    root.appendChild(section('Predicted deadlocks (partial chains)', html(cards)));
  }
  if (a.progressions && a.progressions.length) {
    const top = a.progressions.slice(0, 10);
    const rows = top.map(p => [p.lock_id, p.lock_class || '—', p.peak_waiters, p.holder_stable ? 'yes' : 'no', (p.holders || []).filter(Boolean).join(' → ') || '—']);
    root.appendChild(section('Lock progression (top 10 by peak waiters)', tableRows(['Lock ID', 'Class', 'Peak waiters', 'Stable holder', 'Holders across dumps'], rows)));
  }
  if (a.pools && a.pools.length) {
    const rows = a.pools.map(p => [p.pool, p.threads, p.blocked_pct.toFixed(1) + '%']);
    root.appendChild(section('Pools', tableRows(['Pool', 'Threads', 'Blocked %'], rows)));
  }
  if (a.contention && a.contention.length) {
    const rows = a.contention.map(c => [c.lock.id, c.lock.class, c.holder || '—', c.waiters.length]);
    root.appendChild(section('Top contention', tableRows(['Lock ID', 'Class', 'Holder', '# Waiters'], rows)));
  }
  if (a.lifelines && a.lifelines.some(l => l.signature_run_max >= 2)) {
    const frozen = a.lifelines.filter(l => l.signature_run_max >= 2).slice(0, 20);
    const rows = frozen.map(l => [l.key.name, l.pool || '(unclassified)', l.signature_run_max]);
    root.appendChild(section('Frozen threads (top 20)', tableRows(['Thread', 'Pool', 'Consecutive frozen dumps'], rows)));
  }
  if (a.cpu_top && a.cpu_top.length) {
    const rows = a.cpu_top.slice(0, 10).map(c => [c.name, c.nid, c.percent.toFixed(1) + '%']);
    root.appendChild(section('Hot threads from CPU sample (top 10)', tableRows(['Thread', 'NID', '%CPU'], rows)));
  }
  if (a.gc_stats && a.gc_stats.pauses) {
    const s = a.gc_stats;
    root.appendChild(section('GC log', tableRows(['Metric', 'Value'], [
      ['Pauses', s.pauses],
      ['Total pause time', (s.total_duration/1e9).toFixed(2) + ' s'],
      ['Max pause', (s.max_duration/1e6).toFixed(1) + ' ms'],
      ['Full GC count', s.full_count],
      ['Mixed GC count', s.mixed_count],
      ['Young GC count', s.young_count],
    ])));
  }
}

function section(title, body) {
  const el = document.createElement('div');
  el.style.marginTop = '12px';
  el.innerHTML = `<h4>${escapeHTML(title)}</h4>`;
  el.appendChild(body);
  return el;
}

function tableRows(headers, rows) {
  const t = document.createElement('table');
  t.innerHTML = '<thead><tr>' + headers.map(h => `<th>${escapeHTML(h)}</th>`).join('') + '</tr></thead>' +
    '<tbody>' + rows.map(r => '<tr>' + r.map(c => `<td>${escapeHTML(String(c))}</td>`).join('') + '</tr>').join('') + '</tbody>';
  return t;
}

function html(s) {
  const wrap = document.createElement('div');
  wrap.innerHTML = s;
  return wrap;
}

function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, c => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  })[c]);
}
