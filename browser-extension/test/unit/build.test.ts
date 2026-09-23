// Runs the real build (esbuild.config.mjs) for each target into a temp dir. The
// Chrome output must be what it always was; the Firefox output must be complete.
import { describe, it, expect, beforeAll, afterAll } from 'vitest';
import { execFileSync } from 'node:child_process';
import { existsSync, mkdtempSync, readFileSync, readdirSync, rmSync, statSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { GECKO_ID } from '../../tools/targets.mjs';

const root = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..');
const FILES = ['manifest.json', 'sw.js', 'content.js', 'interceptor.js', 'popup.html', 'popup.js', 'options.html', 'options.js'];

let work: string;
const out = (t: string) => path.join(work, t);

beforeAll(() => {
  work = mkdtempSync(path.join(tmpdir(), 'beacon-ext-build-'));
  for (const t of ['chrome', 'firefox']) {
    execFileSync(process.execPath, ['esbuild.config.mjs', '--target', t, '--outdir', out(t)], {
      cwd: root,
      stdio: 'pipe',
    });
  }
}, 60_000);

afterAll(() => {
  rmSync(work, { recursive: true, force: true });
});

describe.each(['chrome', 'firefox'])('%s build', (t) => {
  it.each(FILES)('emits a non-empty %s', (f) => {
    const p = path.join(out(t), f);
    expect(existsSync(p)).toBe(true);
    expect(statSync(p).size).toBeGreaterThan(0);
  });
});

describe('chrome build', () => {
  it('ships src/manifest.json byte for byte', () => {
    expect(readFileSync(path.join(out('chrome'), 'manifest.json'))).toEqual(
      readFileSync(path.join(root, 'src', 'manifest.json')),
    );
  });

  it('emits the same file set as before per-browser targets existed', () => {
    const got = readdirSync(out('chrome')).filter((f) => !f.endsWith('.map')).sort();
    expect(got).toEqual([...FILES].sort());
  });
});

describe('firefox build', () => {
  it('writes the derived Gecko manifest', () => {
    const m = JSON.parse(readFileSync(path.join(out('firefox'), 'manifest.json'), 'utf8'));
    expect(m.background).toEqual({ scripts: ['sw.js'] });
    expect(m.browser_specific_settings.gecko.id).toBe(GECKO_ID);
    const base = JSON.parse(readFileSync(path.join(root, 'src', 'manifest.json'), 'utf8'));
    expect(m.version).toBe(base.version);
  });

  it('bundles the popup with the host-permission banner wired in', () => {
    expect(readFileSync(path.join(out('firefox'), 'popup.js'), 'utf8')).toContain('perm-grant');
    expect(readFileSync(path.join(out('firefox'), 'popup.html'), 'utf8')).toContain('id="perm-grant"');
    expect(readFileSync(path.join(out('firefox'), 'options.html'), 'utf8')).toContain('id="perm-grant"');
  });
});

describe('build CLI', () => {
  it('fails on an unknown target instead of silently building Chrome', () => {
    expect(() =>
      execFileSync(process.execPath, ['esbuild.config.mjs', '--target', 'nope', '--outdir', out('nope')], {
        cwd: root,
        stdio: 'pipe',
      }),
    ).toThrow(/unknown build target "nope"/);
    expect(existsSync(out('nope'))).toBe(false);
  });
});
