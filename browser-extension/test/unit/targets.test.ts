// Per-target manifest generation (tools/targets.mjs). Chrome must stay exactly
// what it was; Firefox must get the keys Gecko requires and keep everything the
// capture path depends on.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import {
  DEFAULT_TARGET,
  assertSafeOutdir,
  FIREFOX_MIN_VERSION,
  GECKO_ID,
  MIN_FIREFOX_FOR_MAIN_WORLD,
  SAFARI_MIN_VERSION,
  TARGETS,
  firefoxManifest,
  getTarget,
} from '../../tools/targets.mjs';

const root = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..');
const base = (): Record<string, any> =>
  JSON.parse(readFileSync(path.join(root, 'src', 'manifest.json'), 'utf8'));

describe('build targets', () => {
  it('keeps Chrome the default target, built to dist/ and copied verbatim', () => {
    expect(DEFAULT_TARGET).toBe('chrome');
    const chrome = getTarget('chrome');
    expect(chrome.outdir).toBe('dist');
    expect(chrome.esbuildTarget).toEqual(['chrome120']);
    expect(chrome.manifest).toBeNull();
  });

  it('gives every target its own output directory', () => {
    const outdirs = Object.values(TARGETS).map((t) => t.outdir);
    expect(new Set(outdirs).size).toBe(outdirs.length);
    for (const [name, t] of Object.entries(TARGETS)) expect(t.name).toBe(name);
  });

  it('rejects an unknown target by name', () => {
    expect(() => getTarget('opera')).toThrow(/unknown build target "opera".*chrome, firefox, safari/);
    expect(() => getTarget('toString')).toThrow(/unknown build target/);
  });

  it('builds Firefox to dist-firefox with an esbuild target at the manifest minimum', () => {
    const ff = getTarget('firefox');
    expect(ff.outdir).toBe('dist-firefox');
    expect(ff.esbuildTarget).toEqual([`firefox${parseInt(FIREFOX_MIN_VERSION, 10)}`]);
  });
});

describe('safari target', () => {
  it('builds to dist-safari and ships src/manifest.json verbatim', () => {
    const safari = getTarget('safari');
    expect(safari.outdir).toBe('dist-safari');
    expect(safari.manifest).toBeNull();
  });

  it('targets a Safari that runs world: "MAIN" content scripts (Safari 18)', () => {
    expect(SAFARI_MIN_VERSION).toBeGreaterThanOrEqual(18);
    expect(getTarget('safari').esbuildTarget).toEqual([`safari${SAFARI_MIN_VERSION}`]);
    const usesMain = base().content_scripts.some((c: { world?: string }) => c.world === 'MAIN');
    expect(usesMain).toBe(true);
  });

  it('keeps the Chrome background shape, which Safari documents as supported', () => {
    // Verbatim means the service worker ships as is. If Safari needs
    // background.scripts instead, the target must gain a derived manifest.
    expect(base().background).toEqual({ service_worker: 'sw.js' });
  });
});

describe('firefoxManifest', () => {
  it('replaces the service worker with a background script (Firefox runs event pages)', () => {
    const m = firefoxManifest(base());
    expect(m.background).toEqual({ scripts: ['sw.js'] });
    expect(m.background.service_worker).toBeUndefined();
    expect(m.background.type).toBeUndefined(); // bundles are IIFEs, not modules
  });

  it('pins the Gecko add-on ID and a minimum version', () => {
    const g = firefoxManifest(base()).browser_specific_settings.gecko;
    expect(g.id).toBe(GECKO_ID);
    // Changing this ID orphans every install's storage and breaks AMO signing
    // continuity; this assertion is deliberately a literal.
    expect(g.id).toBe('browser-collector@agent-beacon.asymptotelabs.ai');
    expect(g.id).toMatch(/^[a-zA-Z0-9-._]*@[a-zA-Z0-9-._]+$/); // Gecko's ID grammar
    expect(g.strict_min_version).toBe(FIREFOX_MIN_VERSION);
  });

  it('requires a Firefox that honours world: "MAIN" when a content script uses it', () => {
    const m = firefoxManifest(base());
    const usesMain = m.content_scripts.some((c: { world?: string }) => c.world === 'MAIN');
    expect(usesMain).toBe(true);
    const min = parseInt(m.browser_specific_settings.gecko.strict_min_version, 10);
    expect(min).toBeGreaterThanOrEqual(MIN_FIREFOX_FOR_MAIN_WORLD);
  });

  it('declares data collection honestly: chat content leaves the browser', () => {
    const g = firefoxManifest(base()).browser_specific_settings.gecko;
    expect(g.data_collection_permissions.required).toEqual(['personalCommunications']);
    expect(g.data_collection_permissions.required).not.toContain('none');
  });

  it('carries over everything else unchanged, including the MAIN-world interceptor', () => {
    const b = base();
    const m = firefoxManifest(b);
    const { background: _b1, options_page: _o1, ...restBase } = b;
    const { background: _b2, browser_specific_settings: _s, options_ui: _o2, ...restFf } = m;
    expect(restFf).toEqual(restBase);
    expect(m.content_scripts[0]).toMatchObject({ js: ['interceptor.js'], world: 'MAIN', run_at: 'document_start' });
    expect(m.version).toBe(b.version);
    expect(m.manifest_version).toBe(3);
  });

  it("uses Firefox's options_ui in place of Chrome's options_page", () => {
    const m = firefoxManifest(base());
    expect(m.options_page).toBeUndefined();
    expect(m.options_ui).toEqual({ page: 'options.html', open_in_tab: true });
  });

  it('does not mutate the base manifest', () => {
    const b = base();
    const snapshot = structuredClone(b);
    firefoxManifest(b);
    expect(b).toEqual(snapshot);
  });

  it('refuses a base manifest with no service worker to convert', () => {
    const b = base();
    delete b.background;
    expect(() => firefoxManifest(b)).toThrow(/no background.service_worker/);
  });
});

describe('assertSafeOutdir', () => {
  // Pure path checks; nothing is built or deleted here.
  const project = '/repo/browser-extension';
  const src = '/repo/browser-extension/src';

  it.each(['/repo/browser-extension/dist', '/repo/browser-extension/dist-firefox', '/tmp/beacon-build'])(
    'allows %s',
    (out) => {
      expect(() => assertSafeOutdir(project, src, out)).not.toThrow();
    },
  );

  it.each([
    '/repo/browser-extension', // --outdir .
    '/repo', // --outdir ..
    '/', // an ancestor
    '/repo/browser-extension/src', // --outdir src
    '/repo/browser-extension/src/popup', // inside src
  ])('refuses %s, which the build would delete along with sources', (out) => {
    expect(() => assertSafeOutdir(project, src, out)).toThrow(/refusing to build into/);
  });
});
