import { test, expect, seedSettings } from './fixtures.js';
import { flatAttrs, recordByAction } from './helpers/otlp-assertions.js';

const CONV = '11111111-2222-3333-4444-555555555555';
const REQUESTED = 'requested-claude-alias';

for (const requestStyle of ['init', 'request'] as const) {
  test(`keeps the requested Claude model through ${requestStyle} capture`, async ({
    context, serviceWorker, replay, mockCollector,
  }) => {
    replay.setCase({
      site: 'claude', name: 'simple-turn', conversationId: CONV,
      prompt: 'What is the capital of France?',
      requestModel: REQUESTED, requestStyle,
      completionPath: '/api/organizations/org-test/chat_conversations/{conv}/completion',
    });
    await seedSettings(serviceWorker, {
      enabled: true, retention: 'full', endpoint: mockCollector.url,
      sites: { claude_web: true, chatgpt_web: true },
    });

    const page = await context.newPage();
    await page.goto('https://claude.ai/chat');
    await expect(page.locator('[data-testid="assistant-message"]')).toHaveAttribute('data-complete', '1');
    await expect.poll(() => recordByAction(mockCollector.received, 'agent.response.completed'), { timeout: 10_000 }).toBeTruthy();
    const prompt = recordByAction(mockCollector.received, 'prompt.submitted');
    const response = recordByAction(mockCollector.received, 'agent.response.completed');
    expect(prompt).toBeTruthy();
    expect(response).toBeTruthy();
    const input = flatAttrs(prompt!.attributes);
    const output = flatAttrs(response!.attributes);
    expect(input['gen_ai.request.model']).toBe(REQUESTED);
    expect(output['gen_ai.request.model']).toBe(REQUESTED);
    expect(output['gen_ai.response.model']).toBe('claude-opus-4-8');
    expect(input['beacon.prompt.text']).toBe('What is the capital of France?');
    expect(output['gen_ai.conversation.id']).toBe(CONV);
    expect(output['beacon.response.chars']).toBe(6);
  });
}
