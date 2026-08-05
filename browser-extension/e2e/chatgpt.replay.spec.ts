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

  // Response lands on the agent.response.completed record.
  const rec = recordByAction(mockCollector.received, 'agent.response.completed');
  expect(rec, 'a completed-response record should have been delivered').toBeTruthy();
  const attrs = flatAttrs(rec!.attributes);
  expect(attrs['gen_ai.conversation.id']).toBe(CONV);
  expect(attrs['gen_ai.response.model']).toBe('gpt-5-6-thinking');
  expect(String(attrs['gen_ai.output.messages'])).toContain('Good evening');
});
