// Queued batches go out under the policy in force when they are sent, and a copy installed by
// policy holds delivery right after an install until Chrome delivers its managed settings.
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { ChatTurn, Settings } from '../../src/shared/types.js';
import { DEFAULT_SETTINGS } from '../../src/shared/types.js';

const env = vi.hoisted(() => ({
  storage: new Map<string, unknown>(),
  managed: {} as Record<string, unknown> | Error,
  installType: 'normal',
  alarms: new Map<string, number>(),
  posted: [] as { url: string; body: string }[],
}));

vi.mock('../../src/shared/browser.js', () => ({
  ext: {
    storage: {
      local: {
        get: async (key: string) => (env.storage.has(key) ? { [key]: structuredClone(env.storage.get(key)) } : {}),
        set: async (items: Record<string, unknown>) => {
          for (const [k, v] of Object.entries(items)) env.storage.set(k, structuredClone(v));
        },
      },
      managed: {
        get: async () => {
          if (env.managed instanceof Error) throw env.managed;
          return structuredClone(env.managed);
        },
      },
    },
    management: { getSelf: async () => ({ installType: env.installType }) },
    alarms: { create: (name: string, info: { when: number }) => env.alarms.set(name, info.when) },
  },
}));

vi.stubGlobal('fetch', async (url: string, init: { body: string }) => {
  env.posted.push({ url, body: init.body });
  return { ok: true, status: 200 };
});

const { enqueueTurn, flush, markInstalled, POLICY_GRACE_MS } = await import('../../src/background/delivery.js');

function turn(id: string, site: ChatTurn['site'] = 'claude_web'): ChatTurn {
  return {
    site,
    sessionId: 'conv-1',
    turnId: id,
    promptText: `secret prompt ${id}`,
    inputMessages: [{ role: 'user', text: `secret prompt ${id}` }],
    outputMessages: [{ role: 'assistant', text: `answer ${id}` }],
    responseText: `answer ${id}`,
    toolCalls: [],
    startedAt: Date.now(),
    completedAt: Date.now(),
    captureMode: 'sse',
  };
}

const full: Settings = { ...DEFAULT_SETTINGS, retention: 'full' };
const queued = () => (env.storage.get('delivery_queue') as unknown[] | undefined) ?? [];

/** Queue a turn while delivery is held by an unreadable policy, so it waits in the queue. */
async function queueWhileHeld(t: ChatTurn, settings: Settings = full): Promise<void> {
  env.managed = new Error('IO error: LOCK');
  await enqueueTurn(t, settings);
  env.managed = {};
}

beforeEach(() => {
  env.storage.clear();
  env.managed = {};
  env.installType = 'normal';
  env.alarms.clear();
  env.posted.length = 0;
});

describe('policy at send time', () => {
  it('holds every batch while the policy cannot be read', async () => {
    await queueWhileHeld(turn('a'));
    expect(env.posted).toHaveLength(0);
    expect(queued()).toHaveLength(1);
    expect(env.alarms.has('beacon_flush')).toBe(true);
  });

  it('drops a queued batch whose content a stricter locked retention forbids', async () => {
    await queueWhileHeld(turn('a'));
    env.managed = { retention: 'metadata' };
    await flush();
    expect(env.posted).toHaveLength(0);
    expect(queued()).toHaveLength(0);
  });

  it('sends a queued batch that already meets the locked retention', async () => {
    await queueWhileHeld(turn('a'), { ...full, retention: 'metadata' });
    env.managed = { retention: 'redacted' };
    await flush();
    expect(env.posted).toHaveLength(1);
    expect(env.posted[0].body).not.toContain('secret prompt');
  });

  it('sends a queued batch to the locked endpoint instead of the one it was queued for', async () => {
    await queueWhileHeld(turn('a'));
    env.managed = { endpoint: 'http://127.0.0.1:4400/v1/logs' };
    await flush();
    expect(env.posted.map((p) => p.url)).toEqual(['http://127.0.0.1:4400/v1/logs']);
  });

  it('drops queued batches for a site or capture switched off by policy, and keeps the rest', async () => {
    await queueWhileHeld(turn('a', 'claude_web'));
    await queueWhileHeld(turn('b', 'chatgpt_web'));
    env.managed = { sites: { claude_web: false } };
    await flush();
    expect(env.posted).toHaveLength(1);
    expect(env.posted[0].body).toContain('chatgpt_web');

    await queueWhileHeld(turn('c'));
    env.managed = { enabled: false };
    await flush();
    expect(env.posted).toHaveLength(1);
    expect(queued()).toHaveLength(0);
  });
});

describe('policy-installed copy right after install', () => {
  it('holds delivery until the policy arrives, then applies it', async () => {
    env.installType = 'admin';
    await markInstalled();
    await enqueueTurn(turn('a'), full);
    expect(env.posted).toHaveLength(0);
    expect(queued()).toHaveLength(1);

    env.managed = { retention: 'metadata' };
    await flush();
    expect(env.posted).toHaveLength(0); // the full-text batch can no longer be sent
    expect(queued()).toHaveLength(0);
  });

  it('delivers once the grace period passes with no policy pushed', async () => {
    vi.useFakeTimers({ toFake: ['Date'] });
    try {
      env.installType = 'admin';
      await markInstalled();
      await enqueueTurn(turn('a'), full);
      expect(env.posted).toHaveLength(0);
      vi.setSystemTime(Date.now() + POLICY_GRACE_MS + 1);
      await flush();
      expect(env.posted).toHaveLength(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it('does not hold a copy the user installed', async () => {
    await markInstalled();
    await enqueueTurn(turn('a'), full);
    expect(env.posted).toHaveLength(1);
  });
});
