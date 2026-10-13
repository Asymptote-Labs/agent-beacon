import { afterEach, beforeEach, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({ get: vi.fn(), set: vi.fn(), alarm: vi.fn() }));
vi.mock('../../src/shared/browser.js', () => ({ ext: {
  storage: { local: { get: api.get, set: api.set } },
  alarms: { create: api.alarm },
} }));
let queue: any[];
let send: ReturnType<typeof vi.fn>;
beforeEach(() => {
  vi.resetModules();
  vi.useFakeTimers();
  vi.setSystemTime(10_000);
  queue = [{ id: 'turn', endpoint: 'http://127.0.0.1:4318/v1/logs', envelope: {}, attempts: 1, nextAttemptAt: 11_000 }];
  api.get.mockImplementation(async () => ({ delivery_queue: structuredClone(queue) }));
  api.set.mockImplementation(async ({ delivery_queue }) => { queue = structuredClone(delivery_queue); });
  api.alarm.mockClear();
  send = vi.fn().mockResolvedValue({ ok: false });
  vi.stubGlobal('fetch', send);
});
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); });

it('preserves a waiting batch and its alarm deadline across unrelated flushes', async () => {
  const { flush } = await import('../../src/background/delivery.js');
  await flush();
  await flush();
  expect(send).not.toHaveBeenCalled();
  expect(queue[0].attempts).toBe(1);
  expect(api.alarm).toHaveBeenLastCalledWith('beacon_flush', { when: 11_000 });
});

it('retries a due batch and persists its next exponential deadline', async () => {
  vi.setSystemTime(11_000);
  const { flush } = await import('../../src/background/delivery.js');
  await flush();
  expect(send).toHaveBeenCalledTimes(1);
  expect(queue[0]).toMatchObject({ attempts: 2, nextAttemptAt: 12_000 });
});

it('still delivers a legacy queue item without a persisted deadline', async () => {
  delete queue[0].nextAttemptAt;
  send.mockResolvedValue({ ok: true });
  const { flush } = await import('../../src/background/delivery.js');
  await flush();
  expect(send).toHaveBeenCalledTimes(1);
  expect(queue).toEqual([]);
});

it('keeps the existing maximum attempt limit for due failures', async () => {
  vi.setSystemTime(11_000);
  queue[0].attempts = 5;
  const { flush } = await import('../../src/background/delivery.js');
  await flush();
  expect(send).toHaveBeenCalledTimes(1);
  expect(queue).toEqual([]);
});
