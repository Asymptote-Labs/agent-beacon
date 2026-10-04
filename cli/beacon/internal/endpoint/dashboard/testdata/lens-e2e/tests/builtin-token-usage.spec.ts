import { expect, test } from '@playwright/test';

test('token usage: counted totals, by model, by turn and line items', async ({ page }) => {
  await page.goto('/session.html?id=rich&lens=token-usage');
  const frame = page.frameLocator('iframe.lens-frame');
  await expect(frame.locator('#root')).toBeVisible();

  // 5,200 + 800 on the first turn; 1,200 + 300 + 4,000 cache read + 800 cache write on the second.
  await expect(frame.locator('#root')).toContainText('12,300 tokens over 2 usage reports · $0.035 reported by the runtime.');
  // The events agree with Beacon's count, so there is nothing to reconcile.
  await expect(frame.locator('#root')).not.toContainText('Beacon counts');

  const tables = frame.locator('table');
  const byModel = tables.nth(1);
  await expect(byModel.locator('tbody tr')).toHaveCount(2);
  await expect(byModel).toContainText('claude-sonnet-5-5');
  await expect(byModel).toContainText('claude-haiku-4-5');

  const byTurn = tables.nth(2);
  await expect(byTurn.locator('tbody tr')).toHaveCount(2);
  await expect(byTurn.locator('tbody tr').nth(0)).toContainText('Make the retry backoff deterministic');
  await expect(byTurn.locator('tbody tr').nth(1)).toContainText('Now document the change');
  await expect(byTurn.locator('tbody tr').nth(1)).toContainText('4,000'); // cache read column shown

  const items = tables.nth(3);
  await expect(items.locator('tbody tr')).toHaveCount(2);
  await expect(items.locator('tfoot')).toContainText('Reported by events');
  await expect(frame.locator('#root')).toContainText('Beacon never estimates cost.');
});

test('token usage: a runtime that should report usage but did not is flagged', async ({ page }) => {
  await page.goto('/session.html?id=s1&lens=token-usage');
  const frame = page.frameLocator('iframe.lens-frame');
  await expect(frame.locator('#root')).toContainText('is a runtime Beacon reads usage from, but this trace reported none');
  await expect(frame.locator('table')).toHaveCount(0);
});

// Serve the real lens data with one edit, to reach states the fixture log cannot produce directly.
async function editLensData(page: import('@playwright/test').Page, edit: (data: any) => void) {
  await page.route('**/api/lens-data?*', async (route) => {
    const response = await route.fetch();
    const data = await response.json();
    edit(data);
    await route.fulfill({ response, json: data });
  });
}

test('token usage: without Beacon\'s count it bills what the events reported, and says so', async ({ page }) => {
  // As for a trace served from history after its lines rotated out of the live log.
  await editLensData(page, (data) => {
    delete data.token_usage;
    delete data.token_coverage;
  });
  await page.goto('/session.html?id=rich&lens=token-usage');
  const frame = page.frameLocator('iframe.lens-frame');
  await expect(frame.locator('h2').first()).toHaveText('Reported usage');
  await expect(frame.locator('#root')).toContainText('12,300 tokens over 2 usage reports');
  await expect(frame.locator('#root')).toContainText('Beacon could not count this trace\'s usage from the live log');
  await expect(frame.locator('table').nth(1).locator('tbody tr')).toHaveCount(2); // by model, from the events
  await expect(frame.locator('table').nth(3).locator('tbody tr')).toHaveCount(2); // line items
});

test('token usage: a gap between reported and counted usage is explained without overclaiming', async ({ page }) => {
  await editLensData(page, (data) => {
    data.token_usage.totals.input_tokens -= 1000;
  });
  await page.goto('/session.html?id=rich&lens=token-usage');
  const frame = page.frameLocator('iframe.lens-frame');
  await expect(frame.locator('#root')).toContainText('The events below report 12,300 tokens in total; Beacon counts 11,300.');
  await expect(frame.locator('#root')).toContainText('can also differ when this trace was truncated');
});
