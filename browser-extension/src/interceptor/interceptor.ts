// MAIN-world interceptor. Runs in the page's own JS context (injected as a file
// via a world:"MAIN" content script, which satisfies ChatGPT's CSP since it is
// not inline). It monkeypatches window.fetch, clones matched streaming
// responses so the page is completely unaffected, and sends raw SSE chunks to
// the ISOLATED content script over a private MessagePort handed over at
// document_start (src/shared/channel.ts), never over window.postMessage.
//
// This layer is deliberately site-agnostic: it does NOT understand SSE
// semantics. It only decides "is this a chat request worth teeing?" and relays
// bytes. All parsing happens in the service worker via the site adapter.
//
// Everything the send path calls is captured here, at document_start, before
// any page script runs, so a page script that later replaces JSON.stringify or
// MessagePort.prototype.postMessage cannot read or rewrite what is sent.

import type { InterceptEvent } from '../shared/types.js';
import { looksLikeChatRequest } from '../adapters/adapter.js';
import { offerChannel } from '../shared/channel.js';
import { captureRequestBody } from './request-body.js';

declare global {
  interface Window {
    __beaconInterceptorInstalled?: boolean;
  }
}

(function install() {
  if (window.__beaconInterceptorInstalled) return;
  window.__beaconInterceptorInstalled = true;

  const apply = Reflect.apply;
  const stringify = JSON.stringify;
  const portPostMessage = MessagePort.prototype.postMessage;
  const randomId = makeRandomId();
  const originalFetch = window.fetch.bind(window);
  const port = offerChannel(window);

  function post(event: InterceptEvent): void {
    // A JSON string, so the receiving world parses it itself instead of
    // reading an object created in this one.
    apply(portPostMessage, port, [stringify(event)]);
  }

  window.fetch = async function beaconFetch(
    input: RequestInfo | URL,
    init?: RequestInit,
  ): Promise<Response> {
    const url =
      typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
    const method = (init?.method ?? (input instanceof Request ? input.method : 'GET')).toUpperCase();

    const matched = looksLikeChatRequest(url);

    // Snapshot the request body before fetch consumes it, and decode it while
    // the request is in flight. claude.ai sends it gzip-compressed, so it is
    // not always a string (see request-body.ts).
    const body = matched ? captureRequestBody(input, init) : null;

    const response = await originalFetch(input as RequestInfo, init);

    if (body && response.body) {
      const reqId = randomId();
      // Clone now, before the page starts reading, so its own reader is
      // untouched; the clone buffers until the body is decoded.
      const clone = response.clone();
      void body
        .then((text) => {
          post({ kind: 'request', reqId, url, method, body: text });
          return teeStream(clone, reqId, post).catch((err) =>
            post({ kind: 'error', reqId, message: String(err).slice(0, 1024) }),
          );
        })
        .catch(() => {
          /* the channel is gone; nothing to report to */
        });
    }

    return response;
  } as typeof window.fetch;
})();

/** Unguessable request ids, so nothing can address another request's stream. */
function makeRandomId(): () => string {
  const c = globalThis.crypto;
  if (typeof c?.randomUUID === 'function') return c.randomUUID.bind(c);
  const getRandomValues = c.getRandomValues.bind(c);
  return () => {
    const b = getRandomValues(new Uint8Array(16));
    return Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('');
  };
}

async function teeStream(
  response: Response,
  reqId: string,
  post: (e: InterceptEvent) => void,
): Promise<void> {
  const reader = response.body!.getReader();
  const decoder = new TextDecoder();
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      const chunk = decoder.decode(value, { stream: true });
      if (chunk) post({ kind: 'chunk', reqId, chunk });
    }
    const tail = decoder.decode();
    if (tail) post({ kind: 'chunk', reqId, chunk: tail });
    post({ kind: 'done', reqId });
  } finally {
    reader.releaseLock();
  }
}
