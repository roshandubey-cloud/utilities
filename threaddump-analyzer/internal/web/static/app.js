// app.js — orchestrates the three steps of the UI: create session, upload
// dumps, render findings. Everything goes through apiFetch() so the CSRF
// header is consistent. No frameworks, no bundle step.

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

$('newSession').addEventListener('click', async () => {
  const label = $('label').value.trim();
  const r = await apiFetch('/api/session', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ label }),
  });
  if (!r.ok) { showErr('newSession', `failed: ${await r.text()}`); return; }
  const j = await r.json();
  sessionID = j.id;
  $('sessLabel').textContent = `session ${j.label} (${j.id.slice(0, 8)}…)`;
  $('uploadCard').style.display = '';
});

$('uploadText').addEventListener('click', async () => {
  if (!sessionID) return;
  const text = $('dumpText').value;
  if (!text.trim()) { setMsg('please paste a dump first', 'err'); return; }
  await uploadOne(text);
  $('dumpText').value = '';
});

$('uploadFile').addEventListener('click', async () => {
  if (!sessionID) return;
  const files = Array.from($('dumpFile').files || []);
  if (files.length === 0) { setMsg('please pick at least one file', 'err'); return; }
  for (const f of files) {
    const text = await f.text();
    await uploadOne(text, f.name);
  }
  $('dumpFile').value = '';
});

async function uploadOne(text, filename) {
  setMsg(`uploading${filename ? ' ' + filename : ''}…`, 'meta');
  const r = await apiFetch(`/api/session/${sessionID}/upload`, {
    method: 'POST',
    headers: { 'Content-Type': 'text/plain' },
    body: text,
  });
  if (!r.ok) { setMsg(`upload failed: ${await r.text()}`, 'err'); return; }
  const j = await r.json();
  setMsg(`uploaded${filename ? ' ' + filename : ''}: ${j.threads} threads parsed (${j.dumps} dump(s) in session)`, 'ok');
  await refreshAnalysis();
}

function setMsg(text, kind) {
  const el = $('uploadMsg');
  el.textContent = text;
  el.className = kind || 'meta';
}

function showErr(scope, msg) {
  console.error(scope, msg);
  setMsg(msg, 'err');
}

async function refreshAnalysis() {
  const fr = await apiFetch(`/api/session/${sessionID}/findings`).then(r => r.json()).catch(() => ({ findings: [] }));
  const ar = await apiFetch(`/api/session/${sessionID}/analysis`).then(r => r.json()).catch(() => null);
  $('analyseCard').style.display = '';
  renderFindings(fr.findings || []);
  if (ar) renderAnalysis(ar);
}

// Findings are the headline. We render one card per finding, in the order the
// server already sorted them — never re-sort in JS, so the operator always
// sees the same priority the API would.
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

  // States
  if (a.states && a.states.length) {
    root.appendChild(section('Thread state histogram', tableRows(['State', 'Count'], a.states.map(s => [s.state, s.count]))));
  }

  // Deadlocks
  if (a.deadlocks && a.deadlocks.length) {
    const cards = a.deadlocks.map((c, i) => `
      <div class="card">
        <strong>Cycle ${i + 1}:</strong> ${c.threads.map(escapeHTML).join(' → ')}
        <pre>${c.locks.map(l => `${l.id}  (${l.class})`).map(escapeHTML).join('\n')}</pre>
      </div>`).join('');
    root.appendChild(section('Deadlocks', html(cards)));
  }

  // Pools
  if (a.pools && a.pools.length) {
    const rows = a.pools.map(p => [p.pool, p.threads, p.blocked_pct.toFixed(1) + '%']);
    root.appendChild(section('Pools', tableRows(['Pool', 'Threads', 'Blocked %'], rows)));
  }

  // Contention
  if (a.contention && a.contention.length) {
    const rows = a.contention.map(c => [c.lock.id, c.lock.class, c.holder || '—', c.waiters.length]);
    root.appendChild(section('Top contention', tableRows(['Lock ID', 'Class', 'Holder', '# Waiters'], rows)));
  }

  // Lifelines: only relevant when there's more than one dump
  if (a.lifelines && a.lifelines.length && a.lifelines.some(l => l.signature_run_max >= 2)) {
    const frozen = a.lifelines.filter(l => l.signature_run_max >= 2).slice(0, 20);
    const rows = frozen.map(l => [l.key.name, l.pool || '(unclassified)', l.signature_run_max]);
    root.appendChild(section('Frozen threads (top 20)', tableRows(['Thread', 'Pool', 'Consecutive frozen dumps'], rows)));
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
  const thead = '<thead><tr>' + headers.map(h => `<th>${escapeHTML(h)}</th>`).join('') + '</tr></thead>';
  const tbody = '<tbody>' + rows.map(r => '<tr>' + r.map(c => `<td>${escapeHTML(String(c))}</td>`).join('') + '</tr>').join('') + '</tbody>';
  t.innerHTML = thead + tbody;
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
