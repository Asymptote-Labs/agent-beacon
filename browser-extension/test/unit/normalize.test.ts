import { describe, it, expect } from 'vitest';
import { normalizeTurn } from '../../src/shared/normalize.js';
import { attrValue, type KeyValue, type LogRecord } from '../../src/shared/otlp.js';
import type { ChatTurn, Retention } from '../../src/shared/types.js';

function baseTurn(over: Partial<ChatTurn> = {}): ChatTurn {
  return {
    site: 'claude_web',
    sessionId: 'conv-123',
    turnId: 'conv-123:1',
    requestModel: 'claude-opus-4-8',
    responseModel: 'claude-opus-4-8',
    promptText: 'What is 2+2?',
    inputMessages: [{ role: 'user', text: 'What is 2+2?' }],
    outputMessages: [{ role: 'assistant', text: '4' }],
    responseText: '4',
    toolCalls: [],
    usage: { inputTokens: 10, outputTokens: 2 },
    startedAt: 1_700_000_000_000,
    completedAt: 1_700_000_002_000,
    captureMode: 'sse',
    ...over,
  };
}

function flat(attrs: KeyValue[]): Record<string, string | number | boolean> {
  const out: Record<string, string | number | boolean> = {};
  for (const a of attrs) {
    const v = attrValue(attrs, a.key);
    if (v !== undefined) out[a.key] = v;
  }
  return out;
}

/** Find the record for a given beacon.event.action. */
function byAction(recs: LogRecord[], action: string): LogRecord | undefined {
  return recs.find((r) => flat(r.attributes)['beacon.event.action'] === action);
}

function normalize(turn: ChatTurn, retention: Retention = 'full') {
  return normalizeTurn(turn, retention);
}

describe('normalizeTurn — full retention, simple completed turn', () => {
  const { resourceAttributes, logRecords } = normalize(baseTurn());
  const res = flat(resourceAttributes);
  const promptRec = byAction(logRecords, 'prompt.submitted')!;
  const respRec = byAction(logRecords, 'agent.response.completed')!;

  it('emits a prompt.submitted AND an agent.response.completed record', () => {
    expect(logRecords).toHaveLength(2);
    expect(promptRec).toBeTruthy();
    expect(respRec).toBeTruthy();
  });

  it('sets resource attrs identifying the browser source', () => {
    expect(res['beacon.harness.name']).toBe('claude_web');
    expect(res['beacon.origin']).toBe('browser-extension');
    expect(res['gen_ai.provider.name']).toBe('anthropic');
    expect(res['service.name']).toBe('agent-beacon-browser-collector');
  });

  it('puts prompt text on the prompt.submitted record (category prompt)', () => {
    const p = flat(promptRec.attributes);
    expect(p['beacon.event.category']).toBe('prompt');
    expect(p['beacon.prompt.text']).toBe('What is 2+2?');
  });

  it('keeps the response record focused on the response (no prompt.text)', () => {
    const r = flat(respRec.attributes);
    expect(r['beacon.event.category']).toBe('agent');
    expect(r['beacon.prompt.text']).toBeUndefined();
    expect(String(r['gen_ai.output.messages'])).toContain('4');
  });

  it('uses the conversation id for both session and gen_ai.conversation', () => {
    const r = flat(respRec.attributes);
    expect(r['beacon.session.id']).toBe('conv-123');
    expect(r['gen_ai.conversation.id']).toBe('conv-123');
  });

  it('encodes usage tokens as integers on the response', () => {
    const r = flat(respRec.attributes);
    expect(r['gen_ai.usage.input_tokens']).toBe(10);
    expect(r['gen_ai.usage.output_tokens']).toBe(2);
  });

  it('does NOT set fields the exporter fills', () => {
    for (const rec of logRecords) {
      const a = flat(rec.attributes);
      for (const forbidden of ['vendor', 'product', 'schema_version', 'timestamp']) {
        expect(a[forbidden]).toBeUndefined();
        expect(res[forbidden]).toBeUndefined();
      }
    }
  });

  it('timestamps prompt from startedAt and response from completedAt', () => {
    expect(promptRec.timeUnixNano).toBe('1700000000000000000');
    expect(respRec.timeUnixNano).toBe('1700000002000000000');
  });
});

describe('normalizeTurn — retention modes', () => {
  it('metadata retention drops the prompt.submitted record and all text', () => {
    const { logRecords } = normalize(baseTurn(), 'metadata');
    expect(byAction(logRecords, 'prompt.submitted')).toBeUndefined();
    const r = flat(byAction(logRecords, 'agent.response.completed')!.attributes);
    expect(r['beacon.prompt.text']).toBeUndefined();
    expect(r['gen_ai.output.messages']).toBeUndefined();
    expect(r['beacon.content.retention']).toBe('metadata');
    // Metadata still present:
    expect(r['gen_ai.request.model']).toBe('claude-opus-4-8');
    expect(r['gen_ai.usage.output_tokens']).toBe(2);
    expect(r['beacon.response.chars']).toBe(1);
  });

  it('redacted retention scrubs emails from the prompt.submitted text', () => {
    const turn = baseTurn({ promptText: 'email me at alice@example.com please' });
    const p = flat(byAction(normalize(turn, 'redacted').logRecords, 'prompt.submitted')!.attributes);
    expect(p['beacon.prompt.text']).toBe('email me at [email] please');
    expect(p['beacon.content.retention']).toBe('redacted');
  });
});

describe('normalizeTurn — tool calls', () => {
  const turn = baseTurn({
    toolCalls: [
      { id: 'toolu_1', name: 'web_search', arguments: { q: 'weather' }, result: 'sunny' },
    ],
  });
  const { logRecords } = normalize(turn);

  it('emits prompt.submitted + agent.response.completed + tool.invoked', () => {
    expect(logRecords).toHaveLength(3);
    const tool = flat(byAction(logRecords, 'tool.invoked')!.attributes);
    expect(tool['beacon.event.category']).toBe('tool');
    expect(tool['tool.name']).toBe('web_search');
    expect(tool['gen_ai.tool.call.name']).toBe('web_search');
    expect(tool['gen_ai.tool.call.id']).toBe('toolu_1');
    expect(tool['gen_ai.tool.call.arguments']).toBe('{"q":"weather"}');
    expect(tool['gen_ai.tool.call.result']).toBe('sunny');
  });
});

describe('normalizeTurn — partial/aborted stream', () => {
  it('marks an incomplete response as agent.response (not completed)', () => {
    const turn = baseTurn({ completedAt: undefined });
    const { logRecords } = normalize(turn);
    expect(byAction(logRecords, 'agent.response')).toBeTruthy();
    expect(byAction(logRecords, 'agent.response.completed')).toBeUndefined();
    // Prompt still captured.
    expect(byAction(logRecords, 'prompt.submitted')).toBeTruthy();
  });
});

describe('normalizeTurn — size cap', () => {
  it('truncates oversized prompt text and flags it', () => {
    const huge = 'x'.repeat(200_000);
    const p = flat(byAction(normalize(baseTurn({ promptText: huge })).logRecords, 'prompt.submitted')!.attributes);
    expect((p['beacon.prompt.text'] as string).length).toBeLessThan(huge.length);
    expect(p['beacon.field_truncated']).toBe(true);
  });
});
