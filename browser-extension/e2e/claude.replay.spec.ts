import { test, expect, seedSettings, browserTarget } from './fixtures.js';
import { allRecords, flatAttrs, recordByAction, resourceAttrs } from './helpers/otlp-assertions.js';

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
  expect(JSON.parse(String(promptAttrs['gen_ai.input.messages']))).toEqual([
    { role: 'user', parts: [{ type: 'text', content: 'What is the capital of France?' }] },
  ]);

  // The response lands on the agent.response.completed record.
  const rec = recordByAction(mockCollector.received, 'agent.response.completed');
  expect(rec, 'a completed-response record should have been delivered').toBeTruthy();
  const attrs = flatAttrs(rec!.attributes);
  expect(attrs['gen_ai.conversation.id']).toBe(CONV);
  expect(attrs['gen_ai.response.model']).toBe('claude-opus-4-8');
  expect(JSON.parse(String(attrs['gen_ai.output.messages']))).toEqual([
    { role: 'assistant', parts: [{ type: 'text', content: 'Paris.' }] },
  ]);

  // Every envelope says which browser produced it. This runs in whatever browser
  // the fixture launched (BROWSER_CHANNEL / BROWSER_EXECUTABLE); the ground truth
  // is what the same service worker that built the envelope reports.
  const seen = await serviceWorker.evaluate(() => {
    const uad = (navigator as Navigator & { userAgentData?: { brands: { brand: string; version: string }[] } })
      .userAgentData;
    return { brands: uad?.brands.map((b) => `${b.brand} ${b.version}`) ?? [], ua: navigator.userAgent };
  });
  for (const env of mockCollector.received) {
    const identity = resourceAttrs(env);
    const name = identity['user_agent.name'];
    expect(typeof name, `user_agent.name missing in ${browserTarget.label}`).toBe('string');
    expect(identity['user_agent.original']).toBe(seen.ua);
    if (seen.brands.length > 0) expect(identity['browser.brands']).toEqual(seen.brands);
    if (browserTarget.expectedUserAgentNames) {
      expect(browserTarget.expectedUserAgentNames).toContain(name);
    }
  }
  test.info().annotations.push({
    type: 'browser',
    description: `${browserTarget.label}: user_agent.name=${res['user_agent.name']}`,
  });
});

test('captures the prompt when the page uses fetch(new Request(...))', async ({
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
    requestStyle: 'request', // body lives on the Request object, not init.body
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
    .poll(() => recordByAction(mockCollector.received, 'prompt.submitted'), { timeout: 10_000 })
    .toBeTruthy();
  const p = flatAttrs(recordByAction(mockCollector.received, 'prompt.submitted')!.attributes);
  expect(p['beacon.prompt.text']).toBe('What is the capital of France?');
});

// claude.ai as of October 2026: the completion body is a gzip-compressed
// Uint8Array, and web_search runs server side with its results streamed back
// as tool_result blocks.
test('captures a gzip-compressed prompt and server-side tool results', async ({
  context,
  serviceWorker,
  replay,
  mockCollector,
}) => {
  replay.setCase({
    site: 'claude',
    name: 'web-search',
    conversationId: CONV,
    prompt: 'Search the web for the latest NASA news.',
    completionPath: COMPLETION_PATH,
    requestStyle: 'gzip',
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
    .poll(() => allRecords(mockCollector.received).filter((r) => flatAttrs(r.attributes)['beacon.event.action'] === 'tool.invoked').length, { timeout: 10_000 })
    .toBe(2);
  const p = flatAttrs(recordByAction(mockCollector.received, 'prompt.submitted')!.attributes);
  expect(p['beacon.prompt.text']).toBe('Search the web for the latest NASA news.');

  const tools = allRecords(mockCollector.received)
    .map((r) => flatAttrs(r.attributes))
    .filter((a) => a['beacon.event.action'] === 'tool.invoked');
  expect(tools.map((a) => JSON.parse(String(a['gen_ai.tool.call.arguments'])).query)).toEqual([
    'NASA news today',
    'NASA October 2026',
  ]);
  const results = tools.map((a) => JSON.parse(String(a['gen_ai.tool.call.result'])) as { url: string }[]);
  expect(results.map((r) => r.map((x) => x.url))).toEqual([
    ['https://news.example.com/nasa-today', 'https://space.example.org/updates'],
    ['https://space.example.org/artemis'],
  ]);

  const resp = flatAttrs(recordByAction(mockCollector.received, 'agent.response.completed')!.attributes);
  expect(JSON.parse(String(resp['gen_ai.output.messages']))).toEqual([
    {
      role: 'assistant',
      parts: [{ type: 'text', content: 'One recent NASA headline: a new country signed the Artemis Accords.' }],
    },
  ]);
});

test('respects the disabled toggle (no delivery)', async ({
  context,
  serviceWorker,
  replay,
  mockCollector,
}) => {
  const LATER = '22222222-3333-4444-5555-666666666666';
  replay.setCase({
    site: 'claude',
    name: 'simple-turn',
    conversationId: CONV,
    prompt: 'What is the capital of France?',
    completionPath: COMPLETION_PATH,
  });
  replay.setCase({
    site: 'claude',
    name: 'simple-turn',
    conversationId: LATER,
    prompt: 'Captured once re-enabled',
    completionPath: COMPLETION_PATH,
  });
  await seedSettings(serviceWorker, {
    enabled: false,
    retention: 'full',
    endpoint: mockCollector.url,
    sites: { claude_web: true, chatgpt_web: true },
  });

  const page = await context.newPage();
  await page.goto(`https://claude.ai/chat?conv=${CONV}`);
  await expect(page.locator('[data-testid="assistant-message"]')).toHaveAttribute(
    'data-complete',
    '1',
  );

  // Re-enable and capture a later turn. Delivery is ordered, so once the later
  // turn has landed, anything the disabled turn wrongly produced would have too.
  await seedSettings(serviceWorker, { enabled: true });
  const later = await context.newPage();
  await later.goto(`https://claude.ai/chat?conv=${LATER}`);
  await expect(later.locator('[data-testid="assistant-message"]')).toHaveAttribute(
    'data-complete',
    '1',
  );
  await expect
    .poll(() => recordByAction(mockCollector.received, 'agent.response.completed'), { timeout: 10_000 })
    .toBeTruthy();

  const sessions = allRecords(mockCollector.received).map((r) => flatAttrs(r.attributes)['beacon.session.id']);
  expect(new Set(sessions)).toEqual(new Set([LATER]));
});
