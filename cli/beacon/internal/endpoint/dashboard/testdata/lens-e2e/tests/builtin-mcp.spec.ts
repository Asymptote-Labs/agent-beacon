import { expect, test } from '@playwright/test';

test('mcp: calls by server and tool, with arguments, results and failures', async ({ page }) => {
  await page.goto('/session.html?id=mcpish&lens=mcp');
  const frame = page.frameLocator('iframe.lens-frame');
  await expect(frame.locator('#root')).toBeVisible();

  // The pre- and post-tool events of create_issue are one call; the bare mcp__linear__ name counts;
  // the file read does not.
  await expect(frame.locator('#summary')).toHaveText('5 calls to 5 tools on 3 servers · 2 failed.');
  const servers = frame.locator('#list button.row');
  await expect(servers).toHaveCount(3);
  await expect(servers.nth(0)).toContainText('github');
  await expect(servers.nth(0)).toContainText('1 failed');
  // linear has two calls, one of them a failure recorded only as the event's error.
  await expect(servers.nth(1)).toContainText('linear');
  await expect(servers.nth(1)).toContainText('1 failed');
  await expect(servers.nth(2)).toContainText('filesystem');

  const detail = frame.locator('#detail');
  await expect(detail.locator('h2').first()).toHaveText('github');
  const tools = detail.locator('tbody tr');
  await expect(tools).toHaveCount(2);
  await expect(detail.locator('tbody')).toContainText('list_issues');

  const calls = detail.locator('.call');
  await expect(calls).toHaveCount(2);
  await expect(calls.nth(0)).toContainText('#2');
  await expect(calls.nth(0)).toContainText('create_issue');
  await expect(calls.nth(0)).not.toContainText('failed');
  // Arguments and results render on demand, as text: markup in them never runs.
  await calls.nth(0).locator('summary', { hasText: 'Arguments' }).click();
  await expect(calls.nth(0).locator('pre').first()).toContainText('<img src=x onerror=window.pwned=1>');
  await calls.nth(0).locator('summary', { hasText: 'Result' }).click();
  await expect(calls.nth(0).locator('pre').nth(1)).toContainText('Created issue #42');
  expect(await frame.locator('body').evaluate(() => (window as any).pwned)).toBeUndefined();
  await expect(calls.nth(1)).toContainText('list_issues');
  await expect(calls.nth(1)).toContainText('failed');

  // A resource read shows its method and URI.
  await servers.nth(2).click();
  await expect(detail.locator('h2').first()).toHaveText('filesystem');
  await expect(detail.locator('.call')).toContainText('resources/read');
  await expect(detail.locator('.call')).toContainText('file:///work/app/README.md');

  // A call known only by its tool name still lands under its server, and a session-store failure
  // that kept the mcp action and a string result is marked from its error type.
  await servers.nth(1).click();
  await expect(detail.locator('tbody')).toContainText('save_issue');
  const failedCall = detail.locator('.call', { hasText: 'list_teams' });
  await expect(failedCall).toContainText('failed');
  await expect(detail.locator('.call', { hasText: 'save_issue' })).not.toContainText('failed');
});

test('mcp: a call whose name does not split is joined to the result that names its server', async ({ page }) => {
  // Oh My Pi's mcp__<server>_<tool> names cannot be split, so its pre-tool event and its approval
  // carry no server; the result does. All three are one call, under the server the result names,
  // with the approval decided on it.
  await page.goto('/session.html?id=ompmcp&lens=mcp');
  const frame = page.frameLocator('iframe.lens-frame');
  await expect(frame.locator('#summary')).toHaveText('1 call to 1 tool on 1 server · none failed.');
  await expect(frame.locator('#list button.row')).toHaveCount(1);
  await expect(frame.locator('#list button.row').first()).toContainText('beacon-managed');

  const call = frame.locator('#detail .call');
  await expect(call).toHaveCount(1);
  await expect(call).toContainText('beacon_lookup');
  await expect(call).not.toContainText('mcp__beacon_managed_beacon_lookup');
  await expect(call).toContainText('approval: approve');
});

test('mcp: a trace without MCP calls says so', async ({ page }) => {
  await page.goto('/session.html?id=rich&lens=mcp');
  const frame = page.frameLocator('iframe.lens-frame');
  await expect(frame.locator('#summary')).toHaveText('No MCP tool calls in this trace.');
  await expect(frame.locator('#layout')).toBeHidden();
});
