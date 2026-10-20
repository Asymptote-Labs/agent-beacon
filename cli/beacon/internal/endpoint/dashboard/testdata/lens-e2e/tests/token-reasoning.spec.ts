import { expect, test } from '@playwright/test';

// These fixed expectations follow tokens.Usage.TotalTokens: reasoning is an
// output breakdown, not another disjoint quantity to add to the API's output.
const cases = [
  { name: 'reasoning subset', usage: { input_tokens: 100, output_tokens: 50, reasoning_output_tokens: 20 }, total: '150', compact: '150', output: '50', reasoning: '20', width: '33%', note: true },
  { name: 'existing Gemini aggregate', usage: { input_tokens: 9600, output_tokens: 870, cache_read_input_tokens: 8000, reasoning_output_tokens: 420 }, total: '18,470', compact: '18.5K', output: '870', reasoning: '420', width: '5%', note: true },
  { name: 'zero reasoning', usage: { input_tokens: 100, output_tokens: 50, reasoning_output_tokens: 0 }, total: '150', compact: '150', output: '50', reasoning: '0', width: '33%', note: false },
  { name: 'absent reasoning', usage: { input_tokens: 100, output_tokens: 50 }, total: '150', compact: '150', output: '50', reasoning: '0', width: '33%', note: false },
  { name: 'null reasoning', usage: { input_tokens: 100, output_tokens: 50, reasoning_output_tokens: null }, total: '150', compact: '150', output: '50', reasoning: '0', width: '33%', note: false },
  { name: 'all output is reasoning', usage: { output_tokens: 50, reasoning_output_tokens: 50 }, total: '50', compact: '50', output: '50', reasoning: '50', width: '100%', note: true },
  { name: 'cached input remains disjoint', usage: { input_tokens: 100, output_tokens: 50, cache_read_input_tokens: 30, cache_creation_input_tokens: 20, reasoning_output_tokens: 20 }, total: '200', compact: '200', output: '50', reasoning: '20', width: '25%', note: true },
  { name: 'partial reasoning without output does not invent totals', usage: { input_tokens: 100, reasoning_output_tokens: 7 }, total: '100', compact: '100', output: '0', reasoning: '7', width: '0%', note: false },
  { name: 'empty usage', usage: {}, total: '0', compact: '0', output: '0', reasoning: '0', width: null, note: false },
];

for (const fixture of cases) {
  test(`token totals: ${fixture.name}`, async ({ page }) => {
    const usage = { ...fixture.usage, cost_usd: 0.1256, estimated_cost_usd: 0.2345, events: 1 };
    const groups = [{ key: 'owned-model', usage }];
    await page.route('**/api/status', (route) => route.fulfill({ json: { log_path: '/owned/runtime.jsonl' } }));
    await page.route('**/api/tokens?*', (route) => route.fulfill({ json: {
      totals: usage, total_events: 1, events_with_usage: 1,
      series: [{ start: '2026-10-19T12:00:00Z', usage }],
      by_model: groups, by_harness: groups, by_session: groups,
    } }));
    await page.goto('/tokens.html');
    const cards = page.locator('#token-cards .card');
    const card = (label: string) => cards.filter({ has: page.locator('.muted').filter({ hasText: new RegExp(`^${label}$`) }) }).locator('.value');
    await expect(card('Total Tokens')).toHaveText(fixture.total);
    await expect(card('Output Tokens')).toHaveText(fixture.output);
    await expect(card('Reasoning')).toHaveText(fixture.reasoning);
    await expect(card('Reported cost \\(USD\\)')).toHaveText('0.1256');
    await expect(card('Est. cost \\(USD\\)')).toHaveText('~0.2345');
    // All three breakdowns must agree with the API/CLI total, including zero
    // token groups retained because they carry a runtime-reported cost.
    await expect(page.locator('#token-breakdowns .panel-total')).toHaveText([fixture.compact, fixture.compact, fixture.compact]);
    await expect(page.locator('#token-models tr td').nth(2)).toHaveText(fixture.output);
    const note = page.locator('#token-split .token-reasoning-note');
    if (fixture.note) {
      await expect(note).toContainText(fixture.reasoning);
      await expect(note).toContainText('included in output');
    } else {
      await expect(note).toHaveCount(0);
    }
    // The donut's segments are disjoint. Reasoning stays available as a
    // separate annotation/card rather than occupying another slice.
    if (fixture.width === null) {
      await expect(page.locator('#token-split .token-donut')).toHaveCount(0);
      await expect(page.locator('#token-throughput .throughput-row')).toHaveCount(0);
    } else {
      await expect(page.locator('#token-split .token-split-legend')).not.toContainText('Reasoning');
      await expect(page.locator('#token-split .token-donut strong')).toHaveText(fixture.compact);
      await expect(page.locator('#token-throughput .throughput-row strong')).toHaveText(fixture.total);
      await expect(page.locator('#token-throughput .throughput-output')).toHaveAttribute('style', `width: ${fixture.width}`);
    }
  });
}
