import { expect, test, type Page } from '@playwright/test';

// Requests the page made anywhere but the dashboard. A lens has no business reaching any of them,
// and they are aborted so a failing test never touches the network.
async function trackOffsite(page: Page): Promise<string[]> {
  const offsite: string[] = [];
  const origin = new URL(test.info().project.use.baseURL!).origin;
  await page.route('**/*', (route) => {
    const url = route.request().url();
    if (url.startsWith(origin)) return route.continue();
    offsite.push(url);
    return route.abort();
  });
  return offsite;
}

// Every request the probe lens tries carries a via= marker. Chromium reports a request the CSP
// refused as a request that failed with "csp" before it left the browser; any other outcome for a
// marked request -- a response, or a failure for another reason -- means it was sent, whatever the
// API told the lens.
function trackProbeRequests(page: Page): string[] {
  const escaped: string[] = [];
  page.on('requestfinished', (request) => {
    if (request.url().includes('via=')) escaped.push(`sent ${request.method()} ${request.url()}`);
  });
  page.on('requestfailed', (request) => {
    const reason = request.failure()?.errorText ?? '';
    if (request.url().includes('via=') && reason !== 'csp') escaped.push(`${reason} ${request.method()} ${request.url()}`);
  });
  page.on('websocket', (ws) => escaped.push(`websocket ${ws.url()}`));
  return escaped;
}

const lensFrame = (page: Page) => page.frameLocator('iframe.lens-frame');

async function openLens(page: Page, id: string) {
  await page.goto(`/session.html?id=s1&lens=${id}`);
  await expect(page.locator('#session-detail-view')).toBeVisible();
}

async function expectFellBack(page: Page, reason: RegExp) {
  await expect(page.locator('#lens-notice')).toBeVisible({ timeout: 15_000 });
  await expect(page.locator('#lens-notice')).toContainText(reason);
  await expect(page.locator('#session-detail-view')).toHaveAttribute('data-view', 'full');
  await expect(page.locator('iframe.lens-frame')).toHaveCount(0);
  expect(new URL(page.url()).searchParams.get('lens')).toBeNull();
}

test('the built-in activity lens renders the session and its finding', async ({ page }) => {
  const offsite = await trackOffsite(page);
  await openLens(page, 'activity');
  const frame = lensFrame(page);
  await expect(frame.locator('#root')).toBeVisible();
  await expect(frame.locator('#root')).toContainText('rm -rf /');
  await expect(frame.locator('#root')).toContainText('Recursive delete targeting a sensitive root path');
  await expect(page.locator('#lens-status')).toBeHidden();
  await expect(page.locator('.lens-tab[aria-selected="true"]')).toContainText('Activity');
  await expect(page.locator('.session-events-panel')).toBeHidden();
  // The prompt carries markup that would retitle the dashboard if it were ever parsed as HTML.
  expect(await page.title()).not.toBe('pwned');
  expect(offsite).toEqual([]);
});

test('a lens cannot reach the network, the dashboard, storage or code evaluation', async ({ page }) => {
  const offsite = await trackOffsite(page);
  const escaped = trackProbeRequests(page);
  await openLens(page, 'probe');
  const results = lensFrame(page).locator('#results');
  await expect(results).not.toBeEmpty({ timeout: 15_000 });
  const r = JSON.parse((await results.textContent())!);

  expect(r.traceId).toBe('session:claude_code:s1');
  expect(r.events).toBe(2);
  expect(r.forged).toBe(false); // window.beacon could not be replaced
  expect(r.origin).toBe('null'); // opaque origin
  expect(r.seenPorts).toBe(0); // lens listeners never see the host's port

  for (const probe of [
    'fetchSameOrigin', 'fetchRemote', 'xhr', 'websocket', 'image', 'nestedFrame',
    'parentDocument', 'topLocation', 'localStorage', 'cookie', 'eval', 'newFunction', 'popup', 'worker',
    'rtcDirect', 'rtcComputed', 'rtcWebkit', 'rtcDataChannel', 'rtcDelete', 'rtcRedefine',
    // Reaching a fresh realm through a child frame, then every way to put a frame, a resource hint
    // or speculation rules into the document. The prelude's guard is best-effort hardening; these
    // pin the paths it covers.
    'rtcChildNoSrc', 'rtcChildAboutBlank', 'rtcChildSrcdoc', 'rtcChildDocumentWrite', 'rtcChildIndexed',
    'rtcChildFrames', 'rtcChildOwnScript',
    'frameCreateNS', 'frameInnerHTML', 'frameOuterHTML', 'frameInsertAdjacentHTML', 'frameSetHTMLUnsafe',
    'frameDocumentWrite', 'frameParsedAdopt', 'frameFragment', 'frameShadow', 'frameTemplateClone', 'frameStatic',
    'hintCreate', 'hintInnerHTML', 'hintStatic',
    'rulesTypeProperty', 'rulesSetAttribute', 'rulesMutateManifest', 'rulesInnerHTML', 'rulesStatic',
    // Markup the real parser reads differently from a plain document parse: <noscript> with scripting
    // on, foreign content contexts, and closed declarative shadow roots.
    'noscriptInnerHTML', 'noscriptInsertAdjacentHTML', 'noscriptSetHTMLUnsafe', 'noscriptOuterHTML',
    'foreignSvg', 'foreignMath', 'templateClosedShadow', 'parseHTMLUnsafeClosedShadow', 'execCommandInsertHTML',
  ]) {
    expect(r[probe], probe).toBe('blocked');
  }
  // The guard parses markup itself; benign markup must come out exactly as the browser builds it
  // (children, beside-insertion, context-dependent tbody, inert innerHTML scripts, <noscript> as text).
  expect(r.benignParsing).toBe('u+s+script+i+svg+table,before,inert,svg,tbody,tr,em,3');
  expect(r.dnsPrefetchMeta).toBe(true); // implicit DNS prefetching is off in the document
  const frameResponse = await page.request.get('/lenses/frame/probe');
  expect(frameResponse.headers()['x-dns-prefetch-control']).toBe('off');
  // Spoofed window messages to the host changed nothing.
  const height = await page.locator('iframe.lens-frame').evaluate((f: HTMLIFrameElement) => f.style.height);
  expect(height).not.toBe('99999px');
  // Let a queued beacon flush before concluding nothing was sent.
  await page.waitForTimeout(500);
  expect(escaped).toEqual([]);
  expect(offsite).toEqual([]);
});

test('a lens that navigates its own frame is closed without leaking', async ({ page }) => {
  const offsite = await trackOffsite(page);
  await openLens(page, 'navigator');
  await expectFellBack(page, /navigated away from itself/);
  expect(offsite).toEqual([]);
});

test('a lens that throws before rendering falls back to the full session', async ({ page }) => {
  await openLens(page, 'thrower');
  await expectFellBack(page, /boom before render/);
  await expect(page.locator('.session-events-panel')).toBeVisible();
});

test('a lens that never renders falls back after the timeout', async ({ page }) => {
  await page.clock.install();
  await openLens(page, 'staller');
  await expect(page.locator('#lens-status')).toBeVisible();
  await page.clock.runFor(10_000);
  await expectFellBack(page, /did not render within 10 seconds/);
});

test('the host sizes the frame to the lens content', async ({ page }) => {
  await openLens(page, 'tall');
  await expect
    .poll(async () => parseInt(await page.locator('iframe.lens-frame').evaluate((f: HTMLIFrameElement) => f.style.height), 10))
    .toBeGreaterThanOrEqual(2400);
});

test('tabs are added from the menu, deep-linked, and removed', async ({ page }) => {
  await page.goto('/session.html?id=s1');
  await page.evaluate(() => localStorage.clear());
  await page.reload();
  await expect(page.locator('#lens-tabs .lens-tab')).toHaveCount(1);
  await page.click('#lens-add-button');
  await page.locator('.lens-menu-item', { hasText: 'Activity' }).click();
  await expect(page).toHaveURL(/lens=activity/);
  await expect(lensFrame(page).locator('#root')).toBeVisible();

  await page.goBack();
  await expect(page.locator('#session-detail-view')).toHaveAttribute('data-view', 'full');
  await page.goForward();
  await expect(page.locator('#session-detail-view')).toHaveAttribute('data-view', 'lens');

  await page.reload();
  await expect(page.locator('#lens-tabs .lens-tab')).toHaveCount(2); // remembered
  await page.locator('.lens-tab', { hasText: 'Activity' }).locator('.lens-tab-remove').click();
  await expect(page.locator('#lens-tabs .lens-tab')).toHaveCount(1);
  await expect(page).not.toHaveURL(/lens=/);
});

test('going back to a lens whose tab was removed brings the tab back', async ({ page }) => {
  await page.goto('/session.html?id=s1');
  await page.evaluate(() => localStorage.clear());
  await page.goto('/session.html?id=s1&lens=activity');
  await expect(lensFrame(page).locator('#root')).toBeVisible();
  await page.locator('.lens-tab', { hasText: 'Activity' }).locator('.lens-tab-remove').click();
  await expect(page.locator('#lens-tabs .lens-tab')).toHaveCount(1);

  await page.goBack();
  await expect(page).toHaveURL(/lens=activity/);
  await expect(page.locator('.lens-tab[aria-selected="true"]')).toContainText('Activity');
  await expect(lensFrame(page).locator('#root')).toBeVisible();
});

test('clicking the active tab neither reloads the lens nor adds history', async ({ page }) => {
  await page.goto('/session.html?id=s1&lens=activity');
  await expect(lensFrame(page).locator('#root')).toBeVisible();
  const before = await page.evaluate(() => history.length);
  const frame = page.locator('iframe.lens-frame');
  await frame.evaluate((f) => { (f as any).dataset.marker = 'same'; });
  await page.locator('.lens-tab[aria-selected="true"]').click();
  await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
  expect(await page.evaluate(() => history.length)).toBe(before);
  await expect(page.locator('iframe.lens-frame')).toHaveAttribute('data-marker', 'same');
});

