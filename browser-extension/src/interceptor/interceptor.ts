// MAIN-world interceptor. Runs in the page's own JS context (injected as a file
// via a world:"MAIN" content script, which satisfies ChatGPT's CSP since it is
// not inline). It monkeypatches window.fetch, clones matched streaming
// responses so the page is completely unaffected, and posts raw SSE chunks to
// the ISOLATED content script over window.postMessage.
//
// This layer is deliberately site-agnostic: it does NOT understand SSE
// semantics. It only decides "is this a chat request worth teeing?" and relays
// bytes. All parsing happens in the service worker via the site adapter.

import { BEACON_MSG, type InterceptEvent } from '../shared/types.js';
import { looksLikeChatRequest } from '../adapters/adapter.js';

declare global {
  interface Window {
    __beaconInterceptorInstalled?: boolean;
  }
}

(function install() {
  if (window.__beaconInterceptorInstalled) return;
  window.__beaconInterceptorInstalled = true;

  let reqSeq = 0;
  const originalFetch = window.fetch.bind(window);

  function post(event: InterceptEvent): void {
    window.postMessage({ source: BEACON_MSG, event }, window.location.origin);
  }

  window.fetch = async function beaconFetch(
    input: RequestInfo | URL,
    init?: RequestInit,
  ): Promise<Response> {
    const url =
      typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
    const method = (init?.method ?? (input instanceof Request ? input.method : 'GET')).toUpperCase();

    const matched = looksLikeChatRequest(url);

    // Capture the request body BEFORE fetch consumes it. Prefer a string
    // init.body; otherwise read a clone of the Request (fetch(new Request(...))),
    // whose body would otherwise never be seen and the prompt would be lost.
    let body: string | null = null;
    if (matched) {
      if (typeof init?.body === 'string') {
        body = init.body;
      } else if (input instanceof Request) {
        try {
          body = await input.clone().text();
        } catch {
          body = null;
        }
      }
    }

    const response = await originalFetch(input as RequestInfo, init);

    if (matched && response.body) {
      const reqId = ++reqSeq;
      post({ kind: 'request', reqId, url, method, body });

      // Tee the stream via a clone so the page's own reader is untouched.
      const clone = response.clone();
      teeStream(clone, reqId, post).catch((err) =>
        post({ kind: 'error', reqId, message: String(err) }),
      );
    }

    return response;
  } as typeof window.fetch;
})();

async function teeStream(
  response: Response,
  reqId: number,
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
