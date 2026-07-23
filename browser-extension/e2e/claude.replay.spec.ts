import { test, expect, seedSettings } from './fixtures.js';
import { flatAttrs, recordByAction, resourceAttrs } from './helpers/otlp-assertions.js';

const CONV = '11111111-2222-3333-4444-555555555555';
const COMPLETION_PATH = '/api/organizations/org-test/chat_conversations/{conv}/completion';

test.beforeEach(async ({ mockCollector }) => {
  mockCollector.reset();
});

test('captures a simple Claude turn end-to-end into normalized OTLP', async ({
  context,
  serviceWorker,
  replay,
  mockCollector,
}) => {
  replay.setCase({
    site: 'claude',
    name: 'simple-turn',
    conversationId: CONV,
    prompt: 'What is the capital of France?',
    completionPath: COMPLETION_PATH,
  });
  await seedSettings(serviceWorker, {
    enabled: true,
    retention: 'full',
    endpoint: mockCollector.url,
    sites: { claude_web: true, chatgpt_web: true },
  });

  const page = await context.newPage();
  await page.goto('https://claude.ai/chat');
  // Wait until the page finished consuming the stream.
  await expect(page.locator('[data-testid="assistant-message"]')).toHaveAttribute(
    'data-complete',
    '1',
  );

  await expect.poll(() => mockCollector.received.length, { timeout: 10_000 }).toBeGreaterThan(0);

  const res = resourceAttrs(mockCollector.received[0]);
  expect(res['beacon.harness.name']).toBe('claude_web');
  expect(res['gen_ai.provider.name']).toBe('anthropic');

  // The prompt lands on its own prompt.submitted record (category "prompt") so
  // the collector lifts it into the queryable Event.prompt.text field.
  const promptRec = recordByAction(mockCollector.received, 'prompt.submitted');
  expect(promptRec, 'a prompt.submitted record should have been delivered').toBeTruthy();
  const promptAttrs = flatAttrs(promptRec!.attributes);
  expect(promptAttrs['beacon.event.category']).toBe('prompt');
  expect(promptAttrs['beacon.prompt.text']).toBe('What is the capital of France?');
  expect(promptAttrs['beacon.session.id']).toBe(CONV);

  // The response lands on the agent.response.completed record.
  const rec = recordByAction(mockCollector.received, 'agent.response.completed');
  expect(rec, 'a completed-response record should have been delivered').toBeTruthy();
  const attrs = flatAttrs(rec!.attributes);
  expect(attrs['gen_ai.conversation.id']).toBe(CONV);
  expect(attrs['gen_ai.response.model']).toBe('claude-opus-4-8');
  expect(String(attrs['gen_ai.output.messages'])).toContain('Paris');
});

test('captures a Claude tool call as a tool.invoked record', async ({
  context,
  serviceWorker,
  replay,
  mockCollector,
}) => {
  replay.setCase({
    site: 'claude',
    name: 'with-tool-call',
    conversationId: CONV,
    prompt: 'What is the weather today?',
    completionPath: COMPLETION_PATH,
  });
  await seedSettings(serviceWorker, {
    enabled: true,
    retention: 'full',
    endpoint: mockCollector.url,
    sites: { claude_web: true, chatgpt_web: true },
  });

  const page = await context.newPage();
  await page.goto('https://claude.ai/chat');
  await expect(page.locator('[data-testid="assistant-message"]')).toHaveAttribute(
    'data-complete',
    '1',
  );

  await expect
    .poll(() => recordByAction(mockCollector.received, 'tool.invoked'), { timeout: 10_000 })
    .toBeTruthy();

  const tool = flatAttrs(recordByAction(mockCollector.received, 'tool.invoked')!.attributes);
  expect(tool['tool.name']).toBe('web_search');
  expect(tool['gen_ai.tool.call.id']).toBe('toolu_abc');
  expect(String(tool['gen_ai.tool.call.arguments'])).toContain('weather today');
});

test('respects the disabled toggle (no delivery)', async ({
  context,
  serviceWorker,
  replay,
  mockCollector,
}) => {
  replay.setCase({
    site: 'claude',
    name: 'simple-turn',
    conversationId: CONV,
    prompt: 'What is the capital of France?',
    completionPath: COMPLETION_PATH,
  });
  await seedSettings(serviceWorker, {
    enabled: false,
    retention: 'full',
    endpoint: mockCollector.url,
    sites: { claude_web: true, chatgpt_web: true },
  });

  const page = await context.newPage();
  await page.goto('https://claude.ai/chat');
  await expect(page.locator('[data-testid="assistant-message"]')).toHaveAttribute(
    'data-complete',
    '1',
  );
  // Give any (erroneous) delivery a chance to arrive, then assert none did.
  await page.waitForTimeout(1000);
  expect(mockCollector.received).toHaveLength(0);
});
