// Storage-backed settings with defaults. Lives in chrome.storage.local so it
// survives service-worker suspension.
//
// An administrator can also fix settings through Chrome policy, which Chrome
// delivers in chrome.storage.managed (keys and types in src/managed_schema.json).
// Managed values win over stored ones every time settings are read, so a lock
// holds even if something writes storage.local directly; refusing to change a
// locked setting from the popup or options page is only how the UI says so.

import { ext } from '../shared/browser.js';
import { serial } from '../shared/serial.js';
import { DEFAULT_SETTINGS, type LockedSetting, type Settings, type SiteName } from '../shared/types.js';
import { parseSettingsPatch } from '../shared/validate.js';

const KEY = 'settings';
// Concurrent popup and options patches must merge over the preceding write, not a stale snapshot.
const withSettingsLock = serial();

/** Effective settings plus the ones policy fixed. `managedError` is set when Chrome could not
 *  read the policy values, in which case nothing may be captured or changed: the locks in force
 *  are unknown. */
export interface SettingsState {
  settings: Settings;
  locked: LockedSetting[];
  managedError?: string;
}

/**
 * Merge stored settings over the defaults, keeping only stored values that
 * still validate. A value saved by an older build that is no longer allowed
 * (an endpoint off loopback, say) falls back to its default.
 */
export function resolveSettings(stored: unknown): Settings {
  const s = (stored ?? {}) as Partial<Settings>;
  const out: Settings = { ...DEFAULT_SETTINGS, sites: { ...DEFAULT_SETTINGS.sites } };
  for (const key of ['enabled', 'retention', 'endpoint'] as const) {
    if (!(key in s)) continue;
    const r = parseSettingsPatch({ [key]: s[key] });
    if (r.ok) Object.assign(out, r.patch);
  }
  if (s.sites && typeof s.sites === 'object') {
    for (const site of Object.keys(out.sites) as SiteName[]) {
      if (typeof s.sites[site] === 'boolean') out.sites[site] = s.sites[site];
    }
  }
  return out;
}

/**
 * Read policy values with the same checks a stored value gets. Chrome already
 * drops values that do not match the schema; this also holds managed values to
 * the loopback endpoint rule. A value that fails is ignored on its own and
 * locks nothing.
 */
export function resolveManaged(raw: unknown): { patch: Partial<Settings>; locked: LockedSetting[] } {
  const m = (raw ?? {}) as Record<string, unknown>;
  const patch: Partial<Settings> = {};
  const locked: LockedSetting[] = [];
  for (const key of ['enabled', 'retention', 'endpoint'] as const) {
    if (!(key in m)) continue;
    const r = parseSettingsPatch({ [key]: m[key] });
    if (!r.ok) continue;
    Object.assign(patch, r.patch);
    locked.push(key);
  }
  const sites = m.sites;
  if (sites && typeof sites === 'object' && !Array.isArray(sites)) {
    for (const site of Object.keys(DEFAULT_SETTINGS.sites) as SiteName[]) {
      const on = (sites as Record<string, unknown>)[site];
      if (typeof on !== 'boolean') continue;
      patch.sites = { ...(patch.sites ?? {}), [site]: on } as Settings['sites'];
      locked.push(`sites.${site}`);
    }
  }
  return { patch, locked };
}

/** The first setting in `patch` that policy has locked, if any. */
export function lockedIn(patch: Partial<Settings>, locked: readonly LockedSetting[]): LockedSetting | undefined {
  return locked.find((key) => {
    if (!key.startsWith('sites.')) return key in patch;
    return patch.sites != null && key.slice('sites.'.length) in patch.sites;
  });
}

export async function getSettingsState(): Promise<SettingsState> {
  const [stored, managed] = await Promise.all([getStoredSettings(), readManaged()]);
  const { patch, locked } = resolveManaged(managed.values);
  const state: SettingsState = { settings: merge(stored, patch), locked };
  if (managed.error) state.managedError = managed.error;
  return state;
}

export async function getSettings(): Promise<Settings> {
  return (await getSettingsState()).settings;
}

/**
 * Apply an already-validated patch (see parseSettingsPatch) to the stored
 * settings and return the effective ones. Policy values are never written to
 * storage.local, so removing a policy brings back what the user had chosen.
 */
export function saveSettings(patch: Partial<Settings>): Promise<Settings> {
  return withSettingsLock(async () => {
    const [current, managed] = await Promise.all([getStoredSettings(), readManaged()]);
    const next = merge(current, patch);
    await ext.storage.local.set({ [KEY]: next });
    return merge(next, resolveManaged(managed.values).patch);
  });
}

export function siteEnabled(settings: Settings, site: SiteName): boolean {
  return settings.enabled && settings.sites[site] !== false;
}

async function getStoredSettings(): Promise<Settings> {
  const got = await ext.storage.local.get(KEY);
  return resolveSettings(got[KEY]);
}

// No managed area (test doubles) and Firefox's refusal when no enterprise policy
// configures the extension both mean no policy. Any other failure is reported, not
// read as "no policy": that would drop an admin's locks silently.
async function readManaged(): Promise<{ values?: unknown; error?: string }> {
  const area = ext.storage.managed;
  if (!area) return {};
  try {
    return { values: await area.get(null) };
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    if (/managed storage manifest not found/i.test(message)) return {};
    return { error: message };
  }
}

function merge(base: Settings, patch: Partial<Settings>): Settings {
  return { ...base, ...patch, sites: { ...base.sites, ...(patch.sites ?? {}) } };
}
