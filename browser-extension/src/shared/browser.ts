// WebExtension namespace shim. Every extension surface (background, content,
// popup, options) reaches the browser through `ext`, never through the
// `chrome` or `browser` globals directly.
//
// Why: Firefox and Safari expose a promise-based `browser.*` namespace and keep
// `chrome.*` only as a callback-style alias, while Chrome exposes `chrome.*`
// alone (promise-returning in MV3). The code awaits these calls, so relying on
// `chrome.*` returning promises is not portable. Picking `browser` when it is a
// real extension namespace, and `chrome` otherwise, gives every engine the
// promise-returning object without a polyfill or per-call feature checks.
//
// Scope is deliberately small: only the APIs this extension uses (storage,
// runtime, alarms, permissions), which have the same shape on all three engines.
// The type is Chrome's, because @types/chrome describes the promise overloads we
// call and Firefox/Safari match them for this subset.

export type ExtensionApi = typeof chrome;

/**
 * Pick the extension namespace from a global object. Exported for unit tests;
 * callers use `ext`.
 *
 * `browser` is only trusted when it carries `runtime.id`. A page element with
 * `id="browser"` is reachable as `window.browser` through named property access,
 * including from an isolated-world content script, so a bare truthiness check
 * could hand the extension a DOM node on Chrome.
 */
export function resolveExtensionApi(globals: {
  browser?: unknown;
  chrome?: unknown;
}): ExtensionApi | undefined {
  if (isExtensionNamespace(globals.browser)) return globals.browser as ExtensionApi;
  if (globals.chrome != null && typeof globals.chrome === 'object') {
    return globals.chrome as ExtensionApi;
  }
  return undefined;
}

function isExtensionNamespace(value: unknown): boolean {
  if (value == null || typeof value !== 'object') return false;
  const runtime = (value as { runtime?: { id?: unknown } }).runtime;
  return runtime != null && typeof runtime.id === 'string' && runtime.id.length > 0;
}

/**
 * The extension API for the current engine. Always defined inside an extension
 * context; the cast keeps call sites free of null checks that would never fire.
 */
export const ext: ExtensionApi = resolveExtensionApi(
  globalThis as { browser?: unknown; chrome?: unknown },
) as ExtensionApi;
