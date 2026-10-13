import { test, expect, seedSettings } from './fixtures.js';
import { allRecords, flatAttrs } from './helpers/otlp-assertions.js';

const COMPLETION = '/api/organizations/org-test/chat_conversations/{conv}/completion';

test('a stalled collector cannot prevent later turns from becoming durable', async ({
  context, serviceWorker, replay, mockCollector,
}) => {
  test.setTimeout(60_000);
  const conversations = [
    'aaaaaaaa-1010-2020-3030-404040404040',
    'bbbbbbbb-1010-2020-3030-404040404040',
  ];
  for (const [i, conversationId] of conversations.entries()) {
    replay.setCase({ site: 'claude', name: 'simple-turn', conversationId,
      prompt: `durable turn ${i}`, completionPath: COMPLETION });
  }
  await seedSettings(serviceWorker, {
    enabled: true, retention: 'full', endpoint: mockCollector.url,
    sites: { claude_web: true, chatgpt_web: true },
  });
  mockCollector.holdResponses();

  const first = await context.newPage();
  await first.goto(`https://claude.ai/chat?conv=${conversations[0]}`);
  await expect(first.locator('[data-testid="assistant-message"]')).toHaveAttribute('data-complete', '1');
  await expect.poll(() => mockCollector.received.length).toBe(1);

  const second = await context.newPage();
  await second.goto(`https://claude.ai/chat?conv=${conversations[1]}`);
  await expect(second.locator('[data-testid="assistant-message"]')).toHaveAttribute('data-complete', '1');

  // Inspect actual extension storage while the actual loopback server holds
  // HTTP responses open. The timed-out first batch must survive there, and
  // the next completed turn must be persisted despite the stalled POST.
  const queued = async () => serviceWorker.evaluate(async () =>
    (await chrome.storage.local.get('delivery_queue')).delivery_queue ?? []);
  await expect.poll(async () => (await queued()).length, { timeout: 15_000 }).toBe(2);
  const durable = await queued();
  expect(durable[0].attempts).toBeGreaterThanOrEqual(1);
  const queuedSessions = durable.map((item: { envelope: Parameters<typeof allRecords>[0][number] }) =>
    flatAttrs(allRecords([item.envelope])[0].attributes)['beacon.session.id']);
  expect(queuedSessions).toEqual(conversations);

  // Let the same collector acknowledge requests. Existing FIFO processing,
  // rather than a replacement queue or manually edited storage, drains both.
  mockCollector.releaseResponses();
  await expect.poll(async () => (await queued()).length, { timeout: 15_000 }).toBe(0);
  const prompts = allRecords(mockCollector.received).map((r) => flatAttrs(r.attributes))
    .filter((a) => a['beacon.event.action'] === 'prompt.submitted');
  for (const [i, session] of conversations.entries()) {
    expect(prompts.some((a) => a['beacon.session.id'] === session &&
      a['beacon.prompt.text'] === `durable turn ${i}`)).toBe(true);
  }
});
