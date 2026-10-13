import { beforeEach, expect, it, vi } from 'vitest';

const storage = vi.hoisted(() => ({ get: vi.fn(), set: vi.fn() }));
vi.mock('../../src/shared/browser.js', () => ({ ext: { storage: { local: storage } } }));

beforeEach(() => {
  vi.resetModules();
  storage.get.mockReset();
  storage.set.mockReset();
});

it('merges concurrent patches against the preceding persisted settings', async () => {
  let stored = { enabled: false, retention: 'full' };
  storage.get.mockImplementation(async () => ({ settings: { ...stored } }));
  storage.set.mockImplementation(async ({ settings }) => { stored = settings; });
  const { saveSettings } = await import('../../src/background/settings.js');
  await Promise.all([saveSettings({ enabled: true }), saveSettings({ retention: 'metadata' })]);
  expect(stored).toMatchObject({ enabled: true, retention: 'metadata' });
});

it('reports a rejected write and still allows the following save', async () => {
  storage.get.mockResolvedValue({ settings: { enabled: false } });
  storage.set.mockRejectedValueOnce(new Error('storage failed')).mockResolvedValueOnce(undefined);
  const { saveSettings } = await import('../../src/background/settings.js');
  const first = saveSettings({ enabled: true });
  const second = saveSettings({ retention: 'metadata' });
  await expect(first).rejects.toThrow('storage failed');
  await expect(second).resolves.toMatchObject({ retention: 'metadata' });
  expect(storage.set).toHaveBeenCalledTimes(2);
});
