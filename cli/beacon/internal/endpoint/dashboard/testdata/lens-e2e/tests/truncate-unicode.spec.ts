import { expect, test } from '@playwright/test';

// 180 characters fit, and the cut keeps 88 from each end. The characters that sit across each cut
// are built from several code points (a family joined by zero-width joiners, a flag of two
// regional indicators), so a cut by code unit or by code point would split them.
for (const [name, first, last] of [
  ['an emoji', '😀', '😀'],
  ['a family emoji or a flag', '👨‍👩‍👧', '🇯🇵'],
]) {
  test(`a long prompt is shortened without cutting ${name} in half`, async ({ page }) => {
    const prompt = `${'a'.repeat(87)}${first}${'b'.repeat(40)}${last}${'c'.repeat(87)}`;
    await page.route('**/api/status', (route) => route.fulfill({ json: {} }));
    await page.route('**/api/summary?*', (route) => route.fulfill({ json: {} }));
    await page.route('**/api/sessions?*', (route) =>
      route.fulfill({ json: { sessions: [{ id: 'session-emoji', event_count: 1, original_prompt: prompt }] } }),
    );
    await page.goto('/');
    const cell = page.locator('#events tr[data-session-id="session-emoji"] .col-message').first();
    await expect(cell).toContainText('...');
    const text = await cell.evaluate((node) => node.textContent || '');
    expect(text).toContain(`${'a'.repeat(87)}${first}...${last}${'c'.repeat(87)}`);
  });
}
