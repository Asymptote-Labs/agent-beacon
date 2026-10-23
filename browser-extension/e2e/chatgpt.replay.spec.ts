import { test, expect, seedSettings } from './fixtures.js';
import { flatAttrs, recordByAction, resourceAttrs } from './helpers/otlp-assertions.js';

// conversation id embedded in the fixture's resume_conversation_token
const CONV = '6a73b75a-18d8-83ea-bbcf-984208806963';
const COMPLETION_PATH = '/backend-api/f/conversation';

test.beforeEach(async ({ mockCollector }) => {
  mockCollector.reset();
});

test('captures a simple ChatGPT turn end-to-end into normalized OTLP', async ({
  context,
  serviceWorker,
  replay,
  mockCollector,
}) => {
  replay.setCase({
    site: 'chatgpt',
    name: 'simple-turn',
    conversationId: CONV,
    prompt: 'good evening',
    completionPath: COMPLETION_PATH,
  });
  await seedSettings(serviceWorker, {
    enabled: true,
    retention: 'full',
    endpoint: mockCollector.url,
    sites: { claude_web: true, chatgpt_web: true },
  });

  const page = await context.newPage();
  await page.goto('https://chatgpt.com/');
  await expect(page.locator('[data-testid="assistant-message"]')).toHaveAttribute(
    'data-complete',
    '1',
  );

  await expect.poll(() => mockCollector.received.length, { timeout: 10_000 }).toBeGreaterThan(0);

  const res = resourceAttrs(mockCollector.received[0]);
  expect(res['beacon.harness.name']).toBe('chatgpt_web');
  expect(res['gen_ai.provider.name']).toBe('openai');

  // Prompt lands on its own prompt.submitted record with the conv id from the stream.
  const promptRec = recordByAction(mockCollector.received, 'prompt.submitted');
  expect(promptRec, 'a prompt.submitted record should have been delivered').toBeTruthy();
  const promptAttrs = flatAttrs(promptRec!.attributes);
  expect(promptAttrs['beacon.prompt.text']).toBe('good evening');
  expect(promptAttrs['beacon.session.id']).toBe(CONV);
  expect(JSON.parse(String(promptAttrs['gen_ai.input.messages']))).toEqual([
    { role: 'user', parts: [{ type: 'text', content: 'good evening' }] },
  ]);

  // Response lands on the agent.response.completed record.
  const rec = recordByAction(mockCollector.received, 'agent.response.completed');
  expect(rec, 'a completed-response record should have been delivered').toBeTruthy();
  const attrs = flatAttrs(rec!.attributes);
  expect(attrs['gen_ai.conversation.id']).toBe(CONV);
  expect(attrs['gen_ai.response.model']).toBe('gpt-5-6-thinking');
  expect(JSON.parse(String(attrs['gen_ai.output.messages']))).toEqual([
    { role: 'assistant', parts: [{ type: 'text', content: 'Good evening! Hello to you as well. Hope your day is going well.' }] },
  ]);
});

test('captures an answer split around a web search once, with the search as a tool call', async ({
  context,
  serviceWorker,
  replay,
  mockCollector,
}) => {
  const SPLIT_CONV = '6adb0000-0000-8000-8000-000000000001';
  replay.setCase({
    site: 'chatgpt',
    name: 'web-search-split',
    conversationId: SPLIT_CONV,
    prompt: 'Boston Marathon',
    completionPath: COMPLETION_PATH,
  });
  await seedSettings(serviceWorker, {
    enabled: true,
    retention: 'full',
    endpoint: mockCollector.url,
    sites: { claude_web: true, chatgpt_web: true },
  });

  const page = await context.newPage();
  await page.goto('https://chatgpt.com/');
  await expect(page.locator('[data-testid="assistant-message"]')).toHaveAttribute(
    'data-complete',
    '1',
  );
  await expect
    .poll(() => recordByAction(mockCollector.received, 'tool.invoked'), { timeout: 10_000 })
    .toBeTruthy();

  const resp = flatAttrs(recordByAction(mockCollector.received, 'agent.response.completed')!.attributes);
  expect(JSON.parse(String(resp['gen_ai.output.messages']))).toEqual([
    {
      role: 'assistant',
      parts: [{ type: 'text', content: "The Boston Marathon is the world's oldest annual marathon.\n\n**Next race:** Monday, April 19, 2027." }],
    },
  ]);
  const tool = flatAttrs(recordByAction(mockCollector.received, 'tool.invoked')!.attributes);
  expect(tool['gen_ai.tool.call.name']).toBe('web_search');
  expect(JSON.parse(String(tool['gen_ai.tool.call.arguments']))).toEqual({ queries: ['2027 Boston Marathon date'] });
  expect(JSON.parse(String(tool['gen_ai.tool.call.result'])).search_result_groups).toHaveLength(1);
});
