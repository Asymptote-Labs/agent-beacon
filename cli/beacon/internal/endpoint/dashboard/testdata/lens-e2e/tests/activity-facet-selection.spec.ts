import { expect, test, type Page } from '@playwright/test';

// Independent records: the API fixture filters records using request parameters,
// and only then builds summary buckets. It does not model select/URL behavior.
const sessions = [
  { id: 'session-alpha', harness: 'codex_cli', model: 'model-alpha', repository: '/workspace/alpha', original_prompt: 'cat', event_count: 1 },
  { id: 'session-beta', harness: 'claude_code', model: 'model-beta', repository: '/workspace/beta', original_prompt: 'dog', event_count: 1 },
];
async function fixture(page: Page) {
  const requests: URL[] = [];
  await page.route('**/api/**', async (route) => {
    const url = new URL(route.request().url());
    requests.push(url);
    const rows = sessions.filter((row) => ['harness', 'model', 'repository'].every((key) =>
      !url.searchParams.get(key) || row[key as keyof typeof row] === url.searchParams.get(key)) &&
      (!url.searchParams.get('q') || row.original_prompt.includes(url.searchParams.get('q')!)));
    const counts = (key: 'harness' | 'model' | 'repository') => rows.map((row) => ({ name: row[key], count: 1 }));
    const json = url.pathname === '/api/summary' ? {
      total_events: rows.length, active_sessions: rows.length,
      top_harnesses: counts('harness'), top_models: counts('model'), top_repositories: counts('repository'),
    } : url.pathname === '/api/sessions' ? { sessions: rows } : url.pathname === '/api/events' ? { events: [] } : {};
    await route.fulfill({ json });
  });
  return requests;
}
async function settled(page: Page) {
  await expect(page.locator('#events tr')).not.toHaveCount(0);
  await expect(page.locator('#events')).not.toContainText('Loading');
  await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
}
async function submit(page: Page) {
  const response = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/sessions');
  await page.locator('[name="q"]').press('Enter');
  await (await response).finished();
  await settled(page);
}
for (const [facet, value] of [['harness', 'codex_cli'], ['model', 'model-alpha'], ['repository', '/workspace/alpha']]) {
  test(`bookmarked ${facet} reaches the first requests`, async ({ page }) => {
    const requests = await fixture(page);
    await page.goto(`/?range=all&session_state=&${facet}=${encodeURIComponent(value)}`);
    await settled(page);
    for (const path of ['/api/summary', '/api/sessions']) {
      expect(requests.find((url) => url.pathname === path)?.searchParams.get(facet)).toBe(value);
    }
    await expect(page.locator(`[name="${facet}"]`)).toHaveValue(value);
    await expect(page.locator('#events tr[data-session-id]')).toHaveCount(1);
    await expect(page.locator('#events tr')).toHaveAttribute('data-session-id', 'session-alpha');
  });
}
for (const [facet, value] of [['harness', 'codex_cli'], ['model', 'model-alpha'], ['repository', '/workspace/alpha']]) {
  test(`empty results preserve ${facet} across the real poll and explicit clear`, async ({ page }) => {
    await page.clock.install();
    const requests = await fixture(page);
    await page.goto('/?range=all&session_state=');
    await settled(page);
    await page.locator(`[name="${facet}"]`).selectOption(value);
    await page.locator('[name="q"]').fill('dog');
    await submit(page);
    const afterEmpty = await page.locator(`[name="${facet}"]`).inputValue();
    const count = requests.filter((url) => url.pathname === '/api/sessions').length;
    // Fire the production ten-second setInterval by advancing the page clock.
    await page.clock.runFor(10_000);
    await expect.poll(() => requests.filter((url) => url.pathname === '/api/sessions').length).toBeGreaterThan(count);
    await settled(page);
    expect.soft(afterEmpty).toBe(value);
    expect.soft(requests.filter((url) => url.pathname === '/api/sessions').at(-1)?.searchParams.get(facet)).toBe(value);
    expect.soft(await page.locator(`[name="${facet}"]`).inputValue()).toBe(value);
    await expect.soft(page.locator('#events')).toContainText('No sessions match');
    await page.locator(`[name="${facet}"]`).selectOption('');
    await submit(page);
    await expect(page.locator('#events tr')).toHaveAttribute('data-session-id', 'session-beta');
    expect(requests.filter((url) => url.pathname === '/api/sessions').at(-1)?.searchParams.has(facet)).toBe(false);
  });
}
test('overview chart navigation retains its harness on the first sessions request', async ({ page }) => {
  const requests = await fixture(page);
  await page.goto('/overview.html');
  await page.locator('[data-activity-harness="codex_cli"]').click();
  await settled(page);
  expect(requests.find((url) => url.pathname === '/api/sessions')?.searchParams.get('harness')).toBe('codex_cli');
  await expect(page.locator('[name="harness"]')).toHaveValue('codex_cli');
});
test('sessions select submission retains populated labels without duplicate options', async ({ page }) => {
  const requests = await fixture(page);
  await page.goto('/?range=all&session_state=');
  await settled(page);
  await page.locator('[name="harness"]').selectOption('codex_cli');
  await submit(page);
  await expect(page.locator('#events tr')).toHaveAttribute('data-session-id', 'session-alpha');
  expect(requests.filter((url) => url.pathname === '/api/sessions').at(-1)?.searchParams.get('harness')).toBe('codex_cli');
  await expect(page.locator('#harness-filter option[value="codex_cli"]')).toHaveCount(1);
  await expect(page.locator('#harness-filter option[value="codex_cli"]')).toHaveText('Codex CLI (1)');
  await expect(page.locator('#model-filter option[value="model-alpha"]')).toHaveText('model-alpha (1)');
});
