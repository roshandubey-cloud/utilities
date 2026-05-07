// file-upload.js — v0.19.19 helper that lets the operator load the
// contents of a local file into a textarea instead of copy-pasting.
// Used for the user-list CSVs (normal / large / download) and the
// SSH private key textarea, both of which used to require manual
// paste — clipboard handling on small screens or remote terminals
// makes that painful.
//
// Public surface: attachFileUpload(textareaId, opts)
//   - opts.accept   — file picker filter, e.g. ".csv,.txt" or ".pem,.key,*"
//   - opts.label    — button label (default "Upload from file")
//   - opts.maxBytes — refuse files larger than this (default 1 MiB)
//   - opts.onLoad   — optional callback(text) after successful load
//
// Side effects: dispatches input + change events on the textarea so any
// other listeners (validation, character counters) re-run.

const DEFAULT_MAX_BYTES = 1 * 1024 * 1024; // 1 MiB

export function attachFileUpload(textareaId, opts = {}) {
  const ta = document.getElementById(textareaId);
  if (!ta) return;
  // Idempotent — if a button is already present from a previous mount,
  // bail. The shell remounts a few components on view switch, and
  // doubling the button row would clutter the form.
  if (ta.dataset.fileUploadAttached === '1') return;
  ta.dataset.fileUploadAttached = '1';

  const accept   = opts.accept   || '.csv,.txt,.pem,.key';
  const label    = opts.label    || 'Upload from file';
  const maxBytes = opts.maxBytes || DEFAULT_MAX_BYTES;

  const wrap = document.createElement('div');
  wrap.className = 'file-upload-row';
  wrap.style.cssText = 'display:inline-flex;align-items:center;gap:6px;margin:4px 0 0';

  const input = document.createElement('input');
  input.type = 'file';
  input.accept = accept;
  input.style.display = 'none';

  const btn = document.createElement('button');
  btn.type = 'button';
  btn.className = 'btn-ghost btn-sm';
  btn.textContent = label;
  btn.title = `Load contents from a local file (max ${Math.round(maxBytes / 1024)} KiB)`;
  btn.addEventListener('click', () => input.click());

  const status = document.createElement('span');
  status.className = 'hint file-upload-status';
  status.style.cssText = 'font-size:var(--fs-11);color:var(--text-tertiary)';

  input.addEventListener('change', async () => {
    const f = input.files && input.files[0];
    if (!f) return;
    if (f.size > maxBytes) {
      status.textContent = `File too large (${(f.size / 1024).toFixed(1)} KiB > ${Math.round(maxBytes / 1024)} KiB).`;
      status.style.color = 'var(--danger-fg-soft)';
      input.value = '';
      return;
    }
    try {
      const text = await f.text();
      ta.value = text;
      ta.dispatchEvent(new Event('input', { bubbles: true }));
      ta.dispatchEvent(new Event('change', { bubbles: true }));
      status.textContent = `Loaded ${f.name} (${(f.size / 1024).toFixed(1)} KiB).`;
      status.style.color = 'var(--success-fg-soft)';
      if (typeof opts.onLoad === 'function') {
        try { opts.onLoad(text); } catch { /* swallow */ }
      }
    } catch (e) {
      status.textContent = `Read failed: ${e.message || e}`;
      status.style.color = 'var(--danger-fg-soft)';
    } finally {
      input.value = '';
    }
  });

  wrap.appendChild(input);
  wrap.appendChild(btn);
  wrap.appendChild(status);
  // Insert directly after the textarea so the button row is contextual
  // to the field it loads.
  ta.parentNode.insertBefore(wrap, ta.nextSibling);
}

// mountFileUploads wires every textarea that benefits from a load-from-
// file affordance. Called once from app.js after the form is in place.
export function mountFileUploads() {
  attachFileUpload('normal_users',   { accept: '.csv,.txt', label: 'Upload users CSV' });
  attachFileUpload('large_users',    { accept: '.csv,.txt', label: 'Upload users CSV' });
  attachFileUpload('download_users', { accept: '.csv,.txt', label: 'Upload users CSV' });
  attachFileUpload('conn-private-key', { accept: '.pem,.key,.pub,*', label: 'Upload key file', maxBytes: 256 * 1024 });
}
