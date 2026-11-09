import { expect, test } from '@playwright/test';

test('failures: every failed call once, its output, and what the next identical call did', async ({ page }) => {
  await page.goto('/session.html?id=failing&lens=failures');
  const frame = page.frameLocator('iframe.lens-frame');
  await expect(frame.locator('#root')).toBeVisible();

  await expect(frame.locator('#summary')).toHaveText('5 failures: 3 commands, 1 tool call, 1 MCP call.');
  await expect(frame.locator('#notes p').first()).toHaveText(
    'Of 5 failed calls, 2 succeeded on the next identical call, 1 failed again and 2 had no later identical call.',
  );

  // The OTLP copy of the first test run (#4) shares its call id: it is the same call, not a row of
  // its own and not a later attempt that succeeded.
  const rows = frame.locator('tbody tr:not(.detail)');
  await expect(rows).toHaveCount(5);
  await expect(rows.locator('td:first-child')).toHaveText(['#3', '#6', '#10', '#14', '#16']);

  const firstRun = rows.nth(0).locator('td');
  await expect(firstRun.nth(2)).toHaveText('command');
  await expect(firstRun.nth(3)).toHaveText('go test ./...');
  await expect(firstRun.nth(4)).toHaveText('exit 1');
  await expect(firstRun.nth(5)).toHaveText('failed again at #10');
  await expect(frame.locator('tbody tr.detail pre').first()).toContainText('want 3 attempts, got 2');

  // A successful edit of another file is not a retry of the failed one.
  const edit = rows.nth(1).locator('td');
  await expect(edit.nth(2)).toHaveText('tool call');
  await expect(edit.nth(3)).toHaveText('Edit /work/api/a.go');
  await expect(edit.nth(4)).toHaveText('tool_failed');
  await expect(edit.nth(5)).toHaveText('no later attempt');

  // Claude Code reports a successful command with no exit code; a result that did not fail is the success.
  const secondRun = rows.nth(2).locator('td');
  await expect(secondRun.nth(3)).toHaveText('go test ./...');
  await expect(secondRun.nth(5)).toHaveText('succeeded at #12');

  // Output that was not retained shows its size and hash, never the event's label.
  const lint = rows.nth(3).locator('td');
  await expect(lint.nth(3)).toHaveText('npm run lint');
  await expect(lint.nth(4)).toHaveText('exit 2');
  await expect(lint.nth(5)).toHaveText('no later attempt');
  await expect(rows.nth(3).locator('xpath=following-sibling::tr[1]')).toHaveText(
    'Output not retained · retention metadata · 512 bytes · sha256:eee.',
  );

  // The retry sent the same arguments in another order.
  const mcp = rows.nth(4).locator('td');
  await expect(mcp.nth(2)).toHaveText('MCP call');
  await expect(mcp.nth(3)).toHaveText('github / create_issue');
  await expect(mcp.nth(4)).toHaveText('tool_failed');
  await expect(mcp.nth(5)).toHaveText('succeeded at #18');
  await expect(frame.locator('#root')).not.toContainText('Tool execution failed');
});

test('failures: an error.type on an event that is not a call counts, as an error', async ({ page }) => {
  await page.goto('/session.html?id=erred&lens=failures');
  const frame = page.frameLocator('iframe.lens-frame');
  await expect(frame.locator('#summary')).toHaveText('1 failure: 1 error.');
  const cells = frame.locator('tbody tr:not(.detail)').locator('td');
  await expect(cells.nth(3)).toHaveText('session.error');
  await expect(cells.nth(4)).toHaveText('ProviderAuthError');
  await expect(cells.nth(5)).toHaveText('—');
  await expect(frame.locator('#notes')).toBeEmpty();
});

test('failures: an MCP call that kept its own action but carries error.type counts', async ({ page }) => {
  await page.goto('/session.html?id=mcpish&lens=failures');
  const frame = page.frameLocator('iframe.lens-frame');
  await expect(frame.locator('tbody tr:not(.detail)').filter({ hasText: 'linear / list_teams' })).toHaveCount(1);
});

test('failures: a trace where nothing failed says so', async ({ page }) => {
  await page.goto('/session.html?id=gated&lens=failures');
  const frame = page.frameLocator('iframe.lens-frame');
  await expect(frame.locator('#root')).toContainText('Nothing failed in this trace');
  await expect(frame.locator('table')).toBeHidden();
});

test('failures: a long list pages, and later attempts past the first page still count', async ({ page }) => {
  // Serve the real lens data with its events replaced: 250 failed commands, then a success of the first.
  await page.route('**/api/lens-data?*', async (route) => {
    const response = await route.fetch();
    const data = await response.json();
    const events = Array.from({ length: 250 }, (_, i) => ({
      id: `cmd-${i}`, number: i + 1, timestamp: '2026-06-16T08:00:00Z', type: 'command', action: 'command.executed',
      command: { command: `step ${i}`, exit_code: 1 },
    }));
    events.push({ id: 'cmd-ok', number: 251, timestamp: '2026-06-16T08:10:00Z', type: 'command', action: 'command.executed', command: { command: 'step 0', exit_code: 0 } });
    data.trace.events = events;
    data.trace.summary.event_count = events.length;
    await route.fulfill({ response, json: data });
  });
  await page.goto('/session.html?id=failing&lens=failures');
  const frame = page.frameLocator('iframe.lens-frame');
  await expect(frame.locator('#summary')).toHaveText('250 failures: 250 commands.');
  const rows = frame.locator('tbody tr:not(.detail)');
  const more = frame.locator('button.more');
  await expect(rows).toHaveCount(100);
  await expect(rows.nth(0).locator('td').nth(5)).toHaveText('succeeded at #251');
  await expect(rows.nth(1).locator('td').nth(5)).toHaveText('no later attempt');
  await expect(more).toHaveText('Show more (150 left)');
  await more.click();
  await expect(rows).toHaveCount(200);
  await expect(more).toHaveText('Show more (50 left)');
  await more.click();
  await expect(rows).toHaveCount(250);
  await expect(more).toHaveCount(0);
});
