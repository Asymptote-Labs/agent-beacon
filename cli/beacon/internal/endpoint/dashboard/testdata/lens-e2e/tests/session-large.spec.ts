import { expect, test } from '@playwright/test';

const start = Date.parse('2026-10-20T00:00:00Z');
const sparse = (...entries: [number, number][]) => {
  const counts = Array(24).fill(0);
  for (const [index, count] of entries) counts[index] = count;
  return counts;
};
const cases = [
  { name: '150000 internal telemetry events', timestamps: () => Array.from({ length: 150000 }, (_, i) => new Date(start + i).toISOString()), counts: Array(24).fill(6250) },
  { name: 'empty session', timestamps: () => [], counts: sparse() },
  { name: 'one event', timestamps: () => [new Date(start).toISOString()], counts: sparse([0, 1]) },
  { name: 'chronological endpoints and midpoint', timestamps: () => [0, 1, 2].map((i) => new Date(start + i).toISOString()), counts: sparse([0, 1], [12, 1], [23, 1]) },
  { name: 'reversed input order', timestamps: () => [2, 1, 0].map((i) => new Date(start + i).toISOString()), counts: sparse([0, 1], [12, 1], [23, 1]) },
  { name: 'equal timestamps', timestamps: () => Array(3).fill(new Date(start).toISOString()), counts: sparse([0, 3]) },
  { name: 'missing and invalid timestamps preserve the first bucket fallback', timestamps: () => [undefined, 'not-a-timestamp'], counts: sparse([0, 2]) },
];

for (const fixture of cases) {
  test(`session activity: ${fixture.name}`, async ({ page }) => {
    const errors: string[] = [];
    page.on('pageerror', (error) => errors.push(error.message));
    const events = fixture.timestamps().map((timestamp, i) => ({
      id: `owned-${i}`, line: i + 1,
      event: { timestamp, severity: 'info', session: { id: 'owned-large-session' },
        event: { id: `owned-event-${i}`, category: 'metric', action: 'metric.observed' } },
    }));
    await page.route('**/api/status', (route) => route.fulfill({ json: { log_path: '/owned/runtime.jsonl' } }));
    await page.route('**/api/lenses', (route) => route.fulfill({ json: { lenses: [] } }));
    await page.route('**/api/session?*', (route) => route.fulfill({ json: {
      session: { id: 'owned-large-session', event_count: events.length,
        first_event_at: '2026-10-20T00:00:00Z', last_event_at: '2026-10-20T00:02:30Z' },
      events,
    } }));
    await page.goto('/session.html?id=owned-large-session');
    await expect(page.locator('#session-detail-view')).toBeVisible();
    await expect(page.locator('#session-detail-status')).toBeHidden();
    await expect(page.locator('#session-badges')).toContainText(`${events.length} events captured`);
    const bars = page.locator('#session-activity-chart .session-chart-bar');
    await expect(bars).toHaveCount(24);
    // Fixed expected bucket populations are independent of the implementation.
    await expect.poll(() => bars.evaluateAll((nodes) => nodes.map((node) => node.getAttribute('title')))).toEqual(
      fixture.counts.map((count) => `${count} event${count === 1 ? '' : 's'}`),
    );
    await expect(page.locator('#result-meta')).toContainText(`0 of ${events.length} events`);
    await expect(page.locator('#events')).toHaveText('No events match these filters.');
    await expect(page.locator('#session-activity-summary')).toContainText(`${events.length} events`);
    expect(errors).toEqual([]);
  });
}
