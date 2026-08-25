import { defineConfig } from '@playwright/test';

/**
 * Playwright config tuned for Manifest V3 extension testing.
 *
 * - No `projects` with the usual `devices` — extensions require a persistent
 *   context that we build by hand in e2e/fixtures.ts, so the browser launch
 *   lives there, not here.
 * - `trace`/`video` on failure give me artifacts to self-diagnose without a human.
 */
export default defineConfig({
  testDir: './e2e',
  fullyParallel: false, // one persistent Chromium profile at a time keeps state clean
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  workers: 1,
  reporter: [['list'], ['html', { open: 'never' }]],
  use: {
    trace: 'retain-on-failure',
    video: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
});
