// Fixture recorder. Opens a HEADED browser with a PERSISTENT profile you log
// into once (the session is saved locally under .auth-profiles/, git-ignored —
// no credentials are ever seen by the tooling). It watches for the chat
// completion request via the Chrome DevTools Protocol, captures the raw SSE
// response body + the request (prompt) + a DOM snapshot, and writes them to
// fixtures/<site>/<name>.{sse,meta.json,page.html}.
//
// Usage:
//   npm run record:fixtures -- --site claude --name simple-turn
//
// Then: log in if prompted, send ONE message, and wait for the full reply.
// The first captured completion is saved and the tool exits.

import { chromium } from '@playwright/test';
import { mkdirSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.join(__dirname, '..');

interface SiteConfig {
  startUrl: string;
  /** matches the completion request URL */
  isCompletion: (url: string) => boolean;
}

const SITES: Record<string, SiteConfig> = {
  claude: {
    startUrl: 'https://claude.ai/',
    isCompletion: (u) => /chat_conversations\/.+\/(completion|retry_completion)/.test(u),
  },
  chatgpt: {
    startUrl: 'https://chatgpt.com/',
    isCompletion: (u) => /\/backend-api\/(conversation|f\/conversation)\b/.test(u),
  },
};

function arg(name: string, fallback?: string): string {
  const i = process.argv.indexOf(`--${name}`);
  const v = i >= 0 ? process.argv[i + 1] : undefined;
  if (v == null && fallback == null) throw new Error(`missing --${name}`);
  return v ?? fallback!;
}

async function main() {
  const site = arg('site', 'claude');
  const name = arg('name', 'live-capture');
  const cfg = SITES[site];
  if (!cfg) throw new Error(`unknown site: ${site} (known: ${Object.keys(SITES).join(', ')})`);

  const profileDir = path.join(ROOT, '.auth-profiles', site);
  mkdirSync(profileDir, { recursive: true });
  const outDir = path.join(ROOT, 'fixtures', site);
  mkdirSync(outDir, { recursive: true });

  console.log(`\n▶ Opening ${cfg.startUrl} with profile ${profileDir}`);
  console.log('  Log in if needed, then send ONE message and wait for the full reply.\n');

  // Use the real installed Chrome and strip the automation fingerprint so
  // bot-detection (Cloudflare et al.) treats it like a normal browser:
  //  - channel:'chrome' → genuine Chrome branding/UA, not "Chrome for Testing"
  //  - drop --enable-automation and the automation extension
  //  - --disable-blink-features=AutomationControlled → navigator.webdriver=false
  const context = await chromium.launchPersistentContext(profileDir, {
    channel: 'chrome',
    headless: false,
    viewport: null,
    ignoreDefaultArgs: ['--enable-automation'],
    args: ['--disable-blink-features=AutomationControlled', '--start-maximized'],
  });
  const page = context.pages()[0] ?? (await context.newPage());
  const client = await context.newCDPSession(page);
  await client.send('Network.enable');

  const pending = new Map<string, { url: string; postData?: string }>();

  client.on('Network.requestWillBeSent', (e: any) => {
    if (cfg.isCompletion(e.request.url)) {
      pending.set(e.requestId, { url: e.request.url, postData: e.request.postData });
    }
  });

  let done = false;
  client.on('Network.loadingFinished', async (e: any) => {
    if (done) return;
    const meta = pending.get(e.requestId);
    if (!meta) return;
    done = true;
    try {
      const { body, base64Encoded } = await client.send('Network.getResponseBody', {
        requestId: e.requestId,
      });
      const sse = base64Encoded ? Buffer.from(body, 'base64').toString('utf8') : body;
      const html = await page.content();

      writeFileSync(path.join(outDir, `${name}.sse`), sse);
      writeFileSync(path.join(outDir, `${name}.page.html`), html);
      writeFileSync(
        path.join(outDir, `${name}.meta.json`),
        JSON.stringify({ url: meta.url, requestBody: safeJson(meta.postData) }, null, 2),
      );

      console.log(`\n✅ Captured ${site}/${name}:`);
      console.log(`   • ${name}.sse        (${sse.length} bytes of SSE)`);
      console.log(`   • ${name}.meta.json  (request URL + prompt body)`);
      console.log(`   • ${name}.page.html  (DOM snapshot)`);
      console.log('\n⚠ Review these for personal content before committing.\n');
    } catch (err) {
      console.error('capture failed:', err);
    } finally {
      await context.close();
      process.exit(0);
    }
  });

  await page.goto(cfg.startUrl);
  // Stay open until a completion is captured (loadingFinished handler exits).
}

function safeJson(s?: string): unknown {
  if (!s) return null;
  try {
    return JSON.parse(s);
  } catch {
    return s;
  }
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
