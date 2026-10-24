import { expect, test } from '@playwright/test';

const loneSurrogate = /[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/;

test('a long prompt is shortened without cutting an emoji in half', async ({ page }) => {
  // 180 characters fit; the cut keeps 88 from each end, and the emoji sits across both cuts.
  const prompt = `${'a'.repeat(87)}😀${'b'.repeat(40)}😀${'c'.repeat(87)}`;
  await page.route('**/api/status', (route) => route.fulfill({ json: {} }));
  await page.route('**/api/summary?*', (route) => route.fulfill({ json: {} }));
  await page.route('**/api/sessions?*', (route) =>
    route.fulfill({ json: { sessions: [{ id: 'session-emoji', event_count: 1, original_prompt: prompt }] } }),
  );
  await page.goto('/');
  const cell = page.locator('#events tr[data-session-id="session-emoji"] .col-message').first();
  await expect(cell).toContainText('...');
  const text = await cell.evaluate((node) => node.textContent || '');
  expect(loneSurrogate.test(text), `rendered ${JSON.stringify(text)}`).toBe(false);
  expect(text).toContain(`${'a'.repeat(87)}😀...`);
  expect(text).toContain(`...😀${'c'.repeat(87)}`);
});
