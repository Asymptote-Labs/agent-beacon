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
