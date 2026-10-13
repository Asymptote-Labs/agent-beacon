import { test, expect, chromium, type BrowserContext } from '@playwright/test';
import { spawn } from 'node:child_process';
import { cpSync, existsSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync, mkdirSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { resolveBrowserTarget } from './browser-target.js';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

test('watch build refreshes static assets consumed by the real extension', async () => {
  test.setTimeout(60_000);
  const project = mkdtempSync(path.join(tmpdir(), 'beacon-static-watch-'));
  for (const name of ['src', 'tools', 'esbuild.config.mjs', 'package.json']) {
    cpSync(path.join(root, name), path.join(project, name), { recursive: true });
  }
  symlinkSync(path.join(root, 'node_modules'), path.join(project, 'node_modules'), 'dir');
  const popup = path.join(project, 'src/popup/popup.html');
  const icons = path.join(project, 'src/icons');
  mkdirSync(icons, { recursive: true });
  const svg = (width: number) => `<svg xmlns="http://www.w3.org/2000/svg" width="${width}" height="2"><rect width="${width}" height="2" fill="blue"/></svg>`;
  writeFileSync(path.join(icons, 'probe.svg'), svg(3));
  writeFileSync(popup, readFileSync(popup, 'utf8').replace('</body>', '<img id="watch-probe" src="icons/probe.svg"/></body>'));
  const dist = path.join(project, 'dist');
  const worker = spawn(process.execPath, ['esbuild.config.mjs', '--watch'], { cwd: project });
  const stopped = new Promise<void>((resolve) => worker.once('exit', () => resolve()));
  let output = '';
  worker.stdout.on('data', (chunk) => { output += chunk; });
  worker.stderr.on('data', (chunk) => { output += chunk; });
  let context: BrowserContext | undefined;
  const open = async () => {
    context = await chromium.launchPersistentContext('', {
      ...resolveBrowserTarget(process.env).launch,
      headless: true,
      args: [`--disable-extensions-except=${dist}`, `--load-extension=${dist}`],
    });
    const sw = context.serviceWorkers()[0] ?? await context.waitForEvent('serviceworker');
    const page = await context.newPage();
    await page.goto(`chrome-extension://${new URL(sw.url()).host}/popup.html`);
    return { page, sw };
  };
  try {
    await expect.poll(() => output).toContain('[esbuild] watching');
    await expect.poll(() => { try { return readFileSync(path.join(dist, 'sw.js')).length; } catch { return 0; } }).toBeGreaterThan(0);
    const { page, sw } = await open();
    await expect(page.locator('h1')).toHaveText('Agent Beacon — Browser Collector');
    await expect.poll(() => page.locator('#watch-probe').evaluate((img) => (img as HTMLImageElement).naturalWidth)).toBe(3);

    writeFileSync(popup, readFileSync(popup, 'utf8').replace('Agent Beacon — Browser Collector', 'Updated collector popup'));
    await expect.poll(() => readFileSync(path.join(dist, 'popup.html'), 'utf8')).toContain('Updated collector popup');
    await page.reload();
    await expect(page.locator('h1')).toHaveText('Updated collector popup');

    const optionsPath = path.join(project, 'src/options/options.html');
    writeFileSync(optionsPath, readFileSync(optionsPath, 'utf8').replace('Agent Beacon — Browser Collector', 'Updated collector options'));
    await expect.poll(() => readFileSync(path.join(dist, 'options.html'), 'utf8')).toContain('Updated collector options');
    const options = await context!.newPage();
    await options.goto(`chrome-extension://${new URL(sw.url()).host}/options.html`);
    await expect(options.locator('h1')).toHaveText('Updated collector options');

    writeFileSync(path.join(icons, 'probe.svg'), svg(5));
    await expect.poll(() => readFileSync(path.join(dist, 'icons/probe.svg'), 'utf8')).toContain('width="5"');
    await page.reload();
    await expect.poll(() => page.locator('#watch-probe').evaluate((img) => (img as HTMLImageElement).naturalWidth)).toBe(5);

    rmSync(path.join(icons, 'probe.svg'));
    await expect.poll(() => existsSync(path.join(dist, 'icons/probe.svg'))).toBe(false);
    await page.reload();
    await expect.poll(() => page.locator('#watch-probe').evaluate((img) =>
      (img as HTMLImageElement).complete && (img as HTMLImageElement).naturalWidth === 0)).toBe(true);

    const manifestPath = path.join(project, 'src/manifest.json');
    const manifest = JSON.parse(readFileSync(manifestPath, 'utf8'));
    manifest.version = '0.1.1';
    writeFileSync(manifestPath, JSON.stringify(manifest));
    await expect.poll(() => JSON.parse(readFileSync(path.join(dist, 'manifest.json'), 'utf8')).version).toBe('0.1.1');
    await context!.close();
    context = undefined;
    const reloaded = await open();
    expect(await reloaded.sw.evaluate(() => chrome.runtime.getManifest().version)).toBe('0.1.1');
    await expect(reloaded.page.locator('h1')).toHaveText('Updated collector popup');
  } finally {
    await context?.close();
    worker.kill('SIGTERM');
    await stopped;
    rmSync(project, { recursive: true, force: true });
  }
});
