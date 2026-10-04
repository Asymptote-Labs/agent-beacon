import { expect, test } from '@playwright/test';

test('security review: findings grouped by risk, the combination flagged, evidence resolved', async ({ page }) => {
  await page.goto('/session.html?id=risky&lens=security-review');
  const frame = page.frameLocator('iframe.lens-frame');
  await expect(frame.locator('#root')).toBeVisible();

  await expect(frame.locator('#root > p').first()).toHaveText('5 findings across 77 rules evaluated.');
  await expect(frame.locator('.alert')).toContainText('combines untrusted input, sensitive access and a consequential action');

  const rows = frame.locator('tbody tr');
  await expect(rows.nth(0)).toContainText('Untrusted input');
  await expect(rows.nth(0).locator('td').nth(2)).toHaveText('2');
  await expect(rows.nth(1).locator('td').nth(2)).toHaveText('1');
  await expect(rows.nth(2).locator('td').nth(2)).toHaveText('2');
  await expect(rows.nth(2).locator('td').nth(3)).toHaveText('high');

  const sensitive = frame.locator('section[data-group="sensitive"]');
  await expect(sensitive).toContainText('credential-file-read');
  await expect(sensitive).toContainText('read /home/dev/.aws/credentials');

  // A correlation finding cites both steps, in order.
  const exfil = frame.locator('section[data-group="consequential"] .finding', { hasText: 'secret-read-then-egress' });
  await expect(exfil.locator('.evidence li')).toHaveCount(2);
  await expect(exfil.locator('.evidence li').nth(1)).toContainText('curl -d @/home/dev/.aws/credentials');
});

test('security review: a trace with no findings in a group still shows the summary', async ({ page }) => {
  await page.goto('/session.html?id=rich&lens=security-review');
  const frame = page.frameLocator('iframe.lens-frame');
  await expect(frame.locator('#root')).toBeVisible();
  await expect(frame.locator('.alert')).toHaveCount(0);
  await expect(frame.locator('tbody tr')).toHaveCount(3);
});
