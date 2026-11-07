// Service-worker entry. Wires message handling, assembles turns, and hands
// completed turns to the durable delivery queue.

import '../adapters/claude.js'; // registers the Claude adapter (side effect)
import '../adapters/chatgpt.js'; // registers the ChatGPT adapter (side effect)
import { ext } from '../shared/browser.js';
import type { ChatTurn } from '../shared/types.js';
import { getSettingsState, lockedIn, saveSettings, siteEnabled } from './settings.js';
import { Assembler } from './assembler.js';
import { enqueueTurn, flush, installFlushAlarm, markInstalled } from './delivery.js';
import { detectBrowser } from '../shared/browser-identity.js';
import { parseHost, parseInterceptEvent, parseSettingsPatch } from '../shared/validate.js';
import { isExtensionPage, isOwnContentScript, senderHost, type SenderLike } from './sender.js';

const assembler = new Assembler();

// Which Chromium fork (or other browser) this is. Resolved once: the brand
// list and UA string are fixed for the life of the worker. Stamped on every
// emitted turn as user_agent.* / browser.brands.
const browserIdentity = detectBrowser(globalThis.navigator);

// Keep the SW awake while any stream is active, so mid-stream suspension is
// rare. Correctness never depends on this — durable state covers the rest.
// Each tick also closes streams that went idle, so a stream that never sends
// `done` cannot keep the worker awake.
let keepAliveTimer: ReturnType<typeof setInterval> | undefined;
function updateKeepAlive(): void {
  if (assembler.active > 0 && keepAliveTimer == null) {
    keepAliveTimer = setInterval(() => {
      finalizeAll(assembler.sweep());
      updateKeepAlive();
      if (keepAliveTimer != null) void ext.runtime.getPlatformInfo();
    }, 20_000);
  } else if (assembler.active === 0 && keepAliveTimer != null) {
    clearInterval(keepAliveTimer);
    keepAliveTimer = undefined;
  }
}

function finalizeAll(turns: ChatTurn[]): void {
  for (const turn of turns) void finalizeTurn(turn);
}

ext.runtime.onInstalled.addListener((details) => {
  void saveSettings({}); // materialize defaults
  if (details.reason === 'install' || details.reason === 'update') void markInstalled();
});

installFlushAlarm();

// A closed tab's streams will never send `done`; finalize what they captured.
ext.tabs?.onRemoved?.addListener((tabId) => {
  finalizeAll(assembler.closeTab(tabId));
  updateKeepAlive();
});

ext.runtime.onMessage.addListener((msg: unknown, sender, sendResponse) => {
  const type = (msg as { type?: unknown } | null)?.type;
  if (isControlType(type)) {
    if (!isExtensionPage(sender as SenderLike, ext.runtime.id, ext.runtime.getURL(''))) return false;
    handleControl(msg as ControlMessage).then(sendResponse);
    return true; // async response
  }
  if (type === 'BEACON_RAW') {
    if (!isOwnContentScript(sender as SenderLike, ext.runtime.id)) return false;
    const event = parseInterceptEvent((msg as { event?: unknown }).event);
    // Prefer the browser's record of the sending frame over the message body.
    const host = senderHost(sender as SenderLike) ?? parseHost((msg as { host?: unknown }).host);
    if (!event || !host) return false;
    // Feed the assembler SYNCHRONOUSLY, in message-arrival order. Stream events
    // arrive ordered (request → chunk → … → done); an `await` before this call
    // could let a chunk be processed before its `request` registered a parser,
    // dropping early SSE (e.g. message_start). Async work (settings, delivery)
    // happens only after a turn is finalized.
    finalizeAll(assembler.handle(host, sender.tab?.id, event));
    updateKeepAlive();
    return false;
  }
  return false;
});

async function finalizeTurn(turn: ChatTurn): Promise<void> {
  const { settings, managedError } = await getSettingsState();
  // Fail closed: when Chrome cannot read the policy, the retention and endpoint an admin locked
  // are unknown, so the turn is not captured rather than sent under the user's settings.
  if (managedError) return;
  // siteEnabled covers both the global toggle and the per-site toggle.
  if (!siteEnabled(settings, turn.site)) return;
  await enqueueTurn(turn, settings, browserIdentity);
}

// ---- Control messages from popup/options ----

type ControlMessage =
  | { type: 'GET_STATUS' }
  | { type: 'SET_SETTINGS'; patch: unknown }
  | { type: 'FLUSH_NOW' };

function isControlType(type: unknown): type is ControlMessage['type'] {
  return type === 'GET_STATUS' || type === 'SET_SETTINGS' || type === 'FLUSH_NOW';
}

async function handleControl(msg: ControlMessage): Promise<unknown> {
  switch (msg.type) {
    case 'GET_STATUS': {
      const state = await getSettingsState();
      const q = await ext.storage.local.get('delivery_queue');
      return { ...state, queueDepth: (q.delivery_queue ?? []).length, active: assembler.active };
    }
    case 'SET_SETTINGS': {
      const parsed = parseSettingsPatch(msg.patch);
      const state = await getSettingsState();
      if (!parsed.ok) return { error: parsed.error, ...state };
      if (state.managedError) {
        return { error: `your organization's settings could not be read: ${state.managedError}`, ...state };
      }
      const locked = lockedIn(parsed.patch, state.locked);
      if (locked) return { error: `${locked} is set by your organization and cannot be changed here`, ...state };
      return { settings: await saveSettings(parsed.patch), locked: state.locked };
    }
    case 'FLUSH_NOW':
      await flush();
      return { ok: true };
  }
}
