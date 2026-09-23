import path from 'node:path';

// Per-browser build targets. `esbuild.config.mjs` builds one target at a time
// from this table; everything that differs between browsers lives here.
//
// `src/manifest.json` stays the single source of truth (it is also what the
// release workflow reads the version from). A target either ships it verbatim
// (`manifest: null`, Chrome) or derives its own from it with a pure function.
// Adding a browser, Safari for example, is a new entry here plus its test cases
// in test/unit/targets.test.ts; nothing in esbuild.config.mjs changes.
//
// Plain JavaScript because `node esbuild.config.mjs` imports it directly and
// Node 22 does not strip TypeScript by default on every minor. Types for the
// unit tests live in targets.d.mts.

/**
 * Pinned Gecko add-on ID. Firefox keys `storage.local` and AMO signing on it, so
 * it must never change once a build has been signed: a new ID is a new add-on,
 * and every existing install would lose its settings and delivery queue.
 */
export const GECKO_ID = 'browser-collector@agent-beacon.asymptotelabs.ai';

/**
 * Oldest Firefox the Firefox build supports. 140 is the current ESR, which is
 * what managed fleets run, and the first release that understands
 * `data_collection_permissions`. It is also well past 128, the first release
 * that honours `content_scripts[].world: "MAIN"`, which the fetch interceptor
 * depends on (see MIN_FIREFOX_FOR_MAIN_WORLD).
 */
export const FIREFOX_MIN_VERSION = '140.0';

/**
 * Firefox for Android shipped `data_collection_permissions` two releases later
 * than desktop. Declaring it keeps `web-ext lint` clean (it warns when the
 * desktop minimum predates Android support for a key the manifest uses). The
 * extension is built and tested for desktop; this is not a claim of Android
 * support beyond what the manifest keys allow.
 */
export const FIREFOX_ANDROID_MIN_VERSION = '142.0';

/** First Firefox that runs `world: "MAIN"` manifest content scripts. Below it
 *  the interceptor would silently run in the isolated world and see nothing. */
export const MIN_FIREFOX_FOR_MAIN_WORLD = 128;

/**
 * Firefox's install-time data disclosure (required for new AMO submissions).
 * The extension transmits the chat prompts and responses it captures out of the
 * browser -- to the local Beacon collector, which may forward them onward -- so
 * the honest declaration is personal communications, not "none".
 */
export const FIREFOX_DATA_COLLECTION = Object.freeze({
  required: ['personalCommunications'],
});

/**
 * Chrome's service worker becomes a Firefox MV3 background script (an event
 * page; Firefox does not run extension service workers), and Firefox gets its
 * add-on ID, minimum version, and data-collection declaration; `options_page`
 * becomes Firefox's `options_ui`. Nothing else changes: the MAIN-world content script, host permissions, popup and options
 * page carry over as they are.
 *
 * @param {Record<string, any>} base parsed src/manifest.json (not mutated)
 * @returns {Record<string, any>}
 */
export function firefoxManifest(base) {
  const m = structuredClone(base);
  const worker = m.background?.service_worker;
  if (typeof worker !== 'string' || worker === '') {
    throw new Error('firefoxManifest: base manifest has no background.service_worker to convert');
  }
  // The bundles are IIFEs (see esbuild.config.mjs), so a classic script is
  // right; `type: "module"` would be wrong for them.
  m.background = { scripts: [worker] };
  // Firefox documents `options_ui` as its options key; `options_page` is the
  // Chrome spelling. Open in a tab, matching how Chrome shows options_page.
  if (typeof m.options_page === 'string') {
    m.options_ui = { page: m.options_page, open_in_tab: true };
    delete m.options_page;
  }
  m.browser_specific_settings = {
    gecko: {
      id: GECKO_ID,
      strict_min_version: FIREFOX_MIN_VERSION,
      data_collection_permissions: structuredClone(FIREFOX_DATA_COLLECTION),
    },
    gecko_android: {
      strict_min_version: FIREFOX_ANDROID_MIN_VERSION,
    },
  };
  return m;
}

/** Oldest Safari the Safari build supports: the first release documented to run
 *  `content_scripts[].world: "MAIN"`, which the fetch interceptor depends on. */
export const SAFARI_MIN_VERSION = 18;

/**
 * @typedef {object} BuildTarget
 * @property {string} name       target id, as passed to `--target`
 * @property {string} outdir     output directory, relative to browser-extension/
 * @property {string[]} esbuildTarget  esbuild `target` list
 * @property {((base: Record<string, any>) => Record<string, any>) | null} manifest
 *   derives the target's manifest, or null to copy src/manifest.json byte for byte
 */

/** @type {Readonly<Record<string, BuildTarget>>} */
export const TARGETS = Object.freeze({
  // Unchanged from before targets existed: same output directory, same esbuild
  // target, manifest copied verbatim. CI, the e2e and the release workflow all
  // read dist/.
  chrome: {
    name: 'chrome',
    outdir: 'dist',
    esbuildTarget: ['chrome120'],
    manifest: null,
  },
  firefox: {
    name: 'firefox',
    outdir: 'dist-firefox',
    esbuildTarget: [`firefox${parseInt(FIREFOX_MIN_VERSION, 10)}`],
    manifest: firefoxManifest,
  },
  // Safari documents every key src/manifest.json uses, including the
  // `background.service_worker` and the MAIN-world content script (Safari 18),
  // so the manifest ships verbatim and only the esbuild target differs. The
  // output is not loadable on its own: packaging/macos/safari wraps it in the
  // macOS app Safari requires. If the service worker turns out to be unable to
  // reach the collector (see packaging/macos/safari/compatibility.md), this is
  // where a derived manifest with `background.scripts` would go.
  safari: {
    name: 'safari',
    outdir: 'dist-safari',
    esbuildTarget: [`safari${SAFARI_MIN_VERSION}`],
    manifest: null,
  },
});

export const DEFAULT_TARGET = 'chrome';

/**
 * @param {string} name
 * @returns {BuildTarget}
 */
export function getTarget(name) {
  const t = Object.hasOwn(TARGETS, name) ? TARGETS[name] : undefined;
  if (!t) {
    throw new Error(`unknown build target "${name}"; expected one of: ${Object.keys(TARGETS).join(', ')}`);
  }
  return t;
}

/**
 * Throw if `outdir` (which the build deletes first) is the project directory, an
 * ancestor of it, or inside `srcDir`. All paths absolute.
 *
 * @param {string} projectDir
 * @param {string} srcDir
 * @param {string} outdir
 */
export function assertSafeOutdir(projectDir, srcDir, outdir) {
  const within = (child, parent) => {
    const rel = path.relative(parent, child);
    return rel === '' || (!rel.startsWith('..') && !path.isAbsolute(rel));
  };
  if (within(projectDir, outdir) || within(outdir, srcDir)) {
    throw new Error(`refusing to build into ${outdir}: the build deletes it first, and it holds project sources`);
  }
}
