// 02 — REGRESSION GUARD for: "Test connection result lingers when
// switching protocol tabs."
//
// Reported in v0.20.8: an operator runs a Test connection on SFTP,
// sees the result, then clicks the FTPS or FTP tab. The previous
// SFTP probe result is still visible until they click Test
// connection again. Stale state confuses; switching tabs implies
// "this is now a different test about to happen".
//
// The fix: connection.js setProtocol clears the result block when
// the protocol value actually changes.
//
// This spec drives a probe, captures the rendered text, switches
// protocol, and asserts the result block is empty afterwards.
import { test, expect } from '@playwright/test';
import { gotoConfigure, probeAndWait, switchProtocol } from '../fixtures/server';

test('switching protocol tab clears the stale probe result', async ({ page }) => {
  await gotoConfigure(page);

  // SFTP is the default. Run a probe; it will fail (port=1, no
  // server) but the result block will render an error/stages tile.
  const result = await probeAndWait(page);
  const beforeText = (await result.textContent()) ?? '';
  expect(beforeText.length).toBeGreaterThan(0);

  // Switch to FTPS — the SFTP result MUST be cleared so the
  // operator doesn't mistake it for a fresh FTPS test.
  await switchProtocol(page, 'ftps');

  // Allow setProtocol to dispatch its protocol-change event.
  await page.waitForTimeout(150);

  const afterText = (await result.textContent())?.trim() ?? '';
  expect(afterText, 'protocol switch must clear the probe-result block').toBe('');

  // And back to SFTP — again should be empty (no caching).
  await switchProtocol(page, 'sftp');
  await page.waitForTimeout(50);
  expect(((await result.textContent()) ?? '').trim()).toBe('');
});
