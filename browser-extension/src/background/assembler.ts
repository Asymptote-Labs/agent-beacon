// Accumulates raw intercept events into completed ChatTurns, one parser per
// (tabId, reqId). No chrome.* — unit-testable. State is intentionally in-memory:
// a stream lasts seconds and the SW is kept awake while one is active; the
// DURABLE state (the delivery queue) lives in storage, not here.

import type { ChatTurn } from '../shared/types.js';
import type { InterceptEvent } from '../shared/types.js';
import { adapterForHost, type SiteAdapter, type TurnParser } from '../adapters/adapter.js';

interface Entry {
  adapter: SiteAdapter;
  parser: TurnParser;
}

export class Assembler {
  private entries = new Map<string, Entry>();

  private key(tabId: number | undefined, reqId: number): string {
    return `${tabId ?? -1}:${reqId}`;
  }

  /** Feed one intercept event. Returns a finalized ChatTurn on 'done'/'error'. */
  handle(host: string, tabId: number | undefined, event: InterceptEvent): ChatTurn | null {
    const k = this.key(tabId, event.reqId);

    if (event.kind === 'request') {
      const adapter = adapterForHost(host);
      if (!adapter || !adapter.matchesRequest(event.url, event.method)) return null;
      const parser = adapter.createParser();
      parser.onRequest(event.url, event.method, event.body);
      this.entries.set(k, { adapter, parser });
      return null;
    }

    const entry = this.entries.get(k);
    if (!entry) return null;

    switch (event.kind) {
      case 'chunk':
        entry.parser.onChunk(event.chunk);
        return null;
      case 'done':
      case 'error': {
        entry.parser.onDone();
        const turn = entry.parser.getTurn();
        this.entries.delete(k);
        return turn;
      }
    }
    return null;
  }

  /** Number of in-flight streams (used to decide SW keepalive). */
  get active(): number {
    return this.entries.size;
  }
}
