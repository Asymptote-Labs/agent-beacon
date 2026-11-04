import { expect, it } from 'vitest';
import { normalizeTurn } from '../../src/shared/normalize.js';
import { attrValue } from '../../src/shared/otlp.js';
import { MAX_FIELD_BYTES } from '../../src/shared/vocab.js';
import type { ChatTurn } from '../../src/shared/types.js';
const turn = (over: Partial<ChatTurn> = {}): ChatTurn => ({ site: 'claude_web', sessionId: 'session', turnId: 'turn', promptText: 'hi', inputMessages: [], outputMessages: [], responseText: 'ok', toolCalls: [], usage: {}, startedAt: 1700000000000, captureMode: 'sse', ...over });
it('keeps the largest complete Unicode prefix at each emoji byte boundary', () => {
  for (const slack of [1, 2, 3, 4, 5]) {
    const text = 'a'.repeat(MAX_FIELD_BYTES - slack) + '😀tail';
    let oracle = '', bytes = 0;
    for (const cp of text) {
      const size = new TextEncoder().encode(cp).length;
      if (bytes + size > MAX_FIELD_BYTES)
        break;
      oracle += cp;
      bytes += size;
    }
    const r = normalizeTurn(turn({ promptText: text }), 'full').logRecords[0];
    expect(attrValue(r.attributes, 'beacon.prompt.text')).toBe(oracle);
    expect(attrValue(r.attributes, 'beacon.field_truncated')).toBe(true);
  }
});
it('preserves complete text exactly at the limit', () => {
  const text = 'a'.repeat(MAX_FIELD_BYTES - 4) + '😀';
  const r = normalizeTurn(turn({ promptText: text }), 'full').logRecords[0];
  expect(attrValue(r.attributes, 'beacon.prompt.text')).toBe(text);
  expect(attrValue(r.attributes, 'beacon.field_truncated')).toBeUndefined();
});
