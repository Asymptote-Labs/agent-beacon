import { test, expect, seedSettings, browserTarget } from './fixtures.js';
import { resourceAttrs } from './helpers/otlp-assertions.js';

// Every emitted turn must say which browser produced it. This runs the real
// extension in whatever browser the fixture launched (BROWSER_CHANNEL /
// BROWSER_EXECUTABLE) and checks the resource attributes against what that
// browser's own service worker reports.

const CONV = '99999999-8888-7777-6666-555555555555';

test('stamps the browser identity on every delivered envelope', async ({
  context,
  serviceWorker,
  replay,
  mockCollector,
}) => {
  mockCollector.reset();
  replay.setCase({
    site: 'claude',
    name: 'simple-turn',
    conversationId: CONV,
    prompt: 'Which browser am I?',
    completionPath: '/api/organizations/org-test/chat_conversations/{conv}/completion',
  });
  await seedSettings(serviceWorker, {
    enabled: true,
    retention: 'metadata', // identity is metadata: it must survive the leanest mode
    endpoint: mockCollector.url,
    sites: { claude_web: true, chatgpt_web: true },
  });

  const page = await context.newPage();
  await page.goto('https://claude.ai/chat');
  await expect(page.locator('[data-testid="assistant-message"]')).toHaveAttribute('data-complete', '1');
  await expect.poll(() => mockCollector.received.length, { timeout: 10_000 }).toBeGreaterThan(0);

  // Ground truth from the same service worker that built the envelope.
  const seen = await serviceWorker.evaluate(() => {
    const uad = (navigator as Navigator & { userAgentData?: { brands: { brand: string; version: string }[] } })
      .userAgentData;
    return { brands: uad?.brands.map((b) => `${b.brand} ${b.version}`) ?? [], ua: navigator.userAgent };
  });

  for (const env of mockCollector.received) {
    const res = resourceAttrs(env);
    const name = res['user_agent.name'];
    expect(typeof name, `user_agent.name missing in ${browserTarget.label}`).toBe('string');
    expect(res['user_agent.original']).toBe(seen.ua);
    if (seen.brands.length > 0) expect(res['browser.brands']).toEqual(seen.brands);
    if (browserTarget.expectedUserAgentNames) {
      expect(browserTarget.expectedUserAgentNames).toContain(name);
    }
  }
  test.info().annotations.push({
    type: 'browser',
    description: `${browserTarget.label}: user_agent.name=${resourceAttrs(mockCollector.received[0])['user_agent.name']}`,
  });
});
