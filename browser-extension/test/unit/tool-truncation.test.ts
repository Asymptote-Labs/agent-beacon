import { expect, it } from 'vitest';
import { normalizeTurn } from '../../src/shared/normalize.js';
import { attrValue } from '../../src/shared/otlp.js';
import { MAX_FIELD_BYTES } from '../../src/shared/vocab.js';
import type { ChatTurn } from '../../src/shared/types.js';
const turn = (over: Partial<ChatTurn> = {}): ChatTurn => ({ site: 'claude_web', sessionId: 'session', turnId: 'turn', promptText: 'hi', inputMessages: [], outputMessages: [], responseText: 'ok', toolCalls: [], usage: {}, startedAt: 1700000000000, captureMode: 'sse', ...over });
it('marks only tool records whose retained content is truncated', () => {
  for (const field of ['arguments', 'result'] as const)
    for (const retention of ['full', 'redacted', 'metadata'] as const) {
      const tools = [{ id: 'big', name: 'tool', [field]: 'x'.repeat(MAX_FIELD_BYTES + 1) }, { id: 'small', name: 'tool', arguments: 'ok', result: 'ok' }];
      const records = normalizeTurn(turn({ toolCalls: tools }), retention).logRecords.filter(r => attrValue(r.attributes, 'beacon.event.action') === 'tool.invoked');
      expect(attrValue(records[0].attributes, 'beacon.field_truncated')).toBe(retention === 'metadata' ? undefined : true);
      expect(attrValue(records[1].attributes, 'beacon.field_truncated')).toBeUndefined();
      expect(records.every(r => attrValue(r.attributes, 'gen_ai.tool.call.id') !== undefined)).toBe(true);
    }
});
