import * as esbuild from 'esbuild';
import { cp, mkdir, rm } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const src = path.join(__dirname, 'src');
const dist = path.join(__dirname, 'dist');
const watch = process.argv.includes('--watch');

// Each extension surface is its own bundle. Content scripts and the MAIN-world
// interceptor must be classic scripts (no ESM import in those worlds), so we
// bundle everything into a single IIFE per entry.
const entries = {
  'sw': 'background/sw.ts',
  'content': 'content/content.ts',
  'interceptor': 'interceptor/interceptor.ts',
  'popup': 'popup/popup.ts',
  'options': 'options/options.ts',
};

/** Copy static assets (manifest, html, icons) into dist/. */
async function copyStatic() {
  await cp(path.join(src, 'manifest.json'), path.join(dist, 'manifest.json'));
  for (const surface of ['popup', 'options']) {
    await cp(path.join(src, surface, `${surface}.html`), path.join(dist, `${surface}.html`));
  }
  // icons/ is optional; copy if present.
  await cp(path.join(src, 'icons'), path.join(dist, 'icons'), { recursive: true }).catch(() => {});
}

const buildOptions = {
  entryPoints: Object.fromEntries(
    Object.entries(entries).map(([out, rel]) => [out, path.join(src, rel)]),
  ),
  outdir: dist,
  bundle: true,
  format: 'iife',
  target: ['chrome120'],
  sourcemap: true,
  logLevel: 'info',
};

async function run() {
  await rm(dist, { recursive: true, force: true });
  await mkdir(dist, { recursive: true });

  if (watch) {
    const ctx = await esbuild.context(buildOptions);
    await copyStatic();
    await ctx.watch();
    console.log('[esbuild] watching…');
  } else {
    await esbuild.build(buildOptions);
    await copyStatic();
    console.log('[esbuild] build complete → dist/');
  }
}

run().catch((err) => {
  console.error(err);
  process.exit(1);
});
