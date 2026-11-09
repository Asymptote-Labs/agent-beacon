import { expect, test } from '@playwright/test';

test('failures: every failure in order, its output, and what happened next', async ({ page }) => {
  await page.goto('/session.html?id=failing&lens=failures');
  const frame = page.frameLocator('iframe.lens-frame');
  await expect(frame.locator('#root')).toBeVisible();

  await expect(frame.locator('#root > p').first()).toHaveText('5 failures: 2 commands, 2 MCP calls, 1 error.');
  await expect(frame.locator('#root > p').nth(1)).toHaveText('1 later succeeded when the agent ran the same thing again.');

  const rows = frame.locator('tbody tr:not(.detail)');
  await expect(rows).toHaveCount(5);

  const test1 = rows.nth(0);
  await expect(test1.locator('td').nth(3)).toHaveText('go test ./...');
  await expect(test1.locator('td').nth(4)).toHaveText('exit 1');
  await expect(test1.locator('td').nth(5)).toHaveText('succeeded at #2');
  await expect(frame.locator('tbody tr.detail pre').first()).toContainText('want 3 attempts, got 2');

  const lint = rows.nth(1);
  await expect(lint.locator('td').nth(3)).toHaveText('npm run lint');
  await expect(lint.locator('td').nth(4)).toHaveText('exit 2');
  await expect(lint.locator('td').nth(5)).toHaveText('—');

  const mcp = rows.nth(2);
  await expect(mcp.locator('td').nth(2)).toHaveText('MCP call');
  await expect(mcp.locator('td').nth(3)).toHaveText('github / create_issue');
  await expect(mcp.locator('td').nth(4)).toHaveText('tool_execution_failed');
  await expect(mcp.locator('td').nth(5)).toHaveText('failed again at #5');

  const agentError = rows.nth(4);
  await expect(agentError.locator('td').nth(2)).toHaveText('error');
  await expect(frame.locator('tbody tr.detail').last()).toContainText('model request failed: overloaded');
});

test('failures: a trace where nothing failed says so', async ({ page }) => {
  await page.goto('/session.html?id=gated&lens=failures');
  const frame = page.frameLocator('iframe.lens-frame');
  await expect(frame.locator('#root')).toContainText('Nothing failed in this trace');
});
