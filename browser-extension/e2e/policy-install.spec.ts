// Installs the signed CRX the way a managed fleet does. Chrome policy force-installs it from a
// self-hosted update feed while every other extension is blocked, and pushes managed settings that
// lock retention and the collector endpoint. A replayed chat must then reach the collector without
// content, and a newer version on the feed must replace the installed one.
//
// Linux only, opt-in: Chrome reads machine policy from /etc/opt/chrome/policies/managed there with
// no device-management check (on Windows and macOS an off-store force-install needs a managed
// machine), and writing it needs root or passwordless sudo. CI sets POLICY_E2E=1.

import { test, expect, chromium, type BrowserContext, type Page } from '@playwright/test';
import crypto from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { cpSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import http from 'node:http';
import type { AddressInfo } from 'node:net';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { feedXml, packCrx } from '../tools/pack-crx.mjs';
import { resolveBrowserTarget } from './browser-target.js';
import { startMockCollector, type MockCollector } from './helpers/mock-collector.js';
import { startReplayServer, type ReplayServer } from './helpers/sse-replay-server.js';
import { flatAttrs, recordByAction } from './helpers/otlp-assertions.js';

const enabled = process.platform === 'linux' && process.env.POLICY_E2E === '1';
test.skip(!enabled, 'needs Linux and POLICY_E2E=1: it writes Chrome machine policy under /etc');

const dist = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', 'dist');
// Chrome, Chromium and Chrome for Testing each read their own directory. Writing all three lets
// the spec run in whichever BROWSER_CHANNEL is set; CI runs it in branded Chrome.
const POLICY_DIRS = [
  '/etc/opt/chrome/policies/managed',
  '/etc/chromium/policies/managed',
  '/etc/opt/chrome_for_testing/policies/managed',
];
const POLICY_FILE = 'beacon-e2e.json';
const CONV = '11111111-2222-3333-4444-555555555555';

const asRoot = process.getuid?.() === 0;
function writeAsRoot(file: string, content: string): void {
  if (asRoot) {
    mkdirSync(path.dirname(file), { recursive: true });
    writeFileSync(file, content);
    return;
  }
  execFileSync('sudo', ['mkdir', '-p', path.dirname(file)]);
  execFileSync('sudo', ['tee', file], { input: content, stdio: ['pipe', 'ignore', 'inherit'] });
}
function removeAsRoot(file: string): void {
  if (asRoot) rmSync(file, { force: true });
  else execFileSync('sudo', ['rm', '-f', file]);
}

/** dist/ as the release workflow packages it (no sourcemaps), at `version`, signed with `key`. */
function signedBuild(work: string, version: string, key: string): { crx: Buffer; id: string } {
  const dir = path.join(work, `ext-${version}`);
  cpSync(dist, dir, { recursive: true, filter: (src) => !src.endsWith('.map') });
  const manifestPath = path.join(dir, 'manifest.json');
  const manifest = JSON.parse(readFileSync(manifestPath, 'utf8'));
  manifest.version = version;
  writeFileSync(manifestPath, JSON.stringify(manifest, null, 2));
  const zip = path.join(work, `ext-${version}.zip`);
  execFileSync('zip', ['-q', '-r', '-X', zip, '.'], { cwd: dir });
  return packCrx(readFileSync(zip), key);
}

interface Feed {
  url: string;
  requests: string[];
  offer(version: string): void;
  close(): Promise<void>;
}

/** A self-hosted update server: updates.xml for the offered version, and the CRX files. */
async function startFeed(id: string, crxs: Map<string, Buffer>): Promise<Feed> {
  let offered = '';
  const requests: string[] = [];
  const server = http.createServer((req, res) => {
    const pathname = new URL(req.url ?? '/', 'http://feed').pathname;
    requests.push(pathname);
    if (pathname === '/updates.xml') {
      const crx = crxs.get(offered)!;
      const sha256 = crypto.createHash('sha256').update(crx).digest('hex');
      res.writeHead(200, { 'content-type': 'application/xml' });
      res.end(feedXml({ id, version: offered, codebase: `${base}/ext-${offered}.crx`, sha256 }));
      return;
    }
    const crx = crxs.get(/^\/ext-([\d.]+)\.crx$/.exec(pathname)?.[1] ?? '');
    if (crx) {
      res.writeHead(200, { 'content-type': 'application/x-chrome-extension' });
      res.end(crx);
      return;
    }
    res.writeHead(404).end();
  });
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  const base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
  return {
    url: `${base}/updates.xml`,
    requests,
    offer: (version) => (offered = version),
    close: () => new Promise<void>((resolve) => server.close(() => resolve())),
  };
}

/**
 * The installed extension's popup, once Chrome runs `version` (any version when omitted).
 *
 * Driven through an extension page rather than the service worker: Playwright does not report the
 * worker of an extension that policy installs after launch. The page has the same chrome.runtime
 * and chrome.storage APIs.
 */
async function extensionPage(context: BrowserContext, id: string, version?: string): Promise<Page> {
  const deadline = Date.now() + 120_000;
  let last = 'no attempt';
  while (Date.now() < deadline) {
    // A fresh page per attempt: Chrome closes the extension's tabs when it installs or reloads it.
    const page = await context.newPage();
    try {
      await page.goto(`chrome-extension://${id}/popup.html`);
      const running = await page.evaluate(() => chrome.runtime.getManifest().version);
      if (!version || running === version) return page;
      last = `running ${running}`;
    } catch (err) {
      last = err instanceof Error ? err.message.split('\n')[0] : String(err);
    }
    await page.close().catch(() => undefined);
    await new Promise((resolve) => setTimeout(resolve, 2_000));
  }
  // Chrome's own record of the install: an off-store CRX it refused shows up here with the reason.
  const page = await context.newPage();
  await page.goto('chrome://extensions-internals').catch(() => undefined);
  const internals = (await page.locator('body').textContent().catch(() => '')) ?? '';
  throw new Error(
    `extension ${id}${version ? ` ${version}` : ''} never ran (last attempt: ${last}); extensions-internals: ${internals.slice(0, 4000)}`,
  );
}

let work: string;
let feed: Feed;
let collector: MockCollector;
let replay: ReplayServer;
let context: BrowserContext;
let id: string;
let nextVersion: string;

test.beforeAll(async () => {
  test.setTimeout(180_000);
  work = mkdtempSync(path.join(tmpdir(), 'beacon-policy-e2e-'));
  // A throwaway signing key: the ID it gives stands in for the pinned production one.
  const key = crypto.generateKeyPairSync('rsa', { modulusLength: 2048 }).privateKey.export({ type: 'pkcs8', format: 'pem' }).toString();
  const version = JSON.parse(readFileSync(path.join(dist, 'manifest.json'), 'utf8')).version as string;
  nextVersion = version.replace(/\d+$/, (n) => String(Number(n) + 1));
  const current = signedBuild(work, version, key);
  const next = signedBuild(work, nextVersion, key);
  id = current.id;
  feed = await startFeed(id, new Map([[version, current.crx], [nextVersion, next.crx]]));
  feed.offer(version);
  collector = await startMockCollector();
  replay = await startReplayServer();

  // What an admin pushes: block every extension, force-install ours from the feed, and lock its
  // settings. override_update_url makes Chrome keep polling this feed rather than the manifest's
  // update_url, which is how a fleet that mirrors the files internally stays on its mirror.
  const policy = {
    // Test-only. Chrome's default keeps DevTools, and so Playwright, off extensions installed by
    // policy: their pages detach as soon as the test opens them.
    DeveloperToolsAvailability: 1,
    ExtensionSettings: {
      '*': { installation_mode: 'blocked' },
      [id]: { installation_mode: 'force_installed', update_url: feed.url, override_update_url: true },
    },
    '3rdparty': { extensions: { [id]: { enabled: true, retention: 'metadata', endpoint: collector.url } } },
  };
  for (const dir of POLICY_DIRS) writeAsRoot(path.join(dir, POLICY_FILE), JSON.stringify(policy, null, 2));

  const map = ['claude.ai', 'chatgpt.com', 'chat.openai.com']
    .map((host) => `MAP ${host}:443 127.0.0.1:${replay.port}`)
    .join(', ');
  context = await chromium.launchPersistentContext(path.join(work, 'profile'), {
    ...resolveBrowserTarget(process.env).launch,
    headless: process.env.HEADED !== '1',
    ignoreHTTPSErrors: true,
    // Playwright turns off extensions and the background networking the extension updater runs
    // on; a policy install needs both.
    ignoreDefaultArgs: ['--disable-extensions', '--disable-background-networking', '--disable-component-extensions-with-background-pages'],
    args: [`--host-resolver-rules=${map}`, '--ignore-certificate-errors'],
  });
});

test.afterAll(async () => {
  await context?.close();
  for (const dir of POLICY_DIRS) removeAsRoot(path.join(dir, POLICY_FILE));
  await Promise.all([feed?.close(), collector?.close(), replay?.close()]);
  if (work) rmSync(work, { recursive: true, force: true });
});

test('a managed fleet force-installs the signed build, locks its settings and updates it', async () => {
  test.setTimeout(300_000);

  await test.step('Chrome installs the signed CRX from the self-hosted feed', async () => {
    const page = await extensionPage(context, id).catch((err) => {
      throw new Error(`${err.message}; feed requests: ${JSON.stringify(feed.requests)}`);
    });
    expect(feed.requests).toEqual(expect.arrayContaining(['/updates.xml', expect.stringMatching(/\.crx$/)]));
    await page.close();
  });

  await test.step('policy values reach managed storage and lock the popup', async () => {
    // Chrome delivers an extension's policy once it has registered the extension's schema, a
    // moment after the install, and the extension's open pages may not survive that; each read
    // opens a fresh page.
    const managed = async () => {
      const page = await extensionPage(context, id);
      try {
        return await page.evaluate(() => chrome.storage.managed.get(null));
      } finally {
        await page.close().catch(() => undefined);
      }
    };
    await expect
      .poll(managed, { timeout: 30_000 })
      .toEqual({ enabled: true, retention: 'metadata', endpoint: collector.url });
    const popup = await extensionPage(context, id);
    await expect(popup.locator('[data-testid="managed"]')).toBeVisible();
    await expect(popup.locator('#retention')).toHaveValue('metadata');
    await expect(popup.locator('#retention')).toBeDisabled();
    await expect(popup.locator('#enabled')).toBeDisabled();
    await expect(popup.locator('[data-testid="endpoint"]')).toHaveText(collector.url);
    await popup.close();
  });

  await test.step('a chat reaches the locked endpoint with metadata only', async () => {
    replay.setCase({
      site: 'claude',
      name: 'simple-turn',
      conversationId: CONV,
      prompt: 'What is the capital of France?',
      completionPath: '/api/organizations/org-test/chat_conversations/{conv}/completion',
    });
    const page = await context.newPage();
    await page.goto('https://claude.ai/chat');
    await expect(page.locator('[data-testid="assistant-message"]')).toHaveAttribute('data-complete', '1');
    await expect.poll(() => collector.received.length, { timeout: 15_000 }).toBeGreaterThan(0);
    await page.close();

    // Metadata retention drops the prompt record and every text field; the response record keeps
    // its counts.
    expect(recordByAction(collector.received, 'prompt.submitted')).toBeUndefined();
    const response = recordByAction(collector.received, 'agent.response.completed');
    expect(response, 'a completed-response record should have been delivered').toBeTruthy();
    const attrs = flatAttrs(response!.attributes);
    expect(attrs['beacon.content.retention']).toBe('metadata');
    expect(attrs['gen_ai.output.messages']).toBeUndefined();
    expect(attrs['beacon.response.chars']).toBe('Paris.'.length);
  });

  await test.step('a newer version on the feed replaces the installed one', async () => {
    feed.offer(nextVersion);
    const page = await extensionPage(context, id);
    // Chrome holds a downloaded update until the extension is idle. Reloading as soon as it is
    // ready applies it now instead of whenever this test's pages let the extension go idle.
    const status = await page.evaluate(() => {
      chrome.runtime.onUpdateAvailable.addListener(() => chrome.runtime.reload());
      return chrome.runtime.requestUpdateCheck();
    });
    expect(status).toMatchObject({ status: 'update_available', version: nextVersion });
    const updated = await extensionPage(context, id, nextVersion);
    expect(await updated.evaluate(() => chrome.storage.managed.get(null))).toMatchObject({ retention: 'metadata' });
    expect(feed.requests).toContain(`/ext-${nextVersion}.crx`);
  });
});
