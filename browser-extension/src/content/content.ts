// ISOLATED-world content script. The bridge between the page (MAIN interceptor)
// and the extension service worker. Deliberately thin: validate the message
// origin, then forward raw intercept events to the SW. All parsing/normalizing
// happens in the SW so this script stays resistant to page interference.

import { BEACON_MSG, type BeaconWindowMessage, type RelayMessage } from '../shared/types.js';

/**
 * True only when the extension context this content script belongs to is still
 * alive. After the extension is reloaded/updated, previously-injected content
 * scripts in already-open tabs are orphaned: `chrome.runtime` is torn down and
 * touching `.sendMessage` throws. Guarding here degrades gracefully (a tab
 * refresh re-injects a fresh, connected content script).
 */
function extensionAlive(): boolean {
  try {
    return typeof chrome !== 'undefined' && !!chrome.runtime && !!chrome.runtime.id;
  } catch {
    return false;
  }
}

let warnedInvalidated = false;

window.addEventListener('message', (event: MessageEvent) => {
  // Only trust messages from this same window/frame.
  if (event.source !== window) return;
  const data = event.data as BeaconWindowMessage | undefined;
  if (!data || data.source !== BEACON_MSG) return;

  if (!extensionAlive()) {
    if (!warnedInvalidated) {
      warnedInvalidated = true;
      console.debug('[agent-beacon] extension context invalidated; refresh this tab to resume capture.');
    }
    return;
  }

  const relay: RelayMessage = {
    type: 'BEACON_RAW',
    host: window.location.host,
    event: data.event,
  };
  // Fire-and-forget; the SW may be asleep and will wake to handle it. Wrap in
  // try/catch in case the context is invalidated between the check and the call.
  try {
    void chrome.runtime.sendMessage(relay).catch(() => {
      /* SW not ready / extension reloading — safe to drop a single event */
    });
  } catch {
    /* context invalidated mid-flight */
  }
});
