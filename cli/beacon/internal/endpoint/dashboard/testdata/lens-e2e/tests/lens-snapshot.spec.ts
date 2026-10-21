import { expect, test, type Page } from '@playwright/test';

const lens = (page: Page) => page.frameLocator('iframe.lens-frame');
const count = (page: Page) => lens(page).locator('#root table').first().locator('tr').nth(1).locator('td').nth(1);
const tab = (page: Page) => page.locator('.lens-tab[data-lens="activity"]');
const bundle = (session: string, size: number) => ({
  trace: { id: `session:${session}`, events: Array.from({ length: size }, (_, i) => ({
    id: `${session}-${i}`, type: 'command', command: { command: `echo ${session}-${i}` },
  })) },
});

async function fixture(page: Page, size = () => 1) {
  const origin = new URL(test.info().project.use.baseURL!).origin;
  await page.route('**/*', (route) => new URL(route.request().url()).origin === origin ? route.continue() : route.abort());
  await page.route('**/api/status', (route) => route.fulfill({ json: {} }));
  await page.route('**/api/session?*', (route) => {
    const id = new URL(route.request().url()).searchParams.get('id')!;
    return route.fulfill({ json: { session: { id }, events: Array.from({ length: size() }, (_, i) => ({
      event: { id: `${id}-${i}`, action: 'command.executed' }, session: { id }, command: { command: `echo ${id}-${i}` },
    })) } });
  });
}

async function settled(page: Page) {
  await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
}

// Exercise a repeated host-protocol request as well as the lens prelude's own
// one-shot getTrace. The actual host callback must reuse this mount's snapshot.
async function requestAgain(page: Page) {
  await page.evaluate('lensHost.mount.port.onmessage({ data: { type: "getTrace" } })');
  await settled(page);
}

test('a reopened lens sees the normal detail refresh without reloading the active frame', async ({ page }) => {
  await page.clock.install();
  let size = 1;
  let requests = 0;
  await fixture(page, () => size);
  await page.route('**/api/lens-data?*', (route) => {
    requests++;
    return route.fulfill({ json: bundle('s1', size) });
  });
  await page.goto('/session.html?id=s1&lens=activity');
  await expect(count(page)).toHaveText('1');
  await expect(page.locator('#lens-status')).toBeHidden();
  await page.locator('iframe.lens-frame').evaluate((frame) => { frame.setAttribute('data-original', 'yes'); });
  const historyLength = await page.evaluate(() => history.length);
  size = 2;
  const refreshed = page.waitForResponse((response) => new URL(response.url()).pathname === '/api/session');
  await page.clock.runFor(15_000);
  await (await refreshed).finished();
  await expect(page.locator('#result-meta')).toContainText('2 of 2 events');
  await expect(count(page)).toHaveText('1');
  await tab(page).click();
  await expect(page.locator('iframe.lens-frame')).toHaveAttribute('data-original', 'yes');
  expect(await page.evaluate(() => history.length)).toBe(historyLength);
  await requestAgain(page);
  expect(requests).toBe(1);

  await page.locator('.lens-tab[data-lens=""]').click();
  await expect(page.locator('iframe.lens-frame')).toHaveCount(0);
  await tab(page).click();
  await expect(count(page)).toHaveText('2');
  expect(requests).toBe(2);
  await requestAgain(page);
  expect(requests).toBe(2);
  await expect(page.locator('iframe.lens-frame')).toHaveAttribute('sandbox', 'allow-scripts');
  expect(await lens(page).locator('body').evaluate(() => window.origin)).toBe('null');
});

test('a failed snapshot can be retried by reopening the lens', async ({ page }) => {
  await fixture(page);
  let requests = 0;
  await page.route('**/api/lens-data?*', (route) => ++requests === 1
    ? route.fulfill({ status: 503, json: { error: 'snapshot unavailable' } })
    : route.fulfill({ json: bundle('s1', 2) }));
  await page.goto('/session.html?id=s1&lens=activity');
  await expect(lens(page).locator('#status')).toContainText('snapshot unavailable');
  await page.locator('.lens-tab[data-lens=""]').click();
  await expect(page.locator('iframe.lens-frame')).toHaveCount(0);
  await tab(page).click();
  await expect(count(page)).toHaveText('2');
  expect(requests).toBe(2);
});

for (const failure of [false, true]) {
  test(`an unmounted session's late ${failure ? 'failure' : 'success'} cannot affect the new mount`, async ({ page }) => {
    await fixture(page);
    const errors: string[] = [];
    page.on('pageerror', (error) => errors.push(error.message));
    await page.addInitScript(() => {
      if (window !== window.top) return;
      const closed = new WeakSet<MessagePort>();
      const close = MessagePort.prototype.close;
      const post = MessagePort.prototype.postMessage;
      Object.assign(window, { closedPortPosts: 0 });
      MessagePort.prototype.close = function () { closed.add(this); return close.call(this); };
      MessagePort.prototype.postMessage = function (...args: Parameters<MessagePort['postMessage']>) {
        if (closed.has(this)) (window as unknown as { closedPortPosts: number }).closedPortPosts++;
        return Reflect.apply(post, this, args);
      };
    });
    let release!: () => void;
    let started!: () => void;
    const held = new Promise<void>((resolve) => { release = resolve; });
    const requested = new Promise<void>((resolve) => { started = resolve; });
    const requests: string[] = [];
    await page.route('**/api/lens-data?*', async (route) => {
      const session = new URL(route.request().url()).searchParams.get('session')!;
      requests.push(session);
      if (requests.length === 1) {
        started();
        await held;
        if (failure) return route.fulfill({ status: 503, json: { error: 'old snapshot failed' } });
      }
      await route.fulfill({ json: bundle(session, session === 's1' ? 1 : 2) });
    });
    await page.goto('/session.html?id=s1&lens=activity');
    await requested;
    // Reinitialize the real host for a changed session in the same document so
    // the old fetch continuation remains alive after its frame/port is closed.
    await page.evaluate('history.replaceState(null, "", "/session.html?id=s2&lens=activity"); initLensTabs()');
    await expect(count(page)).toHaveText('2');
    expect(requests).toEqual(['s1', 's2']);
    const oldResponse = page.waitForResponse((response) => new URL(response.url()).searchParams.get('session') === 's1');
    release();
    await (await oldResponse).finished();
    await settled(page);
    await requestAgain(page);
    await expect(count(page)).toHaveText('2');
    expect(requests).toEqual(['s1', 's2']);
    expect(await page.evaluate('window.closedPortPosts')).toBe(0);
    expect(errors).toEqual([]);
  });
}

test('a removed lens stays removed when its snapshot finishes late', async ({ page }) => {
  await fixture(page);
  let release!: () => void;
  let started!: () => void;
  const held = new Promise<void>((resolve) => { release = resolve; });
  const requested = new Promise<void>((resolve) => { started = resolve; });
  await page.route('**/api/lens-data?*', async (route) => {
    started();
    await held;
    await route.fulfill({ json: bundle('s1', 1) });
  });
  await page.goto('/session.html?id=s1&lens=activity');
  await requested;
  await page.getByRole('button', { name: 'Remove the Activity lens', exact: true }).click();
  const response = page.waitForResponse('**/api/lens-data?*');
  release();
  await (await response).finished();
  await settled(page);
  await expect(tab(page)).toHaveCount(0);
  await expect(page.locator('iframe.lens-frame')).toHaveCount(0);
  await expect(page.locator('#session-detail-view')).toHaveAttribute('data-view', 'full');
  expect(new URL(page.url()).searchParams.get('lens')).toBeNull();
});
