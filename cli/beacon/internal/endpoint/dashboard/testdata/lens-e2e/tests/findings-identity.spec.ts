import { expect, test } from '@playwright/test';

function finding(id: string, severity = 'medium', rule = 'same-rule', session = 'same-session') {
  return {
    rule_id: rule, title: `Finding ${id}`, severity, posture: 'detect', session_id: session,
    reason: 'Owned finding fixture', events: [{ timestamp: '2026-10-17T12:00:00Z',
      event: { id, action: 'command.executed', category: 'process' },
      session: { id: session }, command: { command: `echo ${id}` } }],
  };
}

const first = finding('first-event');
const second = finding('second-event');
const critical = finding('critical-event', 'critical', 'new-rule', 'new-session');
const legacy = { ...first, events: [{ timestamp: '2026-10-17T12:00:00Z',
  event: { action: 'command.executed', category: 'process' },
  session: { id: 'same-session' }, command: { command: 'echo legacy-event' } }] };
const reorderedLegacy = { ...legacy, events: [{ command: { command: 'echo legacy-event' },
  session: { id: 'same-session' }, event: { category: 'process', action: 'command.executed' },
  timestamp: '2026-10-17T12:00:00Z' }] };

type Finding = ReturnType<typeof finding> | typeof legacy;
const cases: { name: string; before: Finding[]; after: Finding[]; expanded: string | null }[] = [
  { name: 'retains the chosen finding when a higher-severity match arrives',
    before: [first, second], after: [critical, first, second], expanded: 'first-event' },
  { name: 'distinguishes separate matches of the same rule in one session',
    before: [first, second], after: [second, first], expanded: 'first-event' },
  { name: 'retains an unchanged match', before: [first, second], after: [first, second], expanded: 'first-event' },
  { name: 'collapses a removed match instead of expanding its replacement',
    before: [first, second], after: [second], expanded: null },
  { name: 'collapses changed supporting evidence',
    before: [first], after: [{ ...first, events: second.events }], expanded: null },
  { name: 'clears expansion for empty findings', before: [first], after: [], expanded: null },
  { name: 'collapses a previously ambiguous duplicate even when only one remains',
    before: [first, first], after: [first], expanded: null },
  { name: 'collapses a match that becomes an ambiguous duplicate',
    before: [first], after: [first, first], expanded: null },
  { name: 'retains legacy evidence across JSON property reordering',
    before: [legacy], after: [reorderedLegacy], expanded: 'legacy-event' },
  { name: 'collapses changed legacy evidence without native IDs',
    before: [legacy], after: [{ ...legacy, events: [{ ...legacy.events[0],
      command: { command: 'echo changed-legacy-event' } }] }], expanded: null },
  { name: 'preserves the evidence step order in correlation identity',
    before: [{ ...first, events: [...first.events, ...second.events] }],
    after: [{ ...first, events: [...second.events, ...first.events] }], expanded: null },
];

for (const fixture of cases) {
  test(`findings ${fixture.name}`, async ({ page }) => {
    await page.clock.install();
    let current = fixture.before;
    // Keep this UI regression independent of the rule store: these are the
    // actual FindingsResponse fields rendered by the real dashboard page.
    await page.route('**/api/findings*', (route) => route.fulfill({ json: {
      findings: current, scanned: current.length, count: current.length,
    } }));
    await page.route('**/api/detections', (route) => route.fulfill({ json: { rules: [], count: 0 } }));
    await page.route('**/api/events?*', (route) => route.fulfill({ json: { events: [], count: 0 } }));
    await page.goto('/findings.html');
    const rows = page.locator('#findings-rows [data-finding-row]');
    await expect(rows).toHaveCount(fixture.before.length);
    await rows.first().click();
    const expanded = page.locator('#findings-rows [data-finding-row][aria-expanded="true"]');
    await expect(expanded).toHaveCount(1);
    const refreshed = page.waitForResponse((response) => new URL(response.url()).pathname === '/api/findings');
    current = fixture.after;
    await page.clock.runFor(15_000);
    await (await refreshed).finished();
    await expect(rows).toHaveCount(fixture.after.length);
    if (fixture.expanded === null) {
      await expect(expanded).toHaveCount(0);
      await expect(page.locator('#findings-rows .finding-detail')).toHaveCount(0);
    } else {
      await expect(expanded).toHaveCount(1);
      await expect(expanded).toContainText(fixture.before[0].title);
      await expect(page.locator('#findings-rows .finding-detail')).toHaveCount(1);
    }
  });
}
