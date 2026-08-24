// SiteAdapter interface + registry. This is the ONLY place site-specific
// knowledge lives. Everything downstream consumes the canonical ChatTurn.

import type { ChatTurn, SiteName } from '../shared/types.js';

/** Stateful, per-request parser that turns raw SSE/request data into a ChatTurn. */
export interface TurnParser {
  /** Called once with the outbound request (prompt lives in the body). */
  onRequest(url: string, method: string, body: string | null): void;
  /** Called for each raw SSE chunk from the response stream. */
  onChunk(chunk: string): void;
  /** Called when the stream ends (cleanly or aborted). */
  onDone(): void;
  /** Best-effort snapshot of the turn so far (null if nothing captured yet). */
  getTurn(): ChatTurn | null;
}

export interface SiteAdapter {
  name: SiteName;
  matchesHost(host: string): boolean;
  /** Whether this request is a chat completion worth capturing. */
  matchesRequest(url: string, method: string): boolean;
  /** @param reqId the interceptor's per-page request id (from the InterceptEvent). */
  createParser(reqId: number): TurnParser;
}

const registry: SiteAdapter[] = [];

export function registerAdapter(a: SiteAdapter): void {
  registry.push(a);
}

export function adapterForHost(host: string): SiteAdapter | undefined {
  return registry.find((a) => a.matchesHost(host));
}

/** Permissive request predicate for the MAIN-world interceptor (which is
 *  site-agnostic). The SW's adapter makes the authoritative decision. */
export function looksLikeChatRequest(url: string): boolean {
  // Over-matches on purpose (the SW's adapter.matchesRequest makes the precise
  // call); must include ChatGPT's /f/conversation variant so it gets teed.
  return /(\/backend-(api|anon)\/(f\/)?conversation|chat_conversations\/.+\/(completion|retry_completion)|\/completion\b)/.test(
    url,
  );
}
