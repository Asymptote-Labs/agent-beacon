// Options UI: endpoint override + per-site enable.
import { ext } from '../shared/browser.js';
import { mountPermissionBanner } from '../shared/permissions.js';
import type { LockedSetting, Settings, SiteName } from '../shared/types.js';

const $ = <T extends HTMLElement>(id: string) => document.getElementById(id) as T;
const SITES: readonly SiteName[] = ['claude_web', 'chatgpt_web'];
const siteBox = (site: SiteName) => $(`site_${site}`) as HTMLInputElement;

// Settings fixed by policy: shown, disabled, and left out of every save.
let locked = new Set<LockedSetting>();

async function load(): Promise<void> {
  const status = (await ext.runtime.sendMessage({ type: 'GET_STATUS' })) as {
    settings: Settings;
    locked?: LockedSetting[];
  };
  locked = new Set(status.locked ?? []);
  const endpoint = $('endpoint') as HTMLInputElement;
  endpoint.value = status.settings.endpoint;
  endpoint.disabled = locked.has('endpoint');
  for (const site of SITES) {
    siteBox(site).checked = status.settings.sites[site] !== false;
    siteBox(site).disabled = locked.has(`sites.${site}`);
  }
  ($('save') as HTMLButtonElement).disabled =
    endpoint.disabled && SITES.every((site) => siteBox(site).disabled);
  $('managed').hidden = locked.size === 0;
}

$('save').addEventListener('click', async () => {
  const patch: Partial<Settings> = {};
  if (!locked.has('endpoint')) patch.endpoint = ($('endpoint') as HTMLInputElement).value.trim();
  const sites: Partial<Record<SiteName, boolean>> = {};
  for (const site of SITES) {
    if (!locked.has(`sites.${site}`)) sites[site] = siteBox(site).checked;
  }
  if (Object.keys(sites).length > 0) patch.sites = sites as Settings['sites'];
  const res = (await ext.runtime.sendMessage({ type: 'SET_SETTINGS', patch })) as {
    error?: string;
  };
  // The service worker validates every patch; a rejected one changes nothing.
  const error = $('error');
  error.textContent = res?.error ?? '';
  error.hidden = !res?.error;
  if (res?.error) return;
  const saved = $('saved');
  saved.hidden = false;
  setTimeout(() => (saved.hidden = true), 1500);
});

void load();
mountPermissionBanner(ext, document);
