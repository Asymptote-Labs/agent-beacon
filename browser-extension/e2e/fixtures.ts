import { test as base, chromium, type BrowserContext, type Worker } from '@playwright/test';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { startMockCollector, type MockCollector } from './helpers/mock-collector.js';
import { startReplayServer, type ReplayServer } from './helpers/sse-replay-server.js';

const __dirname = path.dirname(fileURLToPath(import.meta.url));

// Load the built extension from dist/ (npm's pretest builds it). Overridable.
const pathToExtension = process.env.EXTENSION_PATH ?? path.join(__dirname, '..', 'dist');
const headless = process.env.HEADED !== '1';

interface Fixtures {
  mockCollector: MockCollector;
  replay: ReplayServer;
  context: BrowserContext;
  serviceWorker: Worker;
  extensionId: string;
}

export const test = base.extend<Fixtures>({
  // Stand-in beacon collector. Fresh per test (received reset in beforeEach via reset()).
  mockCollector: async ({}, use) => {
    const c = await startMockCollector();
    await use(c);
    await c.close();
  },

  // HTTPS server impersonating the chat sites; started before the browser so its
  // port can feed --host-resolver-rules.
  replay: async ({}, use) => {
    const r = await startReplayServer();
    await use(r);
    await r.close();
  },

  context: async ({ replay }, use) => {
    // Map the real chat hostnames (:443) to the local replay server so the
    // extension's content-script matches fire on the genuine origins.
    const map = [
      `MAP claude.ai:443 127.0.0.1:${replay.port}`,
      `MAP chatgpt.com:443 127.0.0.1:${replay.port}`,
      `MAP chat.openai.com:443 127.0.0.1:${replay.port}`,
    ].join(', ');

    const context = await chromium.launchPersistentContext('', {
      channel: 'chromium',
      headless,
      ignoreHTTPSErrors: true,
      args: [
        `--disable-extensions-except=${pathToExtension}`,
        `--load-extension=${pathToExtension}`,
        `--host-resolver-rules=${map}`,
        '--ignore-certificate-errors',
      ],
    });
    await use(context);
    await context.close();
  },

  serviceWorker: async ({ context }, use) => {
    let [worker] = context.serviceWorkers();
    if (!worker) worker = await context.waitForEvent('serviceworker');
    await use(worker);
  },

  extensionId: async ({ serviceWorker }, use) => {
    await use(new URL(serviceWorker.url()).host);
  },
});

export const expect = test.expect;

/** Seed the extension's settings through the service worker. Waits for the
 *  SW's onInstalled default-write to land first so it can't clobber our seed. */
export async function seedSettings(
  worker: Worker,
  patch: Record<string, unknown>,
): Promise<void> {
  await worker.evaluate(async (p) => {
    for (let i = 0; i < 100; i++) {
      const cur = (await chrome.storage.local.get('settings')).settings;
      if (cur) {
        await chrome.storage.local.set({ settings: { ...cur, ...p } });
        return;
      }
      await new Promise((r) => setTimeout(r, 20));
    }
    await chrome.storage.local.set({ settings: p });
  }, patch);
}
