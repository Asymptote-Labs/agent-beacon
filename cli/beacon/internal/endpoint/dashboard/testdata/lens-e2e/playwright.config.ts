import { execFileSync } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { copyFileSync, mkdirSync, mkdtempSync, readdirSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { basename, join, resolve } from 'node:path';
import { defineConfig } from '@playwright/test';

/**
 * Runs the real dashboard handler (../../e2eserver) with the hostile fixture lenses and drives the
 * session page in Chromium. HOME is a fresh directory, so no opt-in trace history exists, and its
 * rule store holds the whole open corpus (rules/) rather than the six-rule embedded baseline, so
 * findings span every category the Security Review lens groups.
 */
const home = mkdtempSync(join(tmpdir(), 'beacon-lens-e2e-'));
const store = join(home, '.beacon', 'endpoint', 'rules');
mkdirSync(store, { recursive: true });
const corpus = resolve(__dirname, '../../../../../../../rules');
for (const category of readdirSync(corpus)) {
  for (const file of readdirSync(join(corpus, category))) {
    if (file.endsWith('.rule.yaml')) copyFileSync(join(corpus, category, file), join(store, basename(file)));
  }
}

const port = Number(process.env.LENS_E2E_PORT || 8798);
// The dashboard serves only requests that carry its launch secret. The config is evaluated again in
// every worker, so the secret is made once and handed down through the environment, which the
// workers and the server inherit. Every test context starts with the cookie the launch URL sets;
// tests/access.spec.ts drops it to exercise the launch URL itself.
process.env.BEACON_E2E_DASHBOARD_TOKEN ||= randomBytes(32).toString('base64url');
const dashboardToken: string = process.env.BEACON_E2E_DASHBOARD_TOKEN;
const dashboardCookie = `beacon_dashboard_${port}`;
// HOME moves to a fresh directory below, and Go keeps its module cache, build cache and
// downloaded toolchains under HOME unless told otherwise. Pin them to wherever they are now, or
// every run downloads the toolchain and every module again into a directory nobody cleans up.
const goEnv = Object.fromEntries(
  ['GOPATH', 'GOMODCACHE', 'GOCACHE'].map((key) => [key, execFileSync('go', ['env', key]).toString().trim()]),
);
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
    storageState: {
      cookies: [
        {
          name: dashboardCookie,
          value: dashboardToken,
          domain: '127.0.0.1',
          path: '/',
          expires: -1,
          httpOnly: true,
          secure: false,
          sameSite: 'Strict',
        },
      ],
      origins: [],
    },
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    launchOptions: process.env.CHROMIUM_PATH ? { executablePath: process.env.CHROMIUM_PATH } : {},
  },
  webServer: {
    command: `go run ../../e2eserver --addr 127.0.0.1:${port} --log fixtures/runtime.jsonl ${lenses}`,
    // Unauthenticated, so it answers 401 once the server is up, which Playwright counts as ready.
    url: `http://127.0.0.1:${port}/api/lenses`,
    timeout: 180_000,
    reuseExistingServer: false,
    env: { ...goEnv, HOME: home, BEACON_E2E_DASHBOARD_TOKEN: dashboardToken },
  },
});
