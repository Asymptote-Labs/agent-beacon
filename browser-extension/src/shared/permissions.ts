// Host-permission status and the grant flow shown in the popup and options page.
//
// Chrome grants `host_permissions` at install, so on a default Chrome install
// nothing here is ever missing and the UI stays hidden. Firefox MV3 treats host
// permissions as user-controlled: they can be declined or revoked per site in
// about:addons, and Chrome users can do the same through "Site access". Without
// them the content scripts never inject and the background cannot reach the
// local collector, so the extension would capture nothing, silently. This module
// turns that into a visible "not granted" state with a one-click request.
//
// Pure over an injected API so it is unit-testable without a browser.

/** The subset of the extension API this module needs. */
export interface PermissionsApi {
  // `object` rather than `{ host_permissions?: string[] }` so Chrome's
  // ManifestV2 | ManifestV3 union (and Firefox's equivalent) is assignable.
  runtime: { getManifest(): object };
  permissions: {
    contains(p: { origins: string[] }): Promise<boolean>;
    request(p: { origins: string[] }): Promise<boolean>;
    // Present on every engine; optional so a partial API still renders.
    onAdded?: { addListener(fn: () => void): void };
    onRemoved?: { addListener(fn: () => void): void };
  };
}

/** Every host pattern the manifest declares. They are all load-bearing: the
 *  chat origins for the content scripts, the loopback ones for delivery. */
export function declaredOrigins(api: PermissionsApi): string[] {
  const manifest = api.runtime.getManifest() as { host_permissions?: unknown };
  const hosts = manifest.host_permissions;
  return Array.isArray(hosts) ? hosts.filter((h): h is string => typeof h === 'string') : [];
}

/**
 * Host patterns declared in the manifest but not currently granted, in manifest
 * order. Checked one origin at a time so the UI can say which sites are missing
 * rather than only that something is.
 *
 * A `contains` call that throws counts that origin as missing: showing a grant
 * button that turns out to be unnecessary is harmless, while hiding a real gap
 * is the silent-capture failure this exists to prevent.
 */
export async function missingOrigins(api: PermissionsApi): Promise<string[]> {
  const origins = declaredOrigins(api);
  const granted = await Promise.all(
    origins.map((origin) =>
      Promise.resolve()
        .then(() => api.permissions.contains({ origins: [origin] }))
        .catch(() => false),
    ),
  );
  return origins.filter((_, i) => !granted[i]);
}

/** Human-readable host for a match pattern, for the "not granted" list. */
export function describeOrigin(pattern: string): string {
  const m = /^[^:]+:\/\/([^/]+)\//.exec(pattern);
  return m ? m[1] : pattern;
}

/**
 * Wire a permission banner in a page that has `#perm-missing` (container),
 * `#perm-list` (text) and `#perm-grant` (button). Hidden when everything is
 * granted. Shared by the popup and the options page.
 *
 * The request is started synchronously inside the click handler: Firefox only
 * honours `permissions.request` while the user gesture is still active, so any
 * `await` before it (such as re-checking what is missing) would make Firefox
 * reject the prompt. It asks for the origins found missing at the last render
 * and then re-renders from a fresh check, never from the prompt's answer.
 */
export function mountPermissionBanner(api: PermissionsApi, doc: Document): void {
  const box = doc.getElementById('perm-missing');
  const list = doc.getElementById('perm-list');
  const grant = doc.getElementById('perm-grant') as HTMLButtonElement | null;
  if (!box || !list || !grant) return;

  // Last rendered state, so the click handler can request exactly the missing
  // origins without awaiting a fresh check first (see the gesture note above).
  let lastMissing: string[] = [];
  const render = (missing: string[]): void => {
    lastMissing = missing;
    box.hidden = missing.length === 0;
    // Records that a check has completed at least once, so "hidden" can be
    // told apart from "not checked yet" (the markup starts hidden).
    box.dataset.state = missing.length === 0 ? 'granted' : 'missing';
    list.textContent = [...new Set(missing.map(describeOrigin))].join(', ');
  };

  grant.addEventListener('click', () => {
    let pending: Promise<unknown>;
    try {
      const origins = lastMissing.length > 0 ? lastMissing : declaredOrigins(api);
      pending = api.permissions.request({ origins });
    } catch {
      pending = Promise.resolve();
    }
    void pending
      .catch(() => undefined)
      .then(() => missingOrigins(api))
      .then(render);
  });

  // Access can also change outside this page (about:addons, Chrome's Site
  // access menu) while it stays open, notably the options tab.
  const recheck = (): void => void missingOrigins(api).then(render);
  api.permissions.onAdded?.addListener(recheck);
  api.permissions.onRemoved?.addListener(recheck);

  recheck();
}
