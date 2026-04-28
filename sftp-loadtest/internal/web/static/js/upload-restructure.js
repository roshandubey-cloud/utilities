// upload-restructure.js — Phase 5 IA fix.
//
// The legacy form had Normal-files, Large-files, Connection, and
// Schedule-and-config as four sibling cards. Folder lived in Connection
// (now hidden), so users couldn't see it next to the upload knobs they
// were configuring. Per Roshan's feedback: folder belongs INSIDE the
// upload card, and large-file mode is an *advanced* option under the
// upload card — not a separate top-level card.
//
// This module rearranges the DOM at runtime (no HTML edit, all legacy IDs
// preserved) so:
//   1. The upload card's header reads "Upload" (not "Normal load").
//   2. A "Folder (remote)" field is injected at the top of the upload card,
//      proxying to the legacy hidden #folder input.
//   3. The Large-files card's body content is reparented INTO the upload
//      card as a collapsible "Advanced — large file mode" disclosure.
//      The legacy outer largeCard wrapper is hidden.
// Every legacy input ID stays where downstream code expects it; only the
// visual layout changes.

export function mountUploadRestructure() {
  const normalCard = document.getElementById('normalCard');
  const largeCard  = document.getElementById('largeCard');
  if (!normalCard) return;
  if (normalCard.dataset.restructured) return;
  normalCard.dataset.restructured = '1';

  // ---- 1. Rename the upload card title ----
  const titleEl = normalCard.querySelector('header .toggle-label');
  if (titleEl) {
    // Keep the checkbox + style but replace the "Normal load" text node.
    const textNodes = Array.from(titleEl.childNodes).filter((n) => n.nodeType === 3);
    textNodes.forEach((n) => {
      if (/normal\s*load/i.test(n.textContent)) n.textContent = 'Upload';
    });
  }
  const pill = normalCard.querySelector('header .pill');
  if (pill) pill.textContent = 'files, sizes, users — required';

  // ---- 2. Inject a Folder (remote) field at the TOP of the upload body ----
  const body = normalCard.querySelector('.body');
  if (body) {
    const legacyFolder = document.getElementById('folder');
    const folderRow = document.createElement('div');
    folderRow.className = 'upload-folder-row';
    folderRow.innerHTML = `
      <label for="upload-folder" class="label">Folder (remote)</label>
      <input class="input" id="upload-folder" type="text" placeholder="inbox" autocomplete="off" />
      <div class="row-tight" style="margin-top:var(--sp-1)">
        <button type="button" class="btn btn-sm btn-ghost" data-folder-preset="inbox">inbox</button>
        <button type="button" class="btn btn-sm btn-ghost" data-folder-preset="incoming">incoming</button>
        <button type="button" class="btn btn-sm btn-ghost" data-folder-preset="upload">upload</button>
      </div>
      <div class="help">Where each upload user drops files. Server-side this is the path the SFTP user has write access to.</div>
    `;
    body.insertBefore(folderRow, body.firstChild);

    const newFolder = folderRow.querySelector('#upload-folder');
    if (legacyFolder) {
      // Initial sync: prefer legacy value if it has one (saved config / import).
      newFolder.value = legacyFolder.value || '';
      const fwd = () => {
        if (legacyFolder.value !== newFolder.value) {
          legacyFolder.value = newFolder.value;
          legacyFolder.dispatchEvent(new Event('change', { bubbles: true }));
        }
      };
      const back = () => {
        if (newFolder.value !== legacyFolder.value) newFolder.value = legacyFolder.value;
      };
      newFolder.addEventListener('input', fwd);
      legacyFolder.addEventListener('change', back);
    }
    folderRow.querySelectorAll('[data-folder-preset]').forEach((btn) => {
      btn.addEventListener('click', (ev) => {
        ev.preventDefault();
        newFolder.value = btn.dataset.folderPreset;
        newFolder.dispatchEvent(new Event('input', { bubbles: true }));
        newFolder.focus();
      });
    });
  }

  // ---- 3. Reparent largeCard body into upload card as Advanced section ----
  if (largeCard && body) {
    const largeBody = largeCard.querySelector('.body');
    const largeHeader = largeCard.querySelector('header');
    if (largeBody && largeHeader) {
      // Steal the Large-files toggle checkbox from the legacy header so
      // toggling Advanced still maps to #large_enabled (legacy input).
      const largeEnabled = document.getElementById('large_enabled');

      const advanced = document.createElement('details');
      advanced.className = 'upload-advanced';
      advanced.id = 'upload-advanced';
      // Open by default if large was enabled in saved config; otherwise closed.
      if (largeEnabled && largeEnabled.checked) advanced.open = true;
      advanced.innerHTML = `
        <summary class="upload-advanced-summary">
          <span class="upload-advanced-chevron" aria-hidden="true">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round"><path d="M9 6l6 6-6 6"/></svg>
          </span>
          <label class="upload-advanced-toggle">
            <input type="checkbox" id="upload-advanced-enabled" />
            <span class="upload-advanced-label">Advanced — large file mode</span>
          </label>
          <span class="upload-advanced-hint">Bigger payloads at a slower cadence (single file at a time, configurable size and interval).</span>
        </summary>
        <div class="upload-advanced-body" id="upload-advanced-body"></div>
      `;
      // Move the legacy body's children into the new advanced body.
      const advBody = advanced.querySelector('#upload-advanced-body');
      while (largeBody.firstChild) advBody.appendChild(largeBody.firstChild);

      // Append at the END of upload body.
      body.appendChild(advanced);

      // Wire the new toggle to the legacy #large_enabled.
      const newToggle = advanced.querySelector('#upload-advanced-enabled');
      if (largeEnabled && newToggle) {
        newToggle.checked = !!largeEnabled.checked;
        // Toggling the checkbox directly opens/closes the disclosure so users
        // get a single clear gesture: tick it ON to expand, OFF to collapse.
        // Stop propagation so clicking the checkbox doesn't ALSO trigger the
        // <summary> default toggle (which would re-flip the state).
        newToggle.addEventListener('click', (ev) => ev.stopPropagation());
        const fwd = () => {
          if (largeEnabled.checked !== newToggle.checked) {
            largeEnabled.checked = newToggle.checked;
            largeEnabled.dispatchEvent(new Event('change', { bubbles: true }));
          }
          advanced.open = newToggle.checked;
        };
        const back = () => {
          if (newToggle.checked !== largeEnabled.checked) {
            newToggle.checked = largeEnabled.checked;
            advanced.open = newToggle.checked;
          }
        };
        newToggle.addEventListener('change', fwd);
        largeEnabled.addEventListener('change', back);
      }

      // Hide the legacy outer largeCard wrapper — its content is now nested.
      largeCard.style.display = 'none';
      largeCard.dataset.restructured = '1';
    }
  }
}
