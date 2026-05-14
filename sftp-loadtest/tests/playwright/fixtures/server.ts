// fixtures/server.ts — small utilities every spec re-uses.
//
//   gotoConfigure(page)  navigates to Configure view from a fresh load.
//   probeAndWait(page)   fills minimal target fields and runs Test
//                        connection; resolves once the result block
//                        is rendered.
//   apiPost / apiGet     thin wrappers that include the CSRF header.

import { Page, APIRequestContext } from '@playwright/test';

export const CSRF = 'sftp-loadtest';

/** Force the Configure view via the sidebar so all specs start in the
 *  same place even when localStorage has a stale lastView. */
export async function gotoConfigure(page: Page): Promise<void> {
  await page.goto('/');
  await page.waitForSelector('.shell-sidebar-row[data-view="configure"]', { state: 'visible' });
  await page.click('.shell-sidebar-row[data-view="configure"]');
  // The Configure view's TARGET section anchor. #conn-host is the
  // visible host input inside the [data-component="connection"]
  // card (the legacy #host input still exists in the DOM but is
  // hidden — configure-redesign relocates inputs without removing
  // the originals).
  await page.waitForSelector('input#conn-host', { state: 'visible' });
}

/** Fill the bare minimum target fields and click Test connection.
 *  Used by probe-related specs that just need ANY probe result to
 *  inspect — the probe will fail because the host is bogus, but
 *  the result block (with stages + Details disclosure) renders all
 *  the same. Returns the probe-result element locator. */
export async function probeAndWait(page: Page, host = '127.0.0.1', port = 1, user = 'u', pass = 'p') {
  // The connection card scopes its data-roles to its root, so we
  // target inputs by data-role inside [data-component="connection"].
  const conn = page.locator('[data-component="connection"]');
  await conn.locator('[data-role="host"]').fill(host);
  await conn.locator('[data-role="port"]').fill(String(port));
  await conn.locator('[data-role="username"]').fill(user);
  await conn.locator('[data-role="password"]').fill(pass);
  await conn.locator('[data-role="submit"]').click();
  const result = conn.locator('[data-role="result"]');
  await result.waitFor({ state: 'visible' });
  // Settled = no longer showing the "Testing connection…" spinner.
  await page.waitForFunction(() => {
    const el = document.querySelector('[data-component="connection"] [data-role="result"]');
    if (!el) return false;
    return !el.textContent?.includes('Testing connection');
  }, { timeout: 15_000 });
  return result;
}

/** Click one of the protocol picker buttons inside the connection card.
 *  Pass the lowercase value: 'sftp' | 'ftp' | 'ftps'. */
export async function switchProtocol(page: Page, proto: 'sftp' | 'ftp' | 'ftps') {
  await page.locator(`[data-component="connection"] [data-role="protocol-picker"] button[data-value="${proto}"]`).click();
}

export async function apiPost(req: APIRequestContext, path: string, body: any = {}) {
  return await req.post(path, {
    headers: { 'Content-Type': 'application/json', 'X-Requested-With': CSRF },
    data: body,
  });
}

export async function apiGet(req: APIRequestContext, path: string) {
  return await req.get(path, { headers: { 'X-Requested-With': CSRF } });
}
