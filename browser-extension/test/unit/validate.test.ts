// Trust-boundary checks (src/shared/validate.ts): relayed intercept events,
// settings patches, and the loopback-only delivery endpoint.
import { describe, it, expect } from 'vitest';
import {
  LIMITS,
  isAllowedEndpoint,
  parseHost,
  parseInterceptEvent,
  parseSettingsPatch,
} from '../../src/shared/validate.js';
import { resolveSettings } from '../../src/background/settings.js';
import { DEFAULT_SETTINGS } from '../../src/shared/types.js';

const ID = '3f2b8c1e-0a4d-4e7b-9c55-1d2e3f4a5b6c';

describe('parseInterceptEvent', () => {
  it.each([
    [{ kind: 'request', reqId: ID, url: 'https://claude.ai/x', method: 'POST', body: '{}' }],
    [{ kind: 'request', reqId: ID, url: 'https://claude.ai/x', method: 'POST', body: null }],
    [{ kind: 'chunk', reqId: ID, chunk: 'data: {}\n\n' }],
    [{ kind: 'done', reqId: ID }],
    [{ kind: 'error', reqId: ID, message: 'TypeError: network' }],
  ])('accepts %o', (e) => {
    expect(parseInterceptEvent(e)).toEqual(e);
  });

  it('keeps only known fields', () => {
    expect(parseInterceptEvent({ kind: 'done', reqId: ID, extra: { evil: true } })).toEqual({ kind: 'done', reqId: ID });
  });

  it.each([
    ['null', null],
    ['a string', 'done'],
    ['an unknown kind', { kind: 'reset', reqId: ID }],
    ['a numeric (guessable) request id', { kind: 'done', reqId: 1 }],
    ['a short request id', { kind: 'done', reqId: 'abc' }],
    ['a request id with odd characters', { kind: 'done', reqId: '../../etc/passwd' }],
    ['a request with no url', { kind: 'request', reqId: ID, method: 'POST', body: null }],
    ['a request with an object body', { kind: 'request', reqId: ID, url: 'u', method: 'POST', body: {} }],
    ['a request with a missing body', { kind: 'request', reqId: ID, url: 'u', method: 'POST' }],
    ['a non-string chunk', { kind: 'chunk', reqId: ID, chunk: 42 }],
    ['an oversized chunk', { kind: 'chunk', reqId: ID, chunk: 'x'.repeat(LIMITS.chunk + 1) }],
    ['an oversized url', { kind: 'request', reqId: ID, url: 'x'.repeat(LIMITS.url + 1), method: 'POST', body: null }],
    ['an error with no message', { kind: 'error', reqId: ID }],
  ])('rejects %s', (_name, e) => {
    expect(parseInterceptEvent(e)).toBeNull();
  });
});

describe('parseHost', () => {
  it('accepts chat hosts', () => {
    expect(parseHost('claude.ai')).toBe('claude.ai');
    expect(parseHost('chatgpt.com:443')).toBe('chatgpt.com:443');
    expect(parseHost('chatgpt.com:65535')).toBe('chatgpt.com:65535');
  });
  it.each(['', 'claude.ai/x', 'a b', 'chatgpt.com:65536', 'chatgpt.com:99999', 42, null])('rejects %o', (h) => {
    expect(parseHost(h)).toBeNull();
  });
});

describe('isAllowedEndpoint', () => {
  it.each([
    'http://127.0.0.1:4318/v1/logs',
    'http://localhost:4318/v1/logs',
    'https://127.0.0.1:4318/v1/logs',
    'http://[::1]:4318/v1/logs',
    'http://127.0.0.1/v1/logs',
    'http://127.0.0.1:80/v1/logs',
  ])('allows %s', (u) => {
    expect(isAllowedEndpoint(u)).toBe(true);
  });

  it.each([
    ['a remote host', 'https://collector.example.com:4318/v1/logs'],
    ['another loopback-looking host', 'http://127.0.0.1.example.com:4318/v1/logs'],
    ['credentials', 'http://user:pw@127.0.0.1:4318/v1/logs'],
    ['a non-http scheme', 'ftp://127.0.0.1:4318/'],
    ['a javascript URL', 'javascript:alert(1)'],
    ['a private LAN address', 'http://192.168.1.10:4318/v1/logs'],
    ['not a URL', 'not a url'],
    ['a non-string', 4318],
  ])('refuses %s', (_name, u) => {
    expect(isAllowedEndpoint(u)).toBe(false);
  });
});

describe('parseSettingsPatch', () => {
  it('accepts every known key with a valid value', () => {
    const patch = {
      enabled: false,
      retention: 'metadata',
      endpoint: 'http://localhost:4318/v1/logs',
      sites: { claude_web: false },
    };
    expect(parseSettingsPatch(patch)).toEqual({ ok: true, patch });
  });

  it.each([
    ['an unknown key', { proxy: 'x' }],
    ['a non-boolean enabled', { enabled: 'yes' }],
    ['an unknown retention', { retention: 'everything' }],
    ['a remote endpoint', { endpoint: 'https://attacker.example/v1/logs' }],
    ['an unknown site', { sites: { gemini_web: true } }],
    ['a non-boolean site', { sites: { claude_web: 1 } }],
    ['a non-object', 'enabled'],
    ['an array', []],
  ])('rejects %s and changes nothing', (_name, patch) => {
    const r = parseSettingsPatch(patch);
    expect(r.ok).toBe(false);
  });

  it('rejects the whole patch when one key is bad', () => {
    expect(parseSettingsPatch({ enabled: false, endpoint: 'https://evil.example:1/' }).ok).toBe(false);
  });
});

describe('resolveSettings', () => {
  it('falls back to defaults for stored values that no longer validate', () => {
    const s = resolveSettings({
      enabled: false,
      retention: 'bogus',
      endpoint: 'https://remote.example:443/v1/logs',
      sites: { claude_web: false, chatgpt_web: 'no' },
    });
    expect(s).toEqual({
      ...DEFAULT_SETTINGS,
      enabled: false,
      sites: { claude_web: false, chatgpt_web: true },
    });
  });

  it('returns defaults for nothing stored', () => {
    expect(resolveSettings(undefined)).toEqual(DEFAULT_SETTINGS);
  });
});
