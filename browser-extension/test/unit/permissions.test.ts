// Host-permission status and grant flow (src/shared/permissions.ts). Firefox MV3
// lets the user withhold host permissions; without them the extension captures
// nothing, so the popup/options must show it and offer the grant.
import { describe, it, expect, vi } from 'vitest';
import {
  declaredOrigins,
  describeOrigin,
  missingOrigins,
  mountPermissionBanner,
  type PermissionsApi,
} from '../../src/shared/permissions.js';

const HOSTS = [
  '*://claude.ai/*',
  '*://chatgpt.com/*',
  '*://chat.openai.com/*',
  'http://127.0.0.1/*',
  'http://localhost/*',
];

function fakeApi(granted: Iterable<string>, opts: { grantOnRequest?: boolean; containsThrows?: boolean } = {}) {
  const have = new Set(granted);
  const request = vi.fn(async ({ origins }: { origins: string[] }) => {
    if (opts.grantOnRequest) origins.forEach((o) => have.add(o));
    return !!opts.grantOnRequest;
  });
  const api: PermissionsApi = {
    runtime: { getManifest: () => ({ host_permissions: HOSTS }) },
    permissions: {
      contains: async ({ origins }) => {
        if (opts.containsThrows) throw new Error('boom');
        return origins.every((o) => have.has(o));
      },
      request,
    },
  };
  return { api, request, have };
}

interface FakeEl {
  hidden: boolean;
  dataset: Record<string, string>;
  textContent: string;
  click?: () => void;
  addEventListener(type: string, fn: () => void): void;
}
function fakeDoc() {
  const mk = (): FakeEl => {
    const el: FakeEl = {
      hidden: true,
      dataset: {},
      textContent: '',
      addEventListener(type, fn) {
        if (type === 'click') el.click = fn;
      },
    };
    return el;
  };
  const els: Record<string, FakeEl> = { 'perm-missing': mk(), 'perm-list': mk(), 'perm-grant': mk() };
  const doc = { getElementById: (id: string) => els[id] ?? null } as unknown as Document;
  return { doc, els };
}

const settle = () => new Promise((r) => setTimeout(r, 0));

describe('missingOrigins', () => {
  it('is empty when every declared host is granted (the Chrome install default)', async () => {
    expect(await missingOrigins(fakeApi(HOSTS).api)).toEqual([]);
  });

  it('lists every host when none are granted (a fresh Firefox install that withheld them)', async () => {
    expect(await missingOrigins(fakeApi([]).api)).toEqual(HOSTS);
  });

  it('lists only the withheld hosts, in manifest order', async () => {
    const { api } = fakeApi(['*://claude.ai/*', 'http://127.0.0.1/*']);
    expect(await missingOrigins(api)).toEqual(['*://chatgpt.com/*', '*://chat.openai.com/*', 'http://localhost/*']);
  });

  it('treats a failing check as missing rather than hiding a gap', async () => {
    expect(await missingOrigins(fakeApi(HOSTS, { containsThrows: true }).api)).toEqual(HOSTS);
  });

  it('treats a synchronously throwing check as missing too', async () => {
    const { api } = fakeApi(HOSTS);
    api.permissions.contains = () => {
      throw new Error('sync');
    };
    expect(await missingOrigins(api)).toEqual(HOSTS);
  });

  it('reads the hosts from the manifest', () => {
    expect(declaredOrigins(fakeApi([]).api)).toEqual(HOSTS);
    const bare: PermissionsApi = { ...fakeApi([]).api, runtime: { getManifest: () => ({}) } };
    expect(declaredOrigins(bare)).toEqual([]);
  });
});

describe('describeOrigin', () => {
  it('reduces a match pattern to its host', () => {
    expect(describeOrigin('*://claude.ai/*')).toBe('claude.ai');
    expect(describeOrigin('http://127.0.0.1/*')).toBe('127.0.0.1');
    expect(describeOrigin('<all_urls>')).toBe('<all_urls>');
  });
});

describe('mountPermissionBanner', () => {
  it('stays hidden when everything is granted', async () => {
    const { doc, els } = fakeDoc();
    mountPermissionBanner(fakeApi(HOSTS).api, doc);
    await settle();
    expect(els['perm-missing'].hidden).toBe(true);
    expect(els['perm-missing'].dataset.state).toBe('granted');
  });

  it('shows the not-granted state naming the missing sites', async () => {
    const { doc, els } = fakeDoc();
    mountPermissionBanner(fakeApi(['*://claude.ai/*']).api, doc);
    await settle();
    expect(els['perm-missing'].hidden).toBe(false);
    expect(els['perm-missing'].dataset.state).toBe('missing');
    expect(els['perm-list'].textContent).toBe('chatgpt.com, chat.openai.com, 127.0.0.1, localhost');
  });

  it('requests exactly the missing hosts synchronously on click, then hides once granted', async () => {
    const { doc, els } = fakeDoc();
    const { api, request } = fakeApi(['*://claude.ai/*'], { grantOnRequest: true });
    mountPermissionBanner(api, doc);
    await settle();
    els['perm-grant'].click!();
    // Called inside the click handler itself: Firefox rejects a request made
    // after the user gesture has been consumed by an await.
    expect(request).toHaveBeenCalledTimes(1);
    expect(request.mock.calls[0][0].origins).toEqual(HOSTS.slice(1));
    await settle();
    expect(els['perm-missing'].hidden).toBe(true);
  });

  it('keeps the banner when the user declines the prompt', async () => {
    const { doc, els } = fakeDoc();
    const { api, request } = fakeApi([]);
    mountPermissionBanner(api, doc);
    await settle();
    els['perm-grant'].click!();
    await settle();
    expect(request).toHaveBeenCalledTimes(1);
    expect(els['perm-missing'].hidden).toBe(false);
  });

  it('survives a request that throws (no gesture, or API refused)', async () => {
    const { doc, els } = fakeDoc();
    const { api } = fakeApi([]);
    api.permissions.request = () => {
      throw new Error('permissions.request may only be called from a user input handler');
    };
    mountPermissionBanner(api, doc);
    await settle();
    expect(() => els['perm-grant'].click!()).not.toThrow();
    await settle();
    expect(els['perm-missing'].hidden).toBe(false);
  });

  it('re-renders when access changes outside the page (about:addons, Site access)', async () => {
    const { doc, els } = fakeDoc();
    const { api, have } = fakeApi([]);
    const listeners: Array<() => void> = [];
    api.permissions.onAdded = { addListener: (fn) => listeners.push(fn) };
    api.permissions.onRemoved = { addListener: (fn) => listeners.push(fn) };
    mountPermissionBanner(api, doc);
    await settle();
    expect(els['perm-missing'].hidden).toBe(false);
    HOSTS.forEach((h) => have.add(h));
    listeners.forEach((fn) => fn());
    await settle();
    expect(els['perm-missing'].hidden).toBe(true);
  });

  it('does nothing on a page without the banner markup', () => {
    const doc = { getElementById: () => null } as unknown as Document;
    expect(() => mountPermissionBanner(fakeApi([]).api, doc)).not.toThrow();
  });
});
