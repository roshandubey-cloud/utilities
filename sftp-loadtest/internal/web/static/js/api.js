// api.js — single entry point for every server call.
// Always sends X-Requested-With (CSRFGuard) and Accept: application/json so
// the browser doesn't render an HTML error page mid-parse. Used by every
// frontend module — never call fetch() directly.

export async function apiFetch(url, init = {}) {
  init.headers = Object.assign(
    {
      'X-Requested-With': 'sftp-loadtest',
      'Accept': 'application/json',
    },
    init.headers || {}
  );
  return fetch(url, init);
}

export async function apiJSON(url, init = {}) {
  const res = await apiFetch(url, init);
  if (!res.ok) {
    const text = await res.text().catch(() => '');
    throw Object.assign(new Error(`HTTP ${res.status}`), { status: res.status, body: text });
  }
  return res.json();
}

export async function apiPostJSON(url, body) {
  return apiJSON(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
}
