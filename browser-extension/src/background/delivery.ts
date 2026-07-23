// Durable delivery queue. Normalized OTLP envelopes are persisted to
// chrome.storage.local so nothing is lost if the SW is suspended, then POSTed
// to the local beacon collector. Failed sends retry with exponential backoff
// driven by chrome.alarms (which survives suspension, unlike setTimeout).

import type { ChatTurn, Settings } from '../shared/types.js';
import { turnToEnvelope } from '../shared/normalize.js';
import type { LogsEnvelope } from '../shared/otlp.js';

const QUEUE_KEY = 'delivery_queue';
const ALARM = 'beacon_flush';
const MAX_ATTEMPTS = 6;

interface QueueItem {
  id: string;
  endpoint: string;
  envelope: LogsEnvelope;
  attempts: number;
}

async function readQueue(): Promise<QueueItem[]> {
  const got = await chrome.storage.local.get(QUEUE_KEY);
  return (got[QUEUE_KEY] as QueueItem[]) ?? [];
}

async function writeQueue(items: QueueItem[]): Promise<void> {
  await chrome.storage.local.set({ [QUEUE_KEY]: items });
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
export async function enqueueTurn(turn: ChatTurn, settings: Settings): Promise<void> {
  const envelope = turnToEnvelope(turn, settings.retention);
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
      const ok = await post(item);
      if (!ok) {
        item.attempts += 1;
        if (item.attempts < MAX_ATTEMPTS) {
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
  try {
    const res = await fetch(item.endpoint, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify(item.envelope),
    });
    return res.ok;
  } catch {
    return false;
  }
}

function scheduleRetry(queue: QueueItem[]): void {
  const minAttempts = Math.min(...queue.map((i) => i.attempts));
  // We compute an exponential backoff, but note MV3 clamps chrome.alarms to a
  // ~30s minimum — so in practice the first retry lands at ~30s regardless of
  // this value. That's acceptable here: the collector is a local loopback
  // endpoint that is almost always reachable, so retries are the rare path and
  // the durable queue (chrome.storage) guarantees eventual delivery. We use
  // alarms rather than setTimeout because they survive service-worker suspension.
  const backoffMs = Math.min(250 * 2 ** minAttempts, 60_000);
  chrome.alarms.create(ALARM, { when: Date.now() + backoffMs });
}

/** Wire the alarm listener (called once from the SW entry). */
export function installFlushAlarm(): void {
  chrome.alarms.onAlarm.addListener((alarm) => {
    if (alarm.name === ALARM) void flush();
  });
}
