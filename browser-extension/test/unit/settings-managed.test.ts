// Settings an administrator fixes through Chrome policy (chrome.storage.managed) win over the
// stored ones, are reported as locked, and are never written back to storage.local.
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { DEFAULT_SETTINGS } from '../../src/shared/types.js';

const local = vi.hoisted(() => ({ get: vi.fn(), set: vi.fn() }));
const managed = vi.hoisted(() => ({ get: vi.fn() }));
vi.mock('../../src/shared/browser.js', () => ({ ext: { storage: { local, managed } } }));

const { getSettingsState, lockedIn, resolveManaged, saveSettings } = await import('../../src/background/settings.js');

let stored: Record<string, unknown> | undefined;
beforeEach(() => {
  stored = undefined;
  local.get.mockReset().mockImplementation(async () => ({ settings: stored }));
  local.set.mockReset().mockImplementation(async ({ settings }) => {
    stored = settings;
  });
  managed.get.mockReset().mockResolvedValue({});
});

describe('resolveManaged', () => {
  it('locks each policy value that passes the same checks a stored value gets', () => {
    expect(
      resolveManaged({ enabled: true, retention: 'metadata', sites: { chatgpt_web: false } }),
    ).toEqual({
      patch: { enabled: true, retention: 'metadata', sites: { chatgpt_web: false } },
      locked: ['enabled', 'retention', 'sites.chatgpt_web'],
    });
  });

  it('ignores a value that fails, without dropping the others', () => {
    const { patch, locked } = resolveManaged({
      endpoint: 'https://collector.example.com/v1/logs',
      retention: 'everything',
      enabled: false,
      sites: { claude_web: 'yes', other_site: true },
    });
    expect(patch).toEqual({ enabled: false });
    expect(locked).toEqual(['enabled']);
  });

  it('reads no policy as nothing locked', () => {
    expect(resolveManaged(undefined)).toEqual({ patch: {}, locked: [] });
  });
});

describe('lockedIn', () => {
  const locked = ['retention', 'sites.claude_web'] as const;
  it('names the first locked setting a patch touches', () => {
    expect(lockedIn({ enabled: true, retention: 'full' }, locked)).toBe('retention');
    expect(lockedIn({ sites: { claude_web: true } as never }, locked)).toBe('sites.claude_web');
  });
  it('lets a patch through when it touches only unlocked settings', () => {
    expect(lockedIn({ enabled: false, sites: { chatgpt_web: false } as never }, locked)).toBeUndefined();
  });
});

describe('effective settings', () => {
  it('lays policy values over the stored ones and reports them locked', async () => {
    stored = { ...DEFAULT_SETTINGS, enabled: false, retention: 'redacted' };
    managed.get.mockResolvedValue({ enabled: true, endpoint: 'http://127.0.0.1:4400/v1/logs' });
    const { settings, locked } = await getSettingsState();
    expect(settings).toMatchObject({ enabled: true, retention: 'redacted', endpoint: 'http://127.0.0.1:4400/v1/logs' });
    expect(locked).toEqual(['enabled', 'endpoint']);
  });

  it("reads Firefox's no-policy refusal as unmanaged", async () => {
    managed.get.mockRejectedValue(new Error('Managed storage manifest not found'));
    expect(await getSettingsState()).toEqual({ settings: DEFAULT_SETTINGS, locked: [] });
  });

  it('reports any other managed-storage failure instead of reading it as no policy', async () => {
    managed.get.mockRejectedValue(new Error('IO error: LOCK: Resource temporarily unavailable'));
    const state = await getSettingsState();
    expect(state.managedError).toContain('IO error');
    expect(state.locked).toEqual([]);
  });

  it('saves the user choice beside a lock without writing the policy value', async () => {
    managed.get.mockResolvedValue({ retention: 'metadata' });
    const effective = await saveSettings({ enabled: false });
    expect(effective).toMatchObject({ enabled: false, retention: 'metadata' });
    // Removing the policy later must bring back what the user had, not the locked value.
    expect(stored).toMatchObject({ enabled: false, retention: 'full' });
  });
});
