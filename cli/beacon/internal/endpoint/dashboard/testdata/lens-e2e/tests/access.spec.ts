import { expect, test } from '@playwright/test';

// Every other spec starts with the cookie the launch URL sets (playwright.config.ts). This one
// starts without it, as a browser does when someone opens the bare port, and then opens the URL
// `beacon endpoint dashboard` prints.
test.use({ storageState: { cookies: [], origins: [] } });

const token = process.env.BEACON_E2E_DASHBOARD_TOKEN!;

test('the bare port is refused and says to use the printed URL', async ({ page }) => {
  for (const path of ['/', '/session.html?id=s1', '/api/events', '/lenses/frame/activity']) {
    const response = await page.goto(path);
    expect(response?.status(), path).toBe(401);
    await expect(page.locator('body')).toContainText('printed');
  }
  const wrong = await page.goto('/?token=not-the-secret');
  expect(wrong?.status()).toBe(403);
});

test('the launch URL signs the browser in and the session page and its lenses work', async ({ page, context }) => {
  await page.goto(`/session.html?id=s1&lens=activity&token=${encodeURIComponent(token)}`);
  // The token leaves the address bar, and the rest of the URL stays.
  await expect(page).toHaveURL((url) => url.pathname === '/session.html' && !url.searchParams.has('token')
    && url.searchParams.get('id') === 's1' && url.searchParams.get('lens') === 'activity');
  const cookie = (await context.cookies()).find((c) => c.name.startsWith('beacon_dashboard_'));
  expect(cookie?.httpOnly).toBe(true);
  expect(cookie?.sameSite).toBe('Strict');

  // The lens frame (a same-site navigation into a sandboxed, opaque-origin frame) and the lens
  // data the page fetches for it both carry the cookie.
  const frame = page.frameLocator('iframe.lens-frame');
  await expect(frame.locator('#root table').first()).toBeVisible();
  await expect(page.locator('#lens-status')).toBeHidden();

  // Navigating on keeps working without the token.
  const response = await page.goto('/');
  expect(response?.status()).toBe(200);
});
