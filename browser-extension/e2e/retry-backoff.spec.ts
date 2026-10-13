import { test, expect, seedSettings } from './fixtures.js';
import { allRecords, flatAttrs } from './helpers/otlp-assertions.js';

test('new turns do not exhaust retries of batches awaiting their alarm', async ({ context, serviceWorker, replay, mockCollector }) => {
  test.setTimeout(60_000);
  mockCollector.setStatus(503);
  await seedSettings(serviceWorker, { enabled: true, retention: 'full', endpoint: mockCollector.url });
  const conversations = Array.from({ length: 8 }, (_, i) => `${String(i + 1).padStart(8, '0')}-1111-2222-3333-444444444444`);
  const pages = await Promise.all(conversations.map(async (conversationId, i) => {
    replay.setCase({ site: 'claude', name: 'simple-turn', conversationId, prompt: `Offline collector turn ${i}`, completionPath: '/api/organizations/org-test/chat_conversations/{conv}/completion' });
    return context.newPage();
  }));
  await Promise.all(pages.map((page, i) => page.goto(`https://claude.ai/chat?conv=${conversations[i]}`)));
  await Promise.all(pages.map((page) => expect(page.locator('[data-testid="assistant-message"]')).toHaveAttribute('data-complete', '1')));
  // All failed sends have finished before inspecting durable state. Count the
  // distinct attempted prompts, not response completion in the replay page.
  await expect.poll(() => new Set(allRecords(mockCollector.received).map((r) => flatAttrs(r.attributes)).filter((a) => a['beacon.event.action'] === 'prompt.submitted').map((a) => a['beacon.session.id'])).size).toBe(8);
  await expect.poll(() => serviceWorker.evaluate(async () => ((await chrome.storage.local.get('delivery_queue')).delivery_queue ?? []).length)).toBe(8);
  mockCollector.reset();
  mockCollector.setStatus(200);
  // Use the real alarm installed by delivery, not a direct test-only flush.
  await expect.poll(() => serviceWorker.evaluate(async () => ((await chrome.storage.local.get('delivery_queue')).delivery_queue ?? []).length), { timeout: 40_000 }).toBe(0);
  expect(new Set(allRecords(mockCollector.received).map((r) => flatAttrs(r.attributes)).filter((a) => a['beacon.event.action'] === 'prompt.submitted').map((a) => a['beacon.session.id']))).toEqual(new Set(conversations));
});
