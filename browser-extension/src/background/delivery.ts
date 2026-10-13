// Durable delivery queue. Normalized OTLP envelopes are persisted to
// chrome.storage.local so nothing is lost if the SW is suspended, then POSTed
// to the local beacon collector. Failed sends retry with exponential backoff
// driven by chrome.alarms (which survives suspension, unlike setTimeout).

import { ext } from '../shared/browser.js';
import type { ChatTurn, Settings } from '../shared/types.js';
import { turnToEnvelope } from '../shared/normalize.js';
import type { LogsEnvelope } from '../shared/otlp.js';
import type { BrowserIdentity } from '../shared/browser-identity.js';
import { isAllowedEndpoint } from '../shared/validate.js';

const QUEUE_KEY = 'delivery_queue';
const ALARM = 'beacon_flush';
const MAX_ATTEMPTS = 6;

interface QueueItem {
  id: string;
  endpoint: string;
  envelope: LogsEnvelope;
  attempts: number;
  /** Persisted deadline so new turns cannot consume retries before backoff. */
  nextAttemptAt?: number;
}

async function readQueue(): Promise<QueueItem[]> {
  const got = await ext.storage.local.get(QUEUE_KEY);
  return (got[QUEUE_KEY] as QueueItem[]) ?? [];
}

async function writeQueue(items: QueueItem[]): Promise<void> {
  await ext.storage.local.set({ [QUEUE_KEY]: items });
}

// Serialize every read-modify-write on the queue. The service worker is a single
// JS context, but async interleaving of enqueue/flush would otherwise let two
// read→write cycles clobber each other's writes (dropping queued telemetry).
// Only this SW writes QUEUE_KEY, so an in-memory promise-chain mutex suffices.
let queueLock: Promise<unknown> = Promise.resolve();
function withQueueLock<T>(fn: () => Promise<T>): Promise<T> {
  const result = queueLock.then(fn, fn);
  queueLock = result.then(
    () => undefined,
    () => undefined,
  );
  return result;
}

/** Normalize a turn and enqueue it for delivery, then kick a flush. */
export async function enqueueTurn(
  turn: ChatTurn,
  settings: Settings,
  identity?: BrowserIdentity,
): Promise<void> {
  const envelope = turnToEnvelope(turn, settings.retention, identity);
  const item: QueueItem = {
    id: turn.turnId,
    endpoint: settings.endpoint,
    envelope,
    attempts: 0,
  };
  await withQueueLock(async () => {
    const queue = await readQueue();
    queue.push(item);
    await writeQueue(queue);
  });
  await flush();
}

/** Attempt to POST everything queued; requeue failures with backoff. */
export async function flush(): Promise<void> {
  await withQueueLock(async () => {
    const queue = await readQueue();
    if (queue.length === 0) return;

    const remaining: QueueItem[] = [];
    let anyFailed = false;

    for (const item of queue) {
      // Checked again at send time, not only when settings are saved: an item
      // queued by an older build, or a tampered queue, must not reach anything
      // but the local collector. Such an item is dropped, not retried.
      if (!isAllowedEndpoint(item.endpoint)) continue;
      if (item.nextAttemptAt != null && item.nextAttemptAt > Date.now()) {
        remaining.push(item);
        anyFailed = true;
        continue;
      }
      const ok = await post(item);
      if (!ok) {
        item.attempts += 1;
        if (item.attempts < MAX_ATTEMPTS) {
          item.nextAttemptAt = Date.now() + retryDelay(item.attempts);
          remaining.push(item);
          anyFailed = true;
        } // else: give up, drop it (avoid an unbounded queue)
      }
    }

    await writeQueue(remaining);
    if (anyFailed) scheduleRetry(remaining);
  });
}

async function post(item: QueueItem): Promise<boolean> {
  if (!isAllowedEndpoint(item.endpoint)) return false;
  try {
    const res = await fetch(item.endpoint, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      // A loopback listener must not be able to bounce chat content elsewhere.
      redirect: 'error',
      body: JSON.stringify(item.envelope),
    });
    return res.ok;
  } catch {
    return false;
  }
}

function retryDelay(attempts: number): number {
  return Math.min(250 * 2 ** attempts, 60_000);
}

function scheduleRetry(queue: QueueItem[]): void {
  // Preserve each failed batch's deadline when unrelated new turns arrive.
  // Chrome may clamp alarm cadence, but an enqueue must never accelerate a
  // retry or repeatedly postpone the existing earliest scheduled deadline.
  const when = Math.min(...queue.map((item) =>
    item.nextAttemptAt ?? Date.now() + retryDelay(item.attempts)));
  ext.alarms.create(ALARM, { when });
}

/** Wire the alarm listener (called once from the SW entry). */
export function installFlushAlarm(): void {
  ext.alarms.onAlarm.addListener((alarm) => {
    if (alarm.name === ALARM) void flush();
  });
}
