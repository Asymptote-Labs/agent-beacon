import { expect, it, vi } from 'vitest';
import { DEFAULT_SETTINGS } from '../../src/shared/types.js';
const storage = vi.hoisted(() => ({ get: vi.fn(), set: vi.fn() }));
vi.mock('../../src/shared/browser.js', () => ({ ext: { storage: { local: storage } } }));
import { resolveSettings, saveSettings } from '../../src/background/settings.js';
it('uses defaults for nonobject storage values', () => {
  for (const raw of [false, true, 0, 42, 'broken', null, undefined, []])
    expect(resolveSettings(raw)).toEqual(DEFAULT_SETTINGS);
});
it('can save a valid patch after encountering corrupt storage', async () => {
  storage.get.mockResolvedValue({ settings: 'broken' });
  storage.set.mockResolvedValue(undefined);
  await expect(saveSettings({ enabled: false })).resolves.toEqual({ ...DEFAULT_SETTINGS, enabled: false });
  expect(storage.set).toHaveBeenCalledWith({ settings: { ...DEFAULT_SETTINGS, enabled: false } });
});
it('keeps valid stored fields and independently defaults invalid fields', () => {
  expect(resolveSettings({ enabled: false, retention: 'invalid', sites: { chatgpt_web: false } })).toEqual({ ...DEFAULT_SETTINGS, enabled: false, sites: { claude_web: true, chatgpt_web: false } });
});
