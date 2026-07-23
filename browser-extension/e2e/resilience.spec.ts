import { test, expect, seedSettings } from './fixtures.js';
import { allRecords, flatAttrs, recordByAction } from './helpers/otlp-assertions.js';

const COMPLETION = '/api/organizations/org-test/chat_conversations/{conv}/completion';

const FULL_SETTINGS = (endpoint: string) => ({
  enabled: true,
  retention: 'full' as const,
  endpoint,
  sites: { claude_web: true, chatgpt_web: true },
});

test.beforeEach(async ({ mockCollector }) => {
  mockCollector.reset();
});

test('two concurrent tabs stay correlated to their own conversations', async ({
  context,
  serviceWorker,
  replay,
  mockCollector,
}) => {
  const A = 'aaaaaaaa-1111-2222-3333-444444444444';
  const B = 'bbbbbbbb-5555-6666-7777-888888888888';
  replay.setCase({ site: 'claude', name: 'simple-turn', conversationId: A, prompt: 'capital of France?', completionPath: COMPLETION });
  replay.setCase({ site: 'claude', name: 'with-tool-call', conversationId: B, prompt: 'weather in NYC?', completionPath: COMPLETION });
  await seedSettings(serviceWorker, FULL_SETTINGS(mockCollector.url));

  const [p1, p2] = [await context.newPage(), await context.newPage()];
  await Promise.all([
    p1.goto(`https://claude.ai/chat?conv=${A}`),
    p2.goto(`https://claude.ai/chat?conv=${B}`),
  ]);
  await Promise.all([
    expect(p1.locator('[data-testid="assistant-message"]')).toHaveAttribute('data-complete', '1'),
    expect(p2.locator('[data-testid="assistant-message"]')).toHaveAttribute('data-complete', '1'),
  ]);

  // Wait until B's tool call (the last-arriving signal) has been delivered.
  await expect
    .poll(() => recordByAction(mockCollector.received, 'tool.invoked'), { timeout: 10_000 })
    .toBeTruthy();

  const recs = allRecords(mockCollector.received).map((r) => flatAttrs(r.attributes));
  const promptFor = (sid: string) =>
    recs.find((a) => a['beacon.session.id'] === sid && a['beacon.event.action'] === 'prompt.submitted');

  // Prompts are attributed to the correct conversation (no cross-contamination).
  expect(promptFor(A)?.['beacon.prompt.text']).toBe('capital of France?');
  expect(promptFor(B)?.['beacon.prompt.text']).toBe('weather in NYC?');

  // The tool call belongs only to conversation B (its fixture had one); A had none.
  const toolSessions = recs
    .filter((a) => a['beacon.event.action'] === 'tool.invoked')
    .map((a) => a['beacon.session.id']);
  expect(toolSessions).toContain(B);
  expect(toolSessions).not.toContain(A);
});

test('aborted stream still delivers a best-effort agent.response (not completed)', async ({
  context,
  serviceWorker,
  replay,
  mockCollector,
}) => {
  const C = 'cccccccc-9999-0000-1111-222222222222';
  replay.setCase({ site: 'claude', name: 'aborted', conversationId: C, prompt: 'tell me a story', completionPath: COMPLETION });
  await seedSettings(serviceWorker, FULL_SETTINGS(mockCollector.url));

  const page = await context.newPage();
  await page.goto(`https://claude.ai/chat?conv=${C}`);
  await expect(page.locator('[data-testid="assistant-message"]')).toHaveAttribute('data-complete', '1');

  // A partial turn is emitted as agent.response (NOT agent.response.completed).
  await expect
    .poll(() => recordByAction(mockCollector.received, 'agent.response'), { timeout: 10_000 })
    .toBeTruthy();
  expect(recordByAction(mockCollector.received, 'agent.response.completed')).toBeFalsy();

  const resp = flatAttrs(recordByAction(mockCollector.received, 'agent.response')!.attributes);
  expect(resp['beacon.session.id']).toBe(C);
  // The prompt was still captured for the aborted turn.
  expect(recordByAction(mockCollector.received, 'prompt.submitted')).toBeTruthy();
});
