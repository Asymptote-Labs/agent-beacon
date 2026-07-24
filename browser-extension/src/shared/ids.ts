// Correlation id helpers. Kept deterministic and dependency-free so both the
// service worker and unit tests can use them without a DOM/crypto polyfill.

/**
 * Derive a stable turn id from a conversation id and the request sequence.
 * Deterministic so retried/duplicate deliveries can be de-duped downstream.
 */
export function turnId(sessionId: string, key: string | number): string {
  return `${sessionId}:${key}`;
}

/** A per-record idempotency key for dedupe across delivery retries. */
export function recordId(turnId: string, action: string, index = 0): string {
  return `${turnId}#${action}#${index}`;
}
