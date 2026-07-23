import { defineConfig } from 'vitest/config';

// Pure unit tests for the browser-free core (adapters + normalization).
// Node environment: no DOM, no chrome.* — these tests exercise pure functions.
export default defineConfig({
  test: {
    environment: 'node',
    include: ['test/unit/**/*.test.ts'],
    watch: false,
  },
});
