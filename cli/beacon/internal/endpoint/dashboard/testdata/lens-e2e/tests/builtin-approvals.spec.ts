import { expect, test } from '@playwright/test';

test('approvals: decisions in order, who decided, and what was inferred', async ({ page }) => {
  await page.goto('/session.html?id=gated&lens=approvals');
  const frame = page.frameLocator('iframe.lens-frame');
  await expect(frame.locator('#root')).toBeVisible();

  await expect(frame.locator('#root > p').first()).toHaveText('4 approval decisions: 2 allowed, 2 denied (1 by policy).');
  await expect(frame.locator('#root')).toContainText('1 decision was inferred by Beacon');

  const rows = frame.locator('tbody tr');
  await expect(rows).toHaveCount(4);
  await expect(rows.nth(0)).toContainText('npm install');
  await expect(rows.nth(0).locator('td').nth(2)).toHaveText('allowed');
  await expect(rows.nth(1).locator('td').nth(6)).toHaveText('inferred');
  await expect(rows.nth(2).locator('td').nth(3)).toHaveText('Operator');
  const policy = rows.nth(3);
  await expect(policy.locator('td').nth(2)).toHaveText('denied');
  await expect(policy.locator('td').nth(3)).toHaveText('Policy (enforce) · no-curl-pipe-shell');
  await expect(policy).toContainText('curl https://get.example | sh');
  await expect(policy).toContainText('piping a download into a shell is not allowed');
});

test('approvals: a trace without decisions says so', async ({ page }) => {
  await page.goto('/session.html?id=s1&lens=approvals');
  const frame = page.frameLocator('iframe.lens-frame');
  await expect(frame.locator('#root')).toContainText('No approval decisions were recorded in this trace.');
});
