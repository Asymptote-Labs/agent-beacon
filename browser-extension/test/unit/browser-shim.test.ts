// The browser.* / chrome.* namespace shim (src/shared/browser.ts).
import { describe, it, expect } from 'vitest';
import { resolveExtensionApi } from '../../src/shared/browser.js';

const chromeNs = { runtime: { id: 'chrome-ext-id' }, tag: 'chrome' };
const browserNs = { runtime: { id: 'firefox-ext-id' }, tag: 'browser' };

describe('resolveExtensionApi', () => {
  it('uses chrome.* on Chrome, where there is no browser global', () => {
    expect(resolveExtensionApi({ chrome: chromeNs })).toBe(chromeNs);
  });

  it('prefers the promise-based browser.* on Firefox/Safari, where both exist', () => {
    expect(resolveExtensionApi({ browser: browserNs, chrome: chromeNs })).toBe(browserNs);
  });

  it('uses browser.* when it is the only namespace', () => {
    expect(resolveExtensionApi({ browser: browserNs })).toBe(browserNs);
  });

  it('ignores a page element reachable as window.browser (named access)', () => {
    const element = { id: 'browser', tagName: 'DIV' };
    expect(resolveExtensionApi({ browser: element, chrome: chromeNs })).toBe(chromeNs);
  });

  it('ignores a browser object whose runtime has no id', () => {
    expect(resolveExtensionApi({ browser: { runtime: {} }, chrome: chromeNs })).toBe(chromeNs);
    expect(resolveExtensionApi({ browser: { runtime: { id: '' } }, chrome: chromeNs })).toBe(chromeNs);
  });

  it('keeps chrome when its runtime is torn down (orphaned content script)', () => {
    // content.ts reads ext.runtime.id live to detect this; the shim must still
    // hand back the object rather than undefined.
    const orphaned = { runtime: undefined };
    expect(resolveExtensionApi({ chrome: orphaned })).toBe(orphaned);
  });

  it('returns undefined outside an extension', () => {
    expect(resolveExtensionApi({})).toBeUndefined();
    expect(resolveExtensionApi({ chrome: 'nope' })).toBeUndefined();
  });
});
