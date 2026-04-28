// external.js — opens download / external links the right way for whichever
// shell the UI is running inside.
//
// In a regular browser, <a href download> + Content-Disposition: attachment
// works natively — no script needed. In the Wails desktop webview the
// embedded engine has no save dialog and trying to navigate to a wails://
// URL via the system browser fails (the system browser doesn't know what
// wails:// is). For desktop CSV saves we bypass HTTP entirely and call the
// Go-side SaveRunCsv binding which uses Wails' SaveFileDialog.
//
// Single capture-phase listener that intercepts CSV/download/external links.

import { pushToast } from './toast.js';

export function installExternalOpener() {
  document.addEventListener('click', async (ev) => {
    const a = ev.target.closest('a[href]');
    if (!a) return;
    const href = a.getAttribute('href') || '';
    if (!href || href.startsWith('#')) return;
    const isCsv      = href.includes('/api/report.csv');
    const isExternal = a.dataset.external === '1';
    const isDownload = a.hasAttribute('download');
    if (!(isCsv || isExternal || isDownload)) return;

    let url;
    try { url = new URL(href, window.location.href).href; }
    catch { url = href; }

    const SaveCsv = window.go && window.go.main && window.go.main.App && window.go.main.App.SaveRunCsv;
    const isWailsApp = !!(window.runtime && SaveCsv);

    if (isWailsApp && isCsv) {
      ev.preventDefault();
      const runID = (() => {
        try { return new URL(url).searchParams.get('run') || ''; }
        catch { return ''; }
      })();
      try {
        const result = await SaveCsv(runID);
        if (result) {
          pushToast(`Save failed: ${result}`, 'error');
        } else {
          pushToast('CSV saved', 'success');
        }
      } catch (e) {
        pushToast(`Save error: ${e.message || e}`, 'error');
      }
      return;
    }

    if (isWailsApp && (isExternal || isDownload) && window.runtime.BrowserOpenURL) {
      ev.preventDefault();
      window.runtime.BrowserOpenURL(url);
      return;
    }

    // Plain browser: native download attribute + Content-Disposition is
    // already correct. No DOM mutation needed.
  }, true);
}
