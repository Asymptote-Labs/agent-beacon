import { expect, test, type BrowserContext, type Page } from '@playwright/test';

const alpha = { id: 'session-alpha', harness: 'codex_cli', original_prompt: 'Inspect the cat', event_count: 1 };
async function fixture(context: BrowserContext, session = alpha) {
  // These API records know nothing about DOM links, focus, or click handlers.
  // Context routing also supplies the first request in genuinely opened tabs.
  await context.route('**/api/**', async (route) => {
    const url = new URL(route.request().url());
    const json = url.pathname === '/api/sessions' ? { sessions: [session] }
      : url.pathname === '/api/session' ? { session, events: [] }
      : url.pathname === '/api/summary' ? { total_events: 1, active_sessions: 1 }
      : url.pathname === '/api/lenses' ? { lenses: [] } : {};
    await route.fulfill({ json });
  });
}
async function list(page: Page) {
  await page.goto('/?range=all&session_state=');
  await expect(page.locator('#events tr[data-session-id]')).toHaveCount(1);
}
async function detail(page: Page, id: string) {
  await expect(page).toHaveURL((url) => url.pathname === '/session.html' && url.searchParams.get('id') === id);
  expect(new URL(page.url()).searchParams.size).toBe(1);
  await expect(page.locator('#session-full-id')).toHaveText(id);
}

test('a session is reachable through sequential Tab and native Enter', async ({ page, context }) => {
  await fixture(context);
  await list(page);
  await page.locator('[name="q"]').focus();
  let reached = false;
  for (let i = 0; i < 20; i++) {
    await page.keyboard.press('Tab');
    reached = await page.evaluate(() => !!document.activeElement?.closest('#events tr[data-session-id] a'));
    if (reached) break;
  }
  expect(reached).toBe(true);
  await page.keyboard.press('Enter');
  await detail(page, alpha.id);
});

// For both gestures the browser's default is to open the link in another tab; the dashboard must
// leave that default alone and not also navigate this page. The probe records both, then cancels
// them: actually opening a background tab makes Playwright stall on a loaded runner.
for (const gesture of ['modifier', 'middle'] as const) {
  test(`${gesture} click leaves opening the session in another tab to the browser`, async ({ page, context }) => {
    await fixture(context);
    await list(page);
    const link = page.locator('#events tr[data-session-id] a');
    await expect(link).toHaveAttribute('href', `/session.html?id=${alpha.id}`);
    await page.evaluate(() => {
      const probe: { prevented?: boolean; navigated?: string } = {};
      (window as unknown as { probe: typeof probe }).probe = probe;
      // navigate fires synchronously when a script sets location.href, so nothing slips past.
      (window as unknown as { navigation: EventTarget }).navigation.addEventListener('navigate', (event) => {
        probe.navigated = (event as unknown as { destination: { url: string } }).destination.url;
        event.preventDefault();
      });
      for (const type of ['click', 'auxclick']) {
        window.addEventListener(type, (event) => {
          if (!(event.target as Element).closest('#events a')) return;
          probe.prevented = event.defaultPrevented;
          event.preventDefault();
        });
      }
    });
    if (gesture === 'middle') await link.click({ button: 'middle' });
    else await link.click({ modifiers: [process.platform === 'darwin' ? 'Meta' : 'Control'] });
    expect(await page.evaluate(() => (window as unknown as { probe: object }).probe)).toEqual({ prevented: false });
  });
}

test('ordinary row-background click still opens the session', async ({ page, context }) => {
  await fixture(context);
  await list(page);
  await page.locator('#events tr td').nth(3).click();
  await detail(page, alpha.id);
});

test('row navigation preserves reserved characters in the session ID', async ({ page, context }) => {
  const session = { ...alpha, id: 'session/a?b#c&d="<quoted>' };
  await fixture(context, session);
  await list(page);
  await page.locator('#events tr td').nth(3).click();
  await detail(page, session.id);
});
