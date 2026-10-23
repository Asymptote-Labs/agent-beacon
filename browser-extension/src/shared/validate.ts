// Shape and size checks for everything that crosses a trust boundary into the
// service worker: intercept events relayed from a chat-site renderer, and
// settings patches. Pure, so it is unit-tested without a browser.

import type { InterceptEvent, Retention, Settings, SiteName } from './types.js';

/** Request ids are random (crypto.randomUUID in the interceptor). */
const REQ_ID = /^[A-Za-z0-9-]{8,64}$/;

export const LIMITS = Object.freeze({
  url: 8 * 1024,
  method: 16,
  /** Request bodies carry the whole conversation on some sites. */
  body: 8 * 1024 * 1024,
  chunk: 4 * 1024 * 1024,
  message: 4 * 1024,
  host: 253,
});

function str(v: unknown, max: number): v is string {
  return typeof v === 'string' && v.length <= max;
}

/**
 * Return `raw` as an InterceptEvent when it has exactly the expected shape and
 * sizes, or null. Unknown kinds, wrong types, oversized strings and malformed
 * request ids are all rejected. The result is a fresh object holding only the
 * known fields.
 */
export function parseInterceptEvent(raw: unknown): InterceptEvent | null {
  if (raw == null || typeof raw !== 'object') return null;
  const e = raw as Record<string, unknown>;
  if (typeof e.reqId !== 'string' || !REQ_ID.test(e.reqId)) return null;
  const reqId = e.reqId;
  switch (e.kind) {
    case 'request':
      if (!str(e.url, LIMITS.url) || !str(e.method, LIMITS.method)) return null;
      if (e.body !== null && !str(e.body, LIMITS.body)) return null;
      return { kind: 'request', reqId, url: e.url, method: e.method, body: e.body };
    case 'chunk':
      if (!str(e.chunk, LIMITS.chunk)) return null;
      return { kind: 'chunk', reqId, chunk: e.chunk };
    case 'done':
      return { kind: 'done', reqId };
    case 'error':
      if (!str(e.message, LIMITS.message)) return null;
      return { kind: 'error', reqId, message: e.message };
    default:
      return null;
  }
}

/** A plausible host name (with optional port), or null. */
export function parseHost(raw: unknown): string | null {
  if (!str(raw, LIMITS.host) || raw === '') return null;
  const match = /^([A-Za-z0-9.-]+)(?::(\d{1,5}))?$/.exec(raw);
  if (!match) return null;
  if (match[2] !== undefined) {
    const port = Number(match[2]);
    // Five digits includes 65536–99999, which are not TCP ports. A chat host
    // carrying one of those never matches claude.ai or chatgpt.com, so the
    // intercept is silently dropped.
    if (!Number.isInteger(port) || port > 65535) return null;
  }
  return raw;
}

const LOOPBACK_HOSTS = new Set(['127.0.0.1', 'localhost', '[::1]']);

/**
 * The only endpoints the extension may deliver to: http(s) on loopback, with
 * no credentials. A URL without a port uses the scheme's default port, which
 * is still loopback; the WHATWG parser reports an explicit default port
 * (:80 on http, :443 on https) as no port at all, so requiring one would
 * refuse those spellings. Chat content never leaves the machine from
 * the browser; anything beyond it is the local collector's job.
 */
export function isAllowedEndpoint(raw: unknown): raw is string {
  if (!str(raw, LIMITS.url)) return false;
  let u: URL;
  try {
    u = new URL(raw);
  } catch {
    return false;
  }
  if (u.protocol !== 'http:' && u.protocol !== 'https:') return false;
  if (!LOOPBACK_HOSTS.has(u.hostname)) return false;
  if (u.username !== '' || u.password !== '') return false;
  return true;
}

const RETENTIONS: readonly Retention[] = ['metadata', 'redacted', 'full'];
const SITES: readonly SiteName[] = ['claude_web', 'chatgpt_web'];

export type PatchResult = { ok: true; patch: Partial<Settings> } | { ok: false; error: string };

/**
 * Validate a settings patch from the popup or options page. Only known keys
 * with valid values are accepted; anything else rejects the whole patch, so a
 * bad save changes nothing.
 */
export function parseSettingsPatch(raw: unknown): PatchResult {
  if (raw == null || typeof raw !== 'object' || Array.isArray(raw)) {
    return { ok: false, error: 'settings patch must be an object' };
  }
  const out: Partial<Settings> = {};
  for (const [key, value] of Object.entries(raw as Record<string, unknown>)) {
    switch (key) {
      case 'enabled':
        if (typeof value !== 'boolean') return { ok: false, error: 'enabled must be true or false' };
        out.enabled = value;
        break;
      case 'retention':
        if (!RETENTIONS.includes(value as Retention)) {
          return { ok: false, error: `retention must be one of ${RETENTIONS.join(', ')}` };
        }
        out.retention = value as Retention;
        break;
      case 'endpoint':
        if (!isAllowedEndpoint(value)) {
          return {
            ok: false,
            error:
              'endpoint must be an http(s) URL on 127.0.0.1, localhost or [::1], such as http://127.0.0.1:4318/v1/logs',
          };
        }
        out.endpoint = value;
        break;
      case 'sites': {
        if (value == null || typeof value !== 'object' || Array.isArray(value)) {
          return { ok: false, error: 'sites must be an object' };
        }
        const sites: Partial<Record<SiteName, boolean>> = {};
        for (const [site, on] of Object.entries(value as Record<string, unknown>)) {
          if (!SITES.includes(site as SiteName)) return { ok: false, error: `unknown site ${site}` };
          if (typeof on !== 'boolean') return { ok: false, error: `sites.${site} must be true or false` };
          sites[site as SiteName] = on;
        }
        out.sites = sites as Record<SiteName, boolean>;
        break;
      }
      default:
        return { ok: false, error: `unknown setting ${key}` };
    }
  }
  return { ok: true, patch: out };
}
