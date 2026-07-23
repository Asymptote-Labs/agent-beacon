// Popup UI: toggle capture, choose retention, show live status.
import type { Settings } from '../shared/types.js';

interface Status {
  settings: Settings;
  queueDepth: number;
  active: number;
}

const $ = <T extends HTMLElement>(id: string) => document.getElementById(id) as T;

async function refresh(): Promise<void> {
  const status = (await chrome.runtime.sendMessage({ type: 'GET_STATUS' })) as Status;
  ($('enabled') as HTMLInputElement).checked = status.settings.enabled;
  ($('retention') as HTMLSelectElement).value = status.settings.retention;
  $('endpoint').textContent = status.settings.endpoint;
  $('queue').textContent = String(status.queueDepth);
  $('active').textContent = String(status.active);
}

$('enabled').addEventListener('change', async (e) => {
  await chrome.runtime.sendMessage({
    type: 'SET_SETTINGS',
    patch: { enabled: (e.target as HTMLInputElement).checked },
  });
  await refresh();
});

$('retention').addEventListener('change', async (e) => {
  await chrome.runtime.sendMessage({
    type: 'SET_SETTINGS',
    patch: { retention: (e.target as HTMLSelectElement).value as Settings['retention'] },
  });
  await refresh();
});

void refresh();
