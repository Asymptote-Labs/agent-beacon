import { expect, test } from '@playwright/test';
import type { Page } from '@playwright/test';

// A scan that failed a rule on some event, or skipped a rule needing a newer Beacon, did not judge
// everything. Every surface that shows findings says so, rather than calling the trace clean.

// Serve the real lens data with no matches and an incomplete scan, which the fixture log cannot
// produce: a rule fails only on a command line past its cost limit.
async function incompleteScan(page: Page) {
  await page.route('**/api/lens-data?*', async (route) => {
    const response = await route.fetch();
    const data = await response.json();
    data.findings.items = [];
    data.findings.failed = ['credential-file-read'];
    data.findings.skipped = ['rule "future-rule" requires threat-rules/v99.0 (future.rule.yaml)'];
    await route.fulfill({ response, json: data });
  });
}

test('security review: an incomplete scan is not "No rule matched"', async ({ page }) => {
  await incompleteScan(page);
  await page.goto('/session.html?id=risky&lens=security-review');
  const frame = page.frameLocator('iframe.lens-frame');
  await expect(frame.locator('#root')).toBeVisible({ timeout: 15_000 });
  await expect(frame.locator('#root > p').first()).toHaveText('No findings across 87 rules evaluated.');
  await expect(frame.locator('#root')).toContainText('The scan is incomplete: credential-file-read could not be evaluated on some events');
  await expect(frame.locator('#root')).toContainText('1 rule was skipped because it needs a newer Beacon.');
  await expect(frame.locator('#root')).not.toContainText('No rule matched');
});

test('activity: an incomplete scan is not "No rule matched"', async ({ page }) => {
  await incompleteScan(page);
  await page.goto('/session.html?id=risky&lens=activity');
  const frame = page.frameLocator('iframe.lens-frame');
  await expect(frame.locator('#root')).toBeVisible({ timeout: 15_000 });
  await expect(frame.locator('#root')).toContainText('No findings (87 rules evaluated).');
  await expect(frame.locator('#root')).toContainText('The scan is incomplete: credential-file-read could not be evaluated on some events');
  await expect(frame.locator('#root')).toContainText('1 rule was skipped because it needs a newer Beacon.');
  await expect(frame.locator('#root')).not.toContainText('No rule matched');
});

test('findings page: failed and skipped rules are both named', async ({ page }) => {
  await page.route('**/api/findings*', (route) => route.fulfill({ json: {
    findings: [], scanned: 3, count: 0,
    failed: [{ rule_id: 'credential-file-read', events: 2, first_event_id: 'e1', error: 'cost limit exceeded' }],
    skipped: ['rule "future-rule" requires threat-rules/v99.0 (future.rule.yaml)'],
  } }));
  await page.route('**/api/detections', (route) => route.fulfill({ json: { rules: [], count: 0 } }));
  await page.route('**/api/events?*', (route) => route.fulfill({ json: { events: [], count: 0 } }));
  await page.goto('/findings.html');
  const notice = page.locator('#findings-notice');
  await expect(notice).toBeVisible();
  await expect(notice).toContainText('The scan is incomplete.');
  await expect(notice).toContainText('credential-file-read on 2 events (first e1): cost limit exceeded');
  await expect(notice).toContainText('A rule was skipped because it needs a newer Beacon: rule "future-rule" requires threat-rules/v99.0');
});
