// Options UI: endpoint override + per-site enable.
import { ext } from '../shared/browser.js';
import { mountPermissionBanner } from '../shared/permissions.js';
import type { LockedSetting, Settings, SiteName } from '../shared/types.js';

const $ = <T extends HTMLElement>(id: string) => document.getElementById(id) as T;
const SITES: readonly SiteName[] = ['claude_web', 'chatgpt_web'];
const siteBox = (site: SiteName) => $(`site_${site}`) as HTMLInputElement;

interface SettingsReply {
  settings?: Settings;
  locked?: LockedSetting[];
  managedError?: string;
  error?: string;
}

// Settings fixed by policy: shown, disabled, and left out of every save.
let locked = new Set<LockedSetting>();

/** Show what policy enforces. Locked controls take the enforced value; unlocked ones keep whatever
 *  the user has typed, so a save refused because a lock arrived can be repeated without it. */
function applyPolicy(reply: SettingsReply): void {
  if (!reply.settings) return;
  locked = new Set(reply.locked ?? []);
  const unreadable = reply.managedError != null;
  const endpoint = $('endpoint') as HTMLInputElement;
  endpoint.disabled = unreadable || locked.has('endpoint');
  if (locked.has('endpoint')) endpoint.value = reply.settings.endpoint;
  for (const site of SITES) {
    const box = siteBox(site);
    box.disabled = unreadable || locked.has(`sites.${site}`);
    if (locked.has(`sites.${site}`)) box.checked = reply.settings.sites[site] !== false;
  }
  ($('save') as HTMLButtonElement).disabled =
    endpoint.disabled && SITES.every((site) => siteBox(site).disabled);
  $('managed').textContent = unreadable
    ? "Your organization's settings could not be read, so capture is paused and nothing can be changed."
    : 'Some settings are managed by your organization and cannot be changed here.';
  $('managed').hidden = !unreadable && locked.size === 0;
}

async function load(): Promise<void> {
  const status = (await ext.runtime.sendMessage({ type: 'GET_STATUS' })) as SettingsReply & { settings: Settings };
  ($('endpoint') as HTMLInputElement).value = status.settings.endpoint;
  for (const site of SITES) siteBox(site).checked = status.settings.sites[site] !== false;
  applyPolicy(status);
}

$('save').addEventListener('click', async () => {
  const patch: Partial<Settings> = {};
  if (!locked.has('endpoint')) patch.endpoint = ($('endpoint') as HTMLInputElement).value.trim();
  const sites: Partial<Record<SiteName, boolean>> = {};
  for (const site of SITES) {
    if (!locked.has(`sites.${site}`)) sites[site] = siteBox(site).checked;
  }
  if (Object.keys(sites).length > 0) patch.sites = sites as Settings['sites'];
  const res = (await ext.runtime.sendMessage({ type: 'SET_SETTINGS', patch })) as SettingsReply;
  // Every reply carries the policy now in force, which may have changed since the page loaded.
  if (res) applyPolicy(res);
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
