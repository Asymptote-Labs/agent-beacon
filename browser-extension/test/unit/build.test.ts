// Runs the real build (esbuild.config.mjs) for each target into a temp dir. The
// Chrome output must be what it always was; the Firefox and Safari outputs must be complete.
import { describe, it, expect, beforeAll, afterAll } from 'vitest';
import { execFileSync } from 'node:child_process';
import { mkdtempSync, readFileSync, readdirSync, rmSync } from 'node:fs';
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
  for (const t of ['chrome', 'firefox', 'safari']) {
    execFileSync(process.execPath, ['esbuild.config.mjs', '--target', t, '--outdir', out(t)], {
      cwd: root,
      stdio: 'pipe',
    });
  }
}, 60_000);

afterAll(() => {
  rmSync(work, { recursive: true, force: true });
});

describe.each(['chrome', 'firefox', 'safari'])('%s build', (t) => {
  it('emits exactly the extension files', () => {
    const got = readdirSync(out(t)).filter((f) => !f.endsWith('.map')).sort();
    expect(got).toEqual([...FILES].sort());
  });
});

describe.each(['chrome', 'safari'])('%s build', (t) => {
  it('ships src/manifest.json byte for byte', () => {
    expect(readFileSync(path.join(out(t), 'manifest.json'))).toEqual(
      readFileSync(path.join(root, 'src', 'manifest.json')),
    );
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
});
