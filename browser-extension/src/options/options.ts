// Options UI: endpoint override + per-site enable.
import type { Settings } from '../shared/types.js';

const $ = <T extends HTMLElement>(id: string) => document.getElementById(id) as T;

async function load(): Promise<void> {
  const { settings } = (await chrome.runtime.sendMessage({ type: 'GET_STATUS' })) as {
    settings: Settings;
  };
  ($('endpoint') as HTMLInputElement).value = settings.endpoint;
  ($('site_claude_web') as HTMLInputElement).checked = settings.sites.claude_web !== false;
  ($('site_chatgpt_web') as HTMLInputElement).checked = settings.sites.chatgpt_web !== false;
}

$('save').addEventListener('click', async () => {
  const patch: Partial<Settings> = {
    endpoint: ($('endpoint') as HTMLInputElement).value.trim(),
    sites: {
      claude_web: ($('site_claude_web') as HTMLInputElement).checked,
      chatgpt_web: ($('site_chatgpt_web') as HTMLInputElement).checked,
    },
  };
  await chrome.runtime.sendMessage({ type: 'SET_SETTINGS', patch });
  const saved = $('saved');
  saved.hidden = false;
  setTimeout(() => (saved.hidden = true), 1500);
});

void load();
