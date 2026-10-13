import { expect, test } from '@playwright/test';

for (const staleFailure of [false, true]) {
  test(`activity keeps the latest filter after an older request ${staleFailure ? 'fails' : 'succeeds'}`, async ({ page }) => {
    let releaseOlder!: () => void;
    let observedOlder!: () => void;
    const olderStarted = new Promise<void>((resolve) => { observedOlder = resolve; });
    const olderReleased = new Promise<void>((resolve) => { releaseOlder = resolve; });
    await page.route('**/api/status', (route) => route.fulfill({ json: {} }));
    await page.route('**/api/summary?*', (route) => route.fulfill({ json: {} }));
    await page.route('**/api/sessions?*', async (route) => {
      const query = new URL(route.request().url()).searchParams.get('q');
      if (query === 'older') {
        observedOlder();
        await olderReleased;
        if (staleFailure) {
          await route.fulfill({ status: 503, json: { error: 'older request failed' } });
          return;
        }
      }
      await route.fulfill({ json: { sessions: [{ id: `session-${query || 'initial'}`, event_count: 1 }] } });
    });
    await page.goto('/');
    await expect(page.locator('#events tr')).toHaveAttribute('data-session-id', 'session-initial');
    const search = page.locator('[name="q"]');
    await search.fill('older');
    await page.locator('#filters').evaluate((form) => form.dispatchEvent(new Event('submit', { cancelable: true })));
    await olderStarted;
    await search.fill('newer');
    await page.locator('#filters').evaluate((form) => form.dispatchEvent(new Event('submit', { cancelable: true })));
    await expect(page.locator('#events tr')).toHaveAttribute('data-session-id', 'session-newer');
    // Wait for the old response and let fetch continuations render before
    // checking that neither its success nor its error replaced the new view.
    const olderSettled = page.waitForResponse((response) => {
      const url = new URL(response.url());
      return url.pathname === '/api/sessions' && url.searchParams.get('q') === 'older';
    });
    releaseOlder();
    const response = await olderSettled;
    await response.finished();
    await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
    await expect(page.locator('#events tr')).toHaveAttribute('data-session-id', 'session-newer');
    await expect(search).toHaveValue('newer');
    await expect(page).toHaveURL(/q=newer/);
    await expect(page.locator('body')).not.toContainText('older request failed');
  });
}

test('polling lets an in-flight filter load finish', async ({ page }) => {
  await page.clock.install();
  let release!: () => void;
  let started!: () => void;
  const held = new Promise<void>((resolve) => { release = resolve; });
  const requested = new Promise<void>((resolve) => { started = resolve; });
  let pendingRequests = 0;
  await page.route('**/api/status', (route) => route.fulfill({ json: {} }));
  await page.route('**/api/summary?*', (route) => route.fulfill({ json: {} }));
  await page.route('**/api/sessions?*', async (route) => {
    const query = new URL(route.request().url()).searchParams.get('q');
    if (query === 'slow') {
      pendingRequests++;
      started();
      await held;
    }
    await route.fulfill({ json: { sessions: [{ id: `session-${query || 'initial'}`, event_count: 1 }] } });
  });
  await page.goto('/');
  await expect(page.locator('#events tr')).toHaveAttribute('data-session-id', 'session-initial');
  await page.locator('[name="q"]').fill('slow');
  await page.locator('#filters').evaluate((form) => form.dispatchEvent(new Event('submit', { cancelable: true })));
  await requested;
  await page.clock.runFor(30_000);
  expect(pendingRequests).toBe(1);
  release();
  await expect(page.locator('#events tr')).toHaveAttribute('data-session-id', 'session-slow');
  // Once the request finishes, the next timer tick must resume refreshing.
  await page.clock.runFor(10_000);
  await expect.poll(() => pendingRequests).toBe(2);
});
