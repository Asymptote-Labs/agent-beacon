import * as esbuild from 'esbuild';
import { cp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { parseArgs } from 'node:util';
import { DEFAULT_TARGET, assertSafeOutdir, getTarget } from './tools/targets.mjs';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const src = path.join(__dirname, 'src');

// `--target chrome` (default) writes dist/, exactly as before per-browser targets
// existed. `--target firefox` writes dist-firefox/. See tools/targets.mjs.
// `--outdir` overrides the output directory (the unit tests build into a temp dir).
const { values: args } = parseArgs({
  options: {
    watch: { type: 'boolean', default: false },
    target: { type: 'string', default: DEFAULT_TARGET },
    outdir: { type: 'string' },
  },
});
const target = getTarget(args.target);
const dist = path.resolve(__dirname, args.outdir ?? target.outdir);
// The output directory is deleted before every build; refuse one that would
// take project sources with it.
assertSafeOutdir(__dirname, src, dist);
const watch = args.watch;

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

/** Write the target's manifest: verbatim for Chrome, derived for the others. */
async function writeManifest() {
  const source = path.join(src, 'manifest.json');
  if (target.manifest == null) {
    await cp(source, path.join(dist, 'manifest.json'));
    return;
  }
  const base = JSON.parse(await readFile(source, 'utf8'));
  const derived = target.manifest(base);
  await writeFile(path.join(dist, 'manifest.json'), JSON.stringify(derived, null, 2) + '\n');
}

/** Copy static assets (manifest, html, icons) into the output directory. */
async function copyStatic() {
  await writeManifest();
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
  target: target.esbuildTarget,
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
    console.log(`[esbuild] ${target.name} build complete → ${path.relative(__dirname, dist) || '.'}/`);
  }
}

run().catch((err) => {
  console.error(err);
  process.exit(1);
});
