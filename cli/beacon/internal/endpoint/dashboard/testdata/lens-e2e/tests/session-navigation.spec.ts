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

for (const gesture of ['modifier', 'middle'] as const) {
  test(`${gesture} click opens the session in a real separate tab`, async ({ page, context }) => {
    await fixture(context);
    await list(page);
    const original = page.url();
    const link = page.locator('#events tr[data-session-id] a');
    await expect(link).toHaveCount(1);
    const opened = context.waitForEvent('page');
    if (gesture === 'middle') await link.click({ button: 'middle' });
    else await link.click({ modifiers: [process.platform === 'darwin' ? 'Meta' : 'Control'] });
    const tab = await opened;
    await detail(tab, alpha.id);
    await expect(page).toHaveURL(original);
    await expect(page.locator('#events tr[data-session-id]')).toHaveCount(1);
    await tab.close();
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
