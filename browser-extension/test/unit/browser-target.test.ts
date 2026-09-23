import { describe, it, expect } from 'vitest';
import { resolveBrowserTarget } from '../../e2e/browser-target.js';

describe('resolveBrowserTarget (e2e browser selection)', () => {
  it('defaults to the chromium channel when nothing is set', () => {
    const t = resolveBrowserTarget({});
    expect(t.launch).toEqual({ channel: 'chromium' });
    expect(t.expectedUserAgentNames).toContain('Chromium');
  });

  it('treats an empty or blank BROWSER_CHANNEL as the default', () => {
    expect(resolveBrowserTarget({ BROWSER_CHANNEL: '' }).launch).toEqual({ channel: 'chromium' });
    expect(resolveBrowserTarget({ BROWSER_CHANNEL: '  ' }).launch).toEqual({ channel: 'chromium' });
  });

  it('selects msedge and expects Edge to identify itself', () => {
    const t = resolveBrowserTarget({ BROWSER_CHANNEL: 'msedge' });
    expect(t.launch).toEqual({ channel: 'msedge' });
    expect(t.expectedUserAgentNames).toEqual(['Microsoft Edge']);
  });

  it('accepts pre-release channels and maps them to the stable expectation', () => {
    const t = resolveBrowserTarget({ BROWSER_CHANNEL: 'msedge-beta' });
    expect(t.launch).toEqual({ channel: 'msedge-beta' });
    expect(t.expectedUserAgentNames).toEqual(['Microsoft Edge']);
    expect(resolveBrowserTarget({ BROWSER_CHANNEL: 'chrome' }).expectedUserAgentNames).toEqual([
      'Google Chrome',
    ]);
  });

  it('BROWSER_EXECUTABLE wins over BROWSER_CHANNEL and carries no fixed expectation', () => {
    const t = resolveBrowserTarget({
      BROWSER_CHANNEL: 'msedge',
      BROWSER_EXECUTABLE: '/Applications/Brave Browser.app/Contents/MacOS/Brave Browser',
    });
    expect(t.launch).toEqual({
      executablePath: '/Applications/Brave Browser.app/Contents/MacOS/Brave Browser',
    });
    expect(t.expectedUserAgentNames).toBeUndefined();
  });

  it('rejects non-Chromium or misspelled channels with an actionable message', () => {
    expect(() => resolveBrowserTarget({ BROWSER_CHANNEL: 'firefox' })).toThrow(/BROWSER_EXECUTABLE/);
    expect(() => resolveBrowserTarget({ BROWSER_CHANNEL: 'edge' })).toThrow(/msedge/);
  });
});
