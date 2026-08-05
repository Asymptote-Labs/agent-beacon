// HTTPS server that impersonates claude.ai / chatgpt.com for tests. It serves a
// minimal chat page (whose DOM mirrors the real message list) that fires the
// same completion fetch the real site would, and it streams a recorded .sse
// fixture back with realistic chunking. Combined with --host-resolver-rules,
// this lets the real extension content scripts (matched on the real hostnames)
// run against deterministic recorded data — no login, no cost.
//
// Multiple cases can be registered (keyed by conversationId) so tests can drive
// two concurrent tabs; a page selects its case via ?conv=<id> (defaulting to the
// first-registered case for single-case tests).

import https from 'node:https';
import type { AddressInfo } from 'node:net';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { getTestCert } from './cert.js';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const fixturesDir = path.join(__dirname, '..', '..', 'fixtures');

export interface ReplayCase {
  /** e.g. 'claude' */
  site: string;
  /** e.g. 'simple-turn' */
  name: string;
  /** conversation id embedded in the completion URL */
  conversationId: string;
  /** prompt the page will submit */
  prompt: string;
  /** completion path (must match the adapter's request matcher) */
  completionPath: string;
  /** optional CSP header to prove injection survives it */
  csp?: string;
  /** how the page issues the completion fetch: 'init' (default) or a Request object */
  requestStyle?: 'init' | 'request';
}

export interface ReplayServer {
  port: number;
  /** Register (or replace) a case, keyed by its conversationId. */
  setCase(c: ReplayCase): void;
  close(): Promise<void>;
}

export async function startReplayServer(): Promise<ReplayServer> {
  const { key, cert } = getTestCert();
  const cases = new Map<string, ReplayCase>();
  let defaultConv: string | undefined;

  const server = https.createServer({ key, cert }, (req, res) => {
    const url = new URL(req.url ?? '/', 'https://claude.ai');

    // The completion endpoint: match the POST path against a registered case's
    // completion path (site-agnostic — Claude embeds the conv id in the path;
    // ChatGPT posts to a fixed /backend-api/f/conversation).
    if (req.method === 'POST') {
      for (const c of cases.values()) {
        if (url.pathname === c.completionPath.replace('{conv}', c.conversationId)) {
          streamFixture(res, c);
          return;
        }
      }
    }

    // Anything else → the chat page for the requested (or default) conversation.
    const convId = url.searchParams.get('conv') ?? defaultConv;
    const c = convId ? cases.get(convId) : undefined;
    if (!c) {
      res.writeHead(503).end('no case set');
      return;
    }
    const headers: Record<string, string> = { 'content-type': 'text/html; charset=utf-8' };
    if (c.csp) headers['content-security-policy'] = c.csp;
    res.writeHead(200, headers).end(pageHtml(c));
  });

  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  const { port } = server.address() as AddressInfo;

  return {
    port,
    setCase: (c) => {
      cases.set(c.conversationId, c);
      defaultConv ??= c.conversationId;
    },
    close: () => new Promise<void>((resolve) => server.close(() => resolve())),
  };
}

function readFixture(site: string, name: string): string {
  return readFileSync(path.join(fixturesDir, site, `${name}.sse`), 'utf8');
}

function streamFixture(res: import('node:http').ServerResponse, c: ReplayCase): void {
  res.writeHead(200, {
    'content-type': 'text/event-stream',
    'cache-control': 'no-cache',
    connection: 'keep-alive',
  });
  const body = readFixture(c.site, c.name);
  // Chunk on a coarse boundary that does NOT respect SSE frame boundaries, to
  // exercise the parser's buffering; write with small delays to simulate streaming.
  const pieces = chunkify(body);
  let i = 0;
  const tick = () => {
    if (i >= pieces.length) {
      res.end();
      return;
    }
    res.write(pieces[i++]);
    setTimeout(tick, 5);
  };
  tick();
}

/** Split into byte-ish pieces that don't respect SSE frame boundaries. */
function chunkify(body: string): string[] {
  const size = 24;
  const out: string[] = [];
  for (let i = 0; i < body.length; i += size) out.push(body.slice(i, i + size));
  return out;
}

/** Minimal page that mirrors a chat message list and fires the completion fetch. */
function pageHtml(c: ReplayCase): string {
  const completionUrl = c.completionPath.replace('{conv}', c.conversationId);
  // Per-site request body: ChatGPT posts messages[].content.parts (conv id comes
  // back in the stream); Claude posts {prompt, conversation_id}.
  const bodyStr =
    c.site === 'chatgpt'
      ? JSON.stringify({
          action: 'next',
          conversation_id: null,
          model: 'gpt-5-6-thinking',
          messages: [
            { author: { role: 'user' }, content: { content_type: 'text', parts: [c.prompt] } },
          ],
        })
      : JSON.stringify({ prompt: c.prompt, conversation_id: c.conversationId });
  return `<!doctype html>
<html>
  <head><meta charset="utf-8" /><title>${c.site} (replay)</title></head>
  <body>
    <main>
      <div class="message-list" data-testid="message-list">
        <div class="user-message">${escapeHtml(c.prompt)}</div>
        <div class="assistant-message" data-testid="assistant-message"></div>
      </div>
    </main>
    <script>
      (async () => {
        const url = ${JSON.stringify(completionUrl)};
        const init = {
          method: 'POST',
          headers: { 'content-type': 'application/json' },
          body: ${JSON.stringify(bodyStr)},
        };
        const res = await ${
          c.requestStyle === 'request' ? 'fetch(new Request(url, init))' : 'fetch(url, init)'
        };
        // Consume the stream like the real site does.
        const reader = res.body.getReader();
        const dec = new TextDecoder();
        const el = document.querySelector('[data-testid="assistant-message"]');
        for (;;) {
          const { done, value } = await reader.read();
          if (done) break;
          el.textContent += dec.decode(value, { stream: true });
        }
        el.setAttribute('data-complete', '1');
      })();
    </script>
  </body>
</html>`;
}

function escapeHtml(s: string): string {
  return s.replace(/[&<>"]/g, (ch) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[ch]!));
}
