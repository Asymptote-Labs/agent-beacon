// Durable delivery queue. Normalized OTLP envelopes are persisted to
// chrome.storage.local so nothing is lost if the SW is suspended, then POSTed
// to the local beacon collector. Failed sends retry with exponential backoff
// driven by chrome.alarms (which survives suspension, unlike setTimeout).
//
// Retention policy: a queued batch is kept until it is delivered, until it is
// older than MAX_QUEUE_AGE_MS, or until newer batches push it past the queue's
// size caps (MAX_QUEUE_ITEMS, MAX_QUEUE_BYTES), which drop the oldest first.
// A failed send never drops a batch on its own, so a collector that is down
// for minutes (restarting during an install or upgrade, not yet started after
// boot, crash-looping) loses nothing it comes back from within the age limit.
// A batch the collector rejects (a non-retryable status) is dropped at once.
//
// Policy: a batch goes out under the locks in force when it is sent, not when it
// was queued, so a lock an admin pushes later also covers what is still queued.
// A copy installed by policy also holds delivery for a short while after an
// install or update, until Chrome delivers its managed settings, because
// turns captured in that window were shaped before any lock was known.

import { ext } from '../shared/browser.js';
import type { ChatTurn, LockedSetting, Retention, Settings, SiteName } from '../shared/types.js';
import { getSettingsState, type SettingsState } from './settings.js';
import { turnToEnvelope } from '../shared/normalize.js';
import type { LogsEnvelope } from '../shared/otlp.js';
import type { BrowserIdentity } from '../shared/browser-identity.js';
import { isAllowedEndpoint } from '../shared/validate.js';
import { serial } from '../shared/serial.js';

const QUEUE_KEY = 'delivery_queue';
const ALARM = 'beacon_flush';
const POST_TIMEOUT_MS = 10_000;

/** A batch older than this is dropped undelivered. */
export const MAX_QUEUE_AGE_MS = 24 * 60 * 60 * 1000;
/** Most batches the queue holds; the oldest are dropped past it. */
export const MAX_QUEUE_ITEMS = 2_000;
/**
 * Most envelope bytes (UTF-8 JSON) the queue holds; the oldest batches are
 * dropped past it. Chrome's storage.local quota is 10 MiB for the whole
 * extension without `unlimitedStorage`, so this leaves room for settings and for
 * the queue's own bookkeeping fields.
 */
export const MAX_QUEUE_BYTES = 6 * 1024 * 1024;
/** Longest wait between retries, and so the longest delay after the collector recovers. */
export const MAX_RETRY_DELAY_MS = 60_000;
/**
 * How long a copy installed by policy holds delivery after an install or update while Chrome has
 * delivered none of its managed settings. Chrome delivers them a moment after the install; an
 * admin who pushes none gets delivery once this passes.
 */
export const POLICY_GRACE_MS = 30_000;
/** How long the queue waits before trying again when Chrome cannot read the policy at all. */
const UNREADABLE_POLICY_RETRY_MS = 60_000;
const INSTALLED_KEY = 'installed_at';

interface QueueItem {
  id: string;
  endpoint: string;
  envelope: LogsEnvelope;
  /** Failed sends so far. Sets the backoff; never a reason to drop the batch. */
  attempts: number;
  /** Persisted deadline so new turns cannot consume retries before backoff. */
  nextAttemptAt?: number;
  /** When the batch was queued (epoch ms); the age limit counts from here. */
  queuedAt?: number;
  /** UTF-8 size of the serialized envelope, counted against MAX_QUEUE_BYTES. */
  bytes?: number;
  /** The site and retention the envelope was built for, checked against policy at send time. */
  site?: SiteName;
  retention?: Retention;
}

async function readQueue(): Promise<QueueItem[]> {
  const got = await ext.storage.local.get(QUEUE_KEY);
  return (got[QUEUE_KEY] as QueueItem[]) ?? [];
}

async function writeQueue(items: QueueItem[]): Promise<void> {
  await ext.storage.local.set({ [QUEUE_KEY]: items });
}

// Every read-modify-write on the queue goes through here, so enqueue and flush never clobber
// each other's writes (dropping queued telemetry).
const withQueueLock = serial();

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
    queuedAt: Date.now(),
    site: turn.site,
    retention: settings.retention,
  };
  await withQueueLock(async () => {
    const queue = await readQueue();
    queue.push(item);
    await writeQueue(pruneQueue(queue, Date.now()));
  });
  await flush();
}

/**
 * POST every queued batch that is due, oldest first; requeue failures with backoff.
 *
 * Once a batch for an endpoint fails or is still waiting out its backoff, the
 * endpoint's later batches wait behind it in this pass. A recovered collector
 * therefore receives batches in the order they were captured, and a collector
 * that is down gets one probe per pass instead of one POST per queued batch.
 */
export async function flush(): Promise<void> {
  await withQueueLock(async () => {
    const stored = await readQueue();
    if (stored.length === 0) return;
    const queue = pruneQueue(stored, Date.now());

    // Nothing goes out while the locks in force are unknown.
    const policy = await getSettingsState();
    const hold = policy.managedError != null ? UNREADABLE_POLICY_RETRY_MS : await policyPendingMs(policy);
    if (hold > 0) {
      await writeQueue(queue);
      ext.alarms.create(ALARM, { when: Date.now() + hold });
      return;
    }

    const remaining: QueueItem[] = [];
    const waiting = new Set<string>();

    for (const queued of queue) {
      const item = underPolicy(queued, policy);
      if (!item) continue;
      // Checked again at send time, not only when settings are saved: an item
      // queued by an older build, or a tampered queue, must not reach anything
      // but the local collector. Such an item is dropped, not retried.
      if (!isAllowedEndpoint(item.endpoint)) continue;
      if (waiting.has(item.endpoint) || (item.nextAttemptAt ?? 0) > Date.now()) {
        remaining.push(item);
        waiting.add(item.endpoint);
        continue;
      }
      const outcome = await post(item);
      // A batch the collector rejects outright would fail the same way forever
      // and hold every later batch behind it, so it is dropped, not retried.
      if (outcome !== 'retry') continue;
      item.attempts += 1;
      item.nextAttemptAt = Date.now() + retryDelay(item.attempts);
      remaining.push(item);
      waiting.add(item.endpoint);
    }

    await writeQueue(remaining);
    scheduleRetry(remaining);
  });
}

const STRICTNESS: Record<Retention, number> = { full: 0, redacted: 1, metadata: 2 };

/**
 * A queued batch under the locks in force: the batch, sent to a locked endpoint if one is set, or
 * undefined when a lock forbids sending it at all. A locked retention stricter than the one the
 * batch was built with drops it, because its content cannot be taken back out.
 */
export function underPolicy(item: QueueItem, policy: SettingsState): QueueItem | undefined {
  const { settings, locked } = policy;
  const isLocked = (key: LockedSetting) => locked.includes(key);
  if (isLocked('enabled') && !settings.enabled) return undefined;
  if (item.site && isLocked(`sites.${item.site}`) && settings.sites[item.site] === false) return undefined;
  if (isLocked('retention') && STRICTNESS[item.retention ?? 'full'] < STRICTNESS[settings.retention]) return undefined;
  if (isLocked('endpoint')) item.endpoint = settings.endpoint;
  return item;
}

/** Record an install or update, which starts a policy-installed copy's grace period. */
export async function markInstalled(): Promise<void> {
  await ext.storage.local.set({ [INSTALLED_KEY]: Date.now() });
}

/**
 * Milliseconds to keep holding the queue for a policy that has not arrived: only a copy installed
 * by policy, only within POLICY_GRACE_MS of its install or update, and only while no managed value
 * is in force. Zero otherwise.
 */
async function policyPendingMs(policy: SettingsState): Promise<number> {
  if (policy.locked.length > 0) return 0;
  const installedAt = (await ext.storage.local.get(INSTALLED_KEY))[INSTALLED_KEY];
  if (typeof installedAt !== 'number') return 0;
  const left = installedAt + POLICY_GRACE_MS - Date.now();
  if (left <= 0) return 0;
  const self = await ext.management?.getSelf?.().catch(() => undefined);
  return self?.installType === 'admin' ? left : 0;
}

/**
 * Drop batches past the age limit, then the oldest batches until the queue fits
 * its caps. The newest batch is always kept. A batch from an older build has no
 * `queuedAt`; it is stamped now, so it gets a full age window rather than none.
 */
function pruneQueue(queue: QueueItem[], now: number): QueueItem[] {
  const fresh = queue.filter((item) => {
    item.queuedAt ??= now;
    return now - item.queuedAt < MAX_QUEUE_AGE_MS;
  });
  let bytes = fresh.reduce((sum, item) => sum + itemBytes(item), 0);
  let drop = 0;
  while (
    fresh.length - drop > 1 &&
    (fresh.length - drop > MAX_QUEUE_ITEMS || bytes > MAX_QUEUE_BYTES)
  ) {
    bytes -= itemBytes(fresh[drop]);
    drop += 1;
  }
  return fresh.slice(drop);
}

const utf8 = new TextEncoder();

function itemBytes(item: QueueItem): number {
  item.bytes ??= utf8.encode(JSON.stringify(item.envelope)).length;
  return item.bytes;
}

/**
 * Statuses the OTLP/HTTP spec marks retryable. Any other non-2xx response
 * rejects the batch itself, so sending it again cannot succeed.
 */
const RETRYABLE_STATUSES = new Set([429, 502, 503, 504]);

type PostOutcome = 'sent' | 'retry' | 'rejected';

async function post(item: QueueItem): Promise<PostOutcome> {
  if (!isAllowedEndpoint(item.endpoint)) return 'rejected';
  try {
    const res = await fetch(item.endpoint, {
      method: 'POST',
      // Release the serialized queue even if a local listener never replies.
      // A timeout follows the same durable retry path as any failed POST.
      signal: AbortSignal.timeout(POST_TIMEOUT_MS),
      headers: { 'content-type': 'application/json' },
      // A loopback listener must not be able to bounce chat content elsewhere.
      redirect: 'error',
      body: JSON.stringify(item.envelope),
    });
    if (res.ok) return 'sent';
    return RETRYABLE_STATUSES.has(res.status) ? 'retry' : 'rejected';
  } catch {
    // Connection refused, timed out or redirected: the collector may come back.
    return 'retry';
  }
}

/** Backoff after the given number of failed sends: 0.5s, 1s, 2s, ... capped at a minute. */
function retryDelay(attempts: number): number {
  return Math.min(250 * 2 ** attempts, MAX_RETRY_DELAY_MS);
}

function scheduleRetry(queue: QueueItem[]): void {
  // Wake at the earliest stored deadline. A batch waiting behind another has
  // none of its own, so an unrelated new turn neither accelerates nor postpones
  // the retry. Chrome clamps the alarm to about 30 seconds.
  let when = Infinity;
  for (const item of queue) {
    if (item.nextAttemptAt != null && item.nextAttemptAt < when) when = item.nextAttemptAt;
  }
  if (when !== Infinity) ext.alarms.create(ALARM, { when });
}

/** Wire the alarm listener and resume delivery (called once from the SW entry). */
export function installFlushAlarm(): void {
  ext.alarms.onAlarm.addListener((alarm) => {
    if (alarm.name === ALARM) void flush();
  });
  // Alarms can be cleared by a browser restart and are cleared by an extension
  // update, so a queue left from before would otherwise wait for the next turn.
  // Flushing on worker start sends what is due and re-arms the retry for the rest.
  void flush();
  // A held queue goes out as soon as Chrome delivers or changes the policy.
  ext.storage.onChanged?.addListener((_changes, area) => {
    if (area === 'managed') void flush();
  });
}
