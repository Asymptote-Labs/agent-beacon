import { afterEach, beforeEach, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({ get: vi.fn(), set: vi.fn(), alarm: vi.fn() }));
vi.mock('../../src/shared/browser.js', () => ({ ext: {
  storage: { local: { get: api.get, set: api.set } },
  alarms: { create: api.alarm },
} }));
// The persisted shape of a delivery queue item, as delivery.ts stores it.
type StoredItem = { id: string; endpoint: string; envelope: object; attempts: number; nextAttemptAt?: number };
let queue: StoredItem[];
let send: ReturnType<typeof vi.fn>;
beforeEach(() => {
  vi.resetModules();
  vi.useFakeTimers();
  vi.setSystemTime(10_000);
  queue = [{ id: 'turn', endpoint: 'http://127.0.0.1:4318/v1/logs', envelope: {}, attempts: 1, nextAttemptAt: 11_000 }];
  api.get.mockImplementation(async () => ({ delivery_queue: structuredClone(queue) }));
  api.set.mockImplementation(async ({ delivery_queue }: { delivery_queue: StoredItem[] }) => { queue = structuredClone(delivery_queue); });
  api.alarm.mockClear();
  send = vi.fn().mockResolvedValue({ ok: false, status: 503 });
  vi.stubGlobal('fetch', send);
});
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); });

it('still delivers a legacy queue item without a persisted deadline', async () => {
  delete queue[0].nextAttemptAt;
  send.mockResolvedValue({ ok: true });
  const { flush } = await import('../../src/background/delivery.js');
  await flush();
  expect(send).toHaveBeenCalledTimes(1);
  expect(queue).toEqual([]);
});
