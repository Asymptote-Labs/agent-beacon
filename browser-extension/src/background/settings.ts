// Storage-backed settings with defaults. Lives in chrome.storage.local so it
// survives service-worker suspension.

import { ext } from '../shared/browser.js';
import { DEFAULT_SETTINGS, type Settings, type SiteName } from '../shared/types.js';

const KEY = 'settings';

export async function getSettings(): Promise<Settings> {
  const got = await ext.storage.local.get(KEY);
  const stored = (got[KEY] ?? {}) as Partial<Settings>;
  return {
    ...DEFAULT_SETTINGS,
    ...stored,
    sites: { ...DEFAULT_SETTINGS.sites, ...(stored.sites ?? {}) },
  };
}

export async function saveSettings(patch: Partial<Settings>): Promise<Settings> {
  const current = await getSettings();
  const next: Settings = {
    ...current,
    ...patch,
    sites: { ...current.sites, ...(patch.sites ?? {}) },
  };
  await ext.storage.local.set({ [KEY]: next });
  return next;
}

export function siteEnabled(settings: Settings, site: SiteName): boolean {
  return settings.enabled && settings.sites[site] !== false;
}
