// external.js — open downloads / external links in the system browser when
// running inside the Wails desktop app webview.
//
// In a regular browser, <a href download> works fine — the browser shows a
// save dialog, current page stays put. In a Wails webview, clicking the same
// link navigates the embedded webview to the URL and the user gets stranded
// (CSV renders as text, no back button). Wails exposes
// window.runtime.BrowserOpenURL(url) which opens the URL in the system
// default browser, where the download dialog works correctly.
//
// We install a single capture-phase click handler that intercepts any anchor
// targeting /api/report.csv (or carrying download attribute / target=_blank /
// data-external) and routes it through Wails when available.

export function installExternalOpener() {
  document.addEventListener('click', (ev) => {
    const a = ev.target.closest('a[href]');
    if (!a) return;
    const href = a.getAttribute('href') || '';
    if (!href || href.startsWith('#')) return;
    const isDownload = a.hasAttribute('download');
    const isExternal = a.dataset.external === '1';
    const isCsv      = href.includes('/api/report.csv');
    const isBlank    = a.getAttribute('target') === '_blank';
    if (!(isDownload || isExternal || isCsv || isBlank)) return;

    // Resolve to absolute URL — Wails can't open relative ones.
    let url;
    try { url = new URL(href, window.location.href).href; }
    catch { url = href; }

    if (window.runtime && typeof window.runtime.BrowserOpenURL === 'function') {
      ev.preventDefault();
      window.runtime.BrowserOpenURL(url);
      return;
    }
    // Outside Wails (regular browser): default behaviour is fine.
    // For safety, ensure target=_blank on CSV links so the page never navigates
    // away mid-download — even though most browsers handle it via the
    // Content-Disposition header.
    if (isCsv && !isBlank) {
      a.setAttribute('target', '_blank');
      a.setAttribute('rel', 'noopener noreferrer');
    }
  }, true);
}
