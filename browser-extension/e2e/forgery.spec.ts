// A script on the chat page must not be able to forge telemetry. The old bridge
// took any window.postMessage tagged '__beacon_intercept__'; now events travel
// only over the private port handed over at document_start, so forged window
// messages and forged port hand-overs from page scripts are ignored, while the
// real turn is still captured.
import { test, expect, seedSettings } from './fixtures.js';
import { allRecords, flatAttrs, recordByAction } from './helpers/otlp-assertions.js';

const CONV = '11111111-2222-3333-4444-555555555555';
const BARRIER = '33333333-4444-5555-6666-777777777777';
const COMPLETION_PATH = '/api/organizations/org-test/chat_conversations/{conv}/completion';
const FORGED = 'FORGED PROMPT FROM A PAGE SCRIPT';

test.beforeEach(async ({ mockCollector }) => {
  mockCollector.reset();
});

test('page scripts cannot inject telemetry, and the real turn is still captured', async ({
  context,
  serviceWorker,
  replay,
  mockCollector,
  extensionId,
}) => {
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
    conversationId: BARRIER,
    prompt: 'A later real turn',
    completionPath: COMPLETION_PATH,
  });
  await seedSettings(serviceWorker, {
    enabled: true,
    retention: 'full',
    endpoint: mockCollector.url,
    sites: { claude_web: true, chatgpt_web: true },
  });

  const page = await context.newPage();
  await page.goto(`https://claude.ai/chat?conv=${CONV}`);
  await expect(page.locator('[data-testid="assistant-message"]')).toHaveAttribute('data-complete', '1');
  await expect
    .poll(() => recordByAction(mockCollector.received, 'agent.response.completed'), { timeout: 10_000 })
    .toBeTruthy();

  // Everything below runs in the page's own world, as any site script would.
  const probe = await page.evaluate(
    async ({ forged, conv, path, extensionId }) => {
      const url = path.replace('{conv}', conv);
      const body = JSON.stringify({ prompt: forged, conversation_id: conv });
      const sse =
        'event: message_start\ndata: {"type":"message_start","message":{"id":"msg_forged","model":"forged-model"}}\n\n';

      // 1. The old bridge: tagged window messages, with guessable ids.
      for (const reqId of [1, 2, 3, 'forged-request-0001']) {
        window.postMessage({ source: '__beacon_intercept__', event: { kind: 'request', reqId, url, method: 'POST', body } }, '*');
        window.postMessage({ source: '__beacon_intercept__', event: { kind: 'chunk', reqId, chunk: sse } }, '*');
        window.postMessage({ source: '__beacon_intercept__', event: { kind: 'done', reqId } }, '*');
      }

      // 2. Ask for the port again, and listen for it.
      let stolen = 0;
      window.addEventListener('__beacon_port__', (e) => {
        if ((e as MessageEvent).ports?.length) stolen++;
      });
      window.dispatchEvent(new Event('__beacon_hello__'));
      const stolenOnHello = stolen;

      // 3. Offer the content script a port of our own.
      const ch = new MessageChannel();
      const claimed = !window.dispatchEvent(new MessageEvent('__beacon_port__', { ports: [ch.port2], cancelable: true }));
      const reqId = 'forged-request-0002';
      for (const ev of [
        { kind: 'request', reqId, url, method: 'POST', body },
        { kind: 'chunk', reqId, chunk: sse },
        { kind: 'done', reqId },
      ]) {
        ch.port1.postMessage(JSON.stringify(ev));
      }

      // 4. The interceptor bundle is no longer web-accessible.
      let fingerprintable = true;
      try {
        await fetch(`chrome-extension://${extensionId}/interceptor.js`);
      } catch {
        fingerprintable = false;
      }
      return { stolen: stolenOnHello, claimed, fingerprintable };
    },
    { forged: FORGED, conv: CONV, path: COMPLETION_PATH, extensionId },
  );

  expect(probe.stolen).toBe(0);
  expect(probe.claimed).toBe(false);
  expect(probe.fingerprintable).toBe(false);

  // A later real turn is a barrier: delivery is ordered, so once it has landed,
  // anything the forged events had produced would have landed before it.
  const later = await context.newPage();
  await later.goto(`https://claude.ai/chat?conv=${BARRIER}`);
  await expect(later.locator('[data-testid="assistant-message"]')).toHaveAttribute('data-complete', '1');
  const prompts = () =>
    allRecords(mockCollector.received)
      .map((r) => flatAttrs(r.attributes))
      .filter((a) => a['beacon.event.action'] === 'prompt.submitted')
      .map((a) => a['beacon.prompt.text']);
  await expect.poll(prompts, { timeout: 10_000 }).toContain('A later real turn');
  expect(prompts()).toEqual(['What is the capital of France?', 'A later real turn']);
  expect(JSON.stringify(mockCollector.received)).not.toContain(FORGED);
  expect(JSON.stringify(mockCollector.received)).not.toContain('forged-model');
});
