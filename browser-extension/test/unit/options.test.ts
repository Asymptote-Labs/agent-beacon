import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

const optionsHtml = readFileSync(
  new URL('../../src/options/options.html', import.meta.url),
  'utf8',
);

describe('options page site toggles', () => {
  it.each([
    ['site_claude_web', 'claude.ai'],
    ['site_chatgpt_web', 'chatgpt.com'],
  ])('associates the %s checkbox with its visible site name', (id, name) => {
    expect(optionsHtml).toContain(
      `    <label class="site"><input type="checkbox" id="${id}" /><span>${name}</span></label>`,
    );
  });
});
