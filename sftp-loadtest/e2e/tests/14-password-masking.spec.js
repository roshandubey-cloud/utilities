// Password masking — the user-CSV textareas mask passwords on blur to
// protect against shoulder-surfing while keeping the raw value
// available to the runner. The mask must be reversible (focus reveals
// the raw text) and must not corrupt round-trip via dataset.raw.

import { test, expect } from '@playwright/test';

test('user CSV password column is masked when textarea loses focus', async ({ page }) => {
  await page.goto('/');
  const ta = page.locator('#normal_users');
  await ta.click();
  await ta.fill('alice,supersecret,invoice*\nbob,p@ssw0rd,order*');
  await ta.blur();
  // Visible value (after mask) — the password column is replaced with bullets.
  const visible = await ta.inputValue();
  expect(visible).not.toContain('supersecret');
  expect(visible).not.toContain('p@ssw0rd');
  expect(visible).toContain('alice');
  expect(visible).toContain('bob');
  // The raw is preserved on dataset.raw for the runner.
  const raw = await ta.evaluate((el) => el.dataset.raw);
  expect(raw).toContain('supersecret');
  expect(raw).toContain('p@ssw0rd');
});

test('refocusing the textarea reveals the raw text again', async ({ page }) => {
  await page.goto('/');
  const ta = page.locator('#normal_users');
  await ta.click();
  await ta.fill('alice,supersecret,invoice*');
  await ta.blur();
  // Re-focus.
  await ta.focus();
  const focused = await ta.inputValue();
  expect(focused, 'on focus the textarea reveals the raw — supersecret must be visible').toContain('supersecret');
});

test('export with passwords-not-included strips the password column', async ({ page }) => {
  await page.goto('/');
  const ta = page.locator('#normal_users');
  await ta.click();
  await ta.fill('alice,supersecret,invoice*');
  await ta.blur();
  // Default export does NOT include passwords.
  const exportBtn = page.getByRole('button', { name: /^export config$/i });
  const [download] = await Promise.all([
    page.waitForEvent('download', { timeout: 5000 }),
    exportBtn.click(),
  ]);
  const fs = await import('node:fs/promises');
  const wrapper = JSON.parse(await fs.readFile(await download.path(), 'utf8'));
  expect(wrapper.passwords_included).toBe(false);
  // The user CSV in the export must NOT contain the cleartext password.
  expect(wrapper.config.normal_users_csv || '').not.toContain('supersecret');
  // But the username and pattern must still round-trip.
  expect(wrapper.config.normal_users_csv || '').toContain('alice');
  expect(wrapper.config.normal_users_csv || '').toContain('invoice');
});
