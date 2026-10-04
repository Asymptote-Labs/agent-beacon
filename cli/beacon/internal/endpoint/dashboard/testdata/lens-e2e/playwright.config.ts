import { mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { defineConfig } from '@playwright/test';

/**
 * Runs the real dashboard handler (../../e2eserver) with the hostile fixture lenses and drives the
 * session page in Chromium. HOME is a fresh directory so the rule store is empty and the embedded
 * baseline rules run, and no opt-in trace history exists.
 */
const port = Number(process.env.LENS_E2E_PORT || 8798);
const lenses = ['probe', 'navigator', 'thrower', 'staller', 'tall']
  .map((id) => `--lens fixtures/${id}.lens.html`)
  .join(' ');

export default defineConfig({
  testDir: './tests',
  forbidOnly: !!process.env.CI,
  retries: 0,
  workers: 1,
  timeout: 30_000,
  reporter: [['list'], ['html', { open: 'never' }]],
  use: {
    baseURL: `http://127.0.0.1:${port}`,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    launchOptions: process.env.CHROMIUM_PATH ? { executablePath: process.env.CHROMIUM_PATH } : {},
  },
  webServer: {
    command: `go run ../../e2eserver --addr 127.0.0.1:${port} --log fixtures/runtime.jsonl ${lenses}`,
    url: `http://127.0.0.1:${port}/api/lenses`,
    timeout: 180_000,
    reuseExistingServer: false,
    env: { HOME: mkdtempSync(join(tmpdir(), 'beacon-lens-e2e-')) },
  },
});
