// Every extension surface goes through the shim in src/shared/browser.ts. A
// direct `chrome.runtime...` call would work on Chrome and break on Firefox,
// where chrome.* is callback-style and the code awaits it -- exactly the kind of
// regression no Chromium e2e can catch.
import { describe, it, expect } from 'vitest';
import { readdirSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const srcDir = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..', 'src');
const SHIM = path.join('shared', 'browser.ts');

function tsFiles(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const p = path.join(dir, e.name);
    return e.isDirectory() ? tsFiles(p) : e.name.endsWith('.ts') ? [p] : [];
  });
}

function stripComments(code: string): string {
  return code.replace(/\/\*[\s\S]*?\*\//g, '').replace(/(^|[^:])\/\/.*$/gm, '$1');
}

const DIRECT = /\b(chrome|browser)\s*\.\s*(runtime|storage|alarms|permissions|scripting|tabs)\b/;

describe('extension API access', () => {
  const files = tsFiles(srcDir).filter((f) => path.relative(srcDir, f) !== SHIM);

  it('scans the extension sources', () => {
    expect(files.length).toBeGreaterThan(5);
  });

  it.each(files.map((f) => [path.relative(srcDir, f), f]))(
    '%s uses the shim, not chrome.* / browser.* directly',
    (_rel, file) => {
      const offending = stripComments(readFileSync(file, 'utf8'))
        .split('\n')
        .filter((line) => DIRECT.test(line));
      expect(offending).toEqual([]);
    },
  );
});
