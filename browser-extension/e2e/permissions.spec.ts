// The host-permission banner (src/shared/permissions.ts) in the real, loaded
// extension. On Chrome the host permissions are granted at install, so the
// banner must stay hidden -- that is the no-regression case for the Chrome
// build. A withheld host (Chrome's "Site access" restriction, or Firefox's
// opt-in host permissions) must make the banner name what is missing.
import { test, expect } from './fixtures.js';

test('popup and options show no site-access warning on a default Chrome install', async ({
  context,
  extensionId,
}) => {
  for (const pageName of ['popup.html', 'options.html']) {
    const page = await context.newPage();
    await page.goto(`chrome-extension://${extensionId}/${pageName}`);
    // The markup starts hidden, so wait for a completed check (data-state)
    // before asserting hidden; otherwise this would pass without running.
    const banner = page.locator('[data-testid="perm-missing"]');
    await expect(banner).toHaveAttribute('data-state', 'granted');
    await expect(banner).toBeHidden();
    await page.close();
  }
});

test('popup and options name withheld hosts and clear once they are granted', async ({
  context,
  extensionId,
}) => {
  // Chrome refuses to revoke a required host permission from script ("You
  // cannot remove required permissions"), and a revocation through Site access
  // needs the browser UI. So simulate the withheld state the way the page sees
  // it: a permissions API that reports two hosts missing until they are
  // requested. Everything else -- markup, CSS, the click wiring -- is real.
  const withheld = ['*://chatgpt.com/*', 'http://127.0.0.1/*'];
  await context.addInitScript((missing: string[]) => {
    if (location.protocol !== 'chrome-extension:') return;
    const perms = chrome.permissions;
    const denied = new Set(missing);
    const requested: string[][] = [];
    (globalThis as any).__beaconRequested = requested;
    perms.contains = (async (p: chrome.permissions.Permissions) =>
      !(p.origins ?? []).some((o) => denied.has(o))) as typeof perms.contains;
    perms.request = (async (p: chrome.permissions.Permissions) => {
      requested.push([...(p.origins ?? [])]);
      (p.origins ?? []).forEach((o) => denied.delete(o));
      return true;
    }) as typeof perms.request;
  }, withheld);

  for (const pageName of ['popup.html', 'options.html']) {
    const page = await context.newPage();
    await page.goto(`chrome-extension://${extensionId}/${pageName}`);
    const banner = page.locator('[data-testid="perm-missing"]');
    await expect(banner).toBeVisible();
    await expect(banner).toHaveAttribute('data-state', 'missing');
    await expect(page.locator('[data-testid="perm-list"]')).toHaveText('chatgpt.com, 127.0.0.1');

    await page.locator('[data-testid="perm-grant"]').click();
    await expect(banner).toHaveAttribute('data-state', 'granted');
    await expect(banner).toBeHidden();
    expect(await page.evaluate(() => (globalThis as any).__beaconRequested)).toEqual([withheld]);
    await page.close();
  }
});
