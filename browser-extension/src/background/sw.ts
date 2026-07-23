// Service-worker entry. Wires message handling, assembles turns, and hands
// completed turns to the durable delivery queue.

import '../adapters/claude.js'; // registers the Claude adapter (side effect)
import type { RelayMessage, Settings } from '../shared/types.js';
import { getSettings, saveSettings, siteEnabled } from './settings.js';
import { Assembler } from './assembler.js';
import { enqueueTurn, flush, installFlushAlarm } from './delivery.js';

const assembler = new Assembler();

// Keep the SW awake while any stream is active, so mid-stream suspension is
// rare. Correctness never depends on this — durable state covers the rest.
let keepAliveTimer: ReturnType<typeof setInterval> | undefined;
function updateKeepAlive(): void {
  if (assembler.active > 0 && keepAliveTimer == null) {
    keepAliveTimer = setInterval(() => chrome.runtime.getPlatformInfo(() => {}), 20_000);
  } else if (assembler.active === 0 && keepAliveTimer != null) {
    clearInterval(keepAliveTimer);
    keepAliveTimer = undefined;
  }
}

chrome.runtime.onInstalled.addListener(() => {
  void saveSettings({}); // materialize defaults
});

installFlushAlarm();

chrome.runtime.onMessage.addListener((msg: RelayMessage | ControlMessage, sender, sendResponse) => {
  if (isControl(msg)) {
    handleControl(msg).then(sendResponse);
    return true; // async response
  }
  if (msg?.type === 'BEACON_RAW') {
    handleRaw(msg, sender.tab?.id);
    return false;
  }
  return false;
});

async function handleRaw(msg: RelayMessage, tabId: number | undefined): Promise<void> {
  const settings = await getSettings();
  if (!settings.enabled) return;

  const turn = assembler.handle(msg.host, tabId, msg.event);
  updateKeepAlive();
  if (!turn) return;
  if (!siteEnabled(settings, turn.site)) return;

  await enqueueTurn(turn, settings);
}

// ---- Control messages from popup/options ----

type ControlMessage =
  | { type: 'GET_STATUS' }
  | { type: 'SET_SETTINGS'; patch: Partial<Settings> }
  | { type: 'FLUSH_NOW' };

function isControl(msg: any): msg is ControlMessage {
  return msg?.type === 'GET_STATUS' || msg?.type === 'SET_SETTINGS' || msg?.type === 'FLUSH_NOW';
}

async function handleControl(msg: ControlMessage): Promise<unknown> {
  switch (msg.type) {
    case 'GET_STATUS': {
      const settings = await getSettings();
      const q = await chrome.storage.local.get('delivery_queue');
      return { settings, queueDepth: (q.delivery_queue ?? []).length, active: assembler.active };
    }
    case 'SET_SETTINGS':
      return { settings: await saveSettings(msg.patch) };
    case 'FLUSH_NOW':
      await flush();
      return { ok: true };
  }
}
