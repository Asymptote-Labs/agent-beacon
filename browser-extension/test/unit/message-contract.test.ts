import { readFileSync } from 'node:fs';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { claudeAdapter } from '../../src/adapters/claude.js';
import { chatgptAdapter } from '../../src/adapters/chatgpt.js';
import { normalizeTurn, turnToEnvelope } from '../../src/shared/normalize.js';
import { attrValue, type LogRecord } from '../../src/shared/otlp.js';
import type { ChatTurn, Msg } from '../../src/shared/types.js';
import { MAX_FIELD_BYTES } from '../../src/shared/vocab.js';

const cases = [
  {
    site: 'claude', adapter: claudeAdapter,
    url: '/api/organizations/org-test/chat_conversations/11111111-2222-3333-4444-555555555555/completion',
    body: { prompt: 'What is the capital of France?', model: 'claude-opus-4-8' },
    answer: 'Paris.',
  },
  {
    site: 'chatgpt', adapter: chatgptAdapter,
    url: 'https://chatgpt.com/backend-api/f/conversation',
    body: { model: 'gpt-5-6-thinking', messages: [{ author: { role: 'user' }, content: { parts: ['good evening'] } }] },
    answer: 'Good evening! Hello to you as well. Hope your day is going well.',
  },
];

afterEach(() => vi.useRealTimers());

describe('recorded browser message contract', () => {
  for (const { site, adapter, url, body, answer } of cases) {
    it(`${site} emits semconv messages for a recorded turn`, () => {
      vi.useFakeTimers();
      vi.setSystemTime(new Date('2026-10-01T10:00:00Z'));
      const parser = adapter.createParser(`message-contract-${site}`);
      parser.onRequest(url, 'POST', JSON.stringify(body));
      const fixture = readFileSync(new URL(`../../fixtures/${site}/simple-turn.sse`, import.meta.url), 'utf8');
      for (let i = 0; i < fixture.length; i += 7) parser.onChunk(fixture.slice(i, i + 7));
      parser.onDone();
      const turn = parser.getTurn()!;
      expect(turn.responseText).toBe(answer);
      const envelope = turnToEnvelope(turn, 'full');
      const response = envelope.resourceLogs[0].scopeLogs[0].logRecords.find((record) =>
        attrValue(record.attributes, 'beacon.event.action') === 'agent.response.completed')!;
      expect(JSON.parse(attrValue(response.attributes, 'gen_ai.output.messages') as string)).toEqual([
        { role: 'assistant', parts: [{ type: 'text', content: answer }] },
      ]);
    });
  }
});

function turn(messages: Msg[]): ChatTurn {
  return {
    site: 'claude_web', sessionId: 'message-session', turnId: 'message-turn',
    requestModel: 'request', responseModel: 'response',
    promptText: 'prompt', inputMessages: messages, outputMessages: messages,
    responseText: messages.map((message) => message.text).join(''), toolCalls: [],
    startedAt: 1000, completedAt: 2000, captureMode: 'sse',
    usage: { inputTokens: 7, outputTokens: 3 },
  };
}

function field(record: LogRecord, key: string): { role: string; parts: { type: string; content: string }[] }[] {
  const serialized = attrValue(record.attributes, key) as string;
  expect(new TextEncoder().encode(serialized).length).toBeLessThanOrEqual(MAX_FIELD_BYTES);
  return JSON.parse(serialized);
}

describe('bounded structured message retention', () => {
  it('preserves ordered message roles and text, including JSON escapes and Unicode', () => {
    const messages: Msg[] = [
      { role: 'system', text: 'line\n"quoted" \\ path 😀' },
      { role: 'user', text: 'café' }, { role: 'assistant', text: '' },
      { role: 'tool', text: 'tool result' },
    ];
    const records = normalizeTurn(turn(messages), 'full').logRecords;
    for (const [record, key] of [[records[0], 'gen_ai.input.messages'], [records[1], 'gen_ai.output.messages']] as const) {
      expect(field(record, key)).toEqual(messages.map(({ role, text }) => ({ role, parts: [{ type: 'text', content: text }] })));
      expect(attrValue(record.attributes, 'beacon.field_truncated')).toBeUndefined();
    }
  });

  it('scrubs message content before serialization and drops all message fields in metadata mode', () => {
    const messages: Msg[] = [{ role: 'assistant', text: 'alice@example.com skabcdefghijklmnop "\\\n😀' }];
    const redacted = normalizeTurn(turn(messages), 'redacted').logRecords;
    expect(field(redacted[1], 'gen_ai.output.messages')[0].parts[0].content).toBe('[email] [token] "\\\n😀');
    const metadata = normalizeTurn(turn(messages), 'metadata').logRecords;
    expect(metadata).toHaveLength(1);
    expect(attrValue(metadata[0].attributes, 'gen_ai.output.messages')).toBeUndefined();
    expect(attrValue(metadata[0].attributes, 'gen_ai.input.messages')).toBeUndefined();
    expect(attrValue(metadata[0].attributes, 'gen_ai.usage.output_tokens')).toBe(3);
    expect(attrValue(metadata[0].attributes, 'beacon.record.id')).toBe('message-turn#agent.response.completed#0');
  });

  it('keeps empty arrays valid without falsely reporting truncation', () => {
    const records = normalizeTurn(turn([]), 'full').logRecords;
    expect(field(records[0], 'gen_ai.input.messages')).toEqual([]);
    expect(field(records[1], 'gen_ai.output.messages')).toEqual([]);
    expect(attrValue(records[1].attributes, 'beacon.field_truncated')).toBeUndefined();
  });

  it('preserves exact-budget JSON and flags a one-byte overflow', () => {
    const overhead = new TextEncoder().encode(JSON.stringify([{ role: 'assistant', parts: [{ type: 'text', content: '' }] }])).length;
    const text = 'x'.repeat(MAX_FIELD_BYTES - overhead);
    for (const extra of ['', 'x']) {
      const records = normalizeTurn(turn([{ role: 'assistant', text: text + extra }]), 'full').logRecords;
      expect(field(records[1], 'gen_ai.output.messages')[0].parts[0].content).toBe(text);
      expect(attrValue(records[1].attributes, 'beacon.field_truncated')).toBe(extra ? true : undefined);
    }
  });

  it('keeps the largest complete Unicode prefix at serialized byte boundaries', () => {
    const encoder = new TextEncoder();
    const overhead = encoder.encode(JSON.stringify([{ role: 'assistant', parts: [{ type: 'text', content: '' }] }])).length;
    for (const slack of [1, 2, 3, 4, 5, 6]) {
      const text = 'a'.repeat(MAX_FIELD_BYTES - overhead - slack) + '😀end';
      let oracle = '';
      let bytes = overhead;
      for (const cp of text) {
        const next = encoder.encode(JSON.stringify(cp)).length - 2;
        if (bytes + next > MAX_FIELD_BYTES) break;
        oracle += cp;
        bytes += next;
      }
      const record = normalizeTurn(turn([{ role: 'assistant', text }]), 'full').logRecords[1];
      expect(field(record, 'gen_ai.output.messages')[0].parts[0].content).toBe(oracle);
      expect(attrValue(record.attributes, 'beacon.field_truncated')).toBe(true);
    }
  });

  for (const text of ['"\\\n'.repeat(MAX_FIELD_BYTES), '😀é中'.repeat(MAX_FIELD_BYTES)]) {
    it(`bounds oversized ${text.startsWith('😀') ? 'Unicode' : 'escaped'} text as valid JSON`, () => {
      const messages: Msg[] = [{ role: 'system', text: 'keep first' }, { role: 'assistant', text }, { role: 'user', text: 'later' }];
      const records = normalizeTurn(turn(messages), 'full').logRecords;
      for (const [record, key] of [[records[0], 'gen_ai.input.messages'], [records[1], 'gen_ai.output.messages']] as const) {
        const kept = field(record, key);
        expect(kept).toHaveLength(2);
        expect(kept[0].parts[0].content).toBe('keep first');
        const prefix = kept[1].parts[0].content;
        expect(prefix.length).toBeGreaterThan(0);
        expect(text.startsWith(prefix)).toBe(true);
        expect(Array.from(prefix).some((cp) => cp.length === 1 && /[\uD800-\uDFFF]/.test(cp))).toBe(false);
        expect(attrValue(record.attributes, 'beacon.field_truncated')).toBe(true);
      }
    });
  }

  it('drops a message whose wrapper alone does not fit', () => {
    const overhead = new TextEncoder().encode(JSON.stringify([{ role: 'assistant', parts: [{ type: 'text', content: '' }] }])).length;
    const full = 'x'.repeat(MAX_FIELD_BYTES - overhead);
    const records = normalizeTurn(turn([{ role: 'assistant', text: full }, { role: 'user', text: '' }]), 'full').logRecords;
    expect(field(records[1], 'gen_ai.output.messages')).toEqual([{ role: 'assistant', parts: [{ type: 'text', content: full }] }]);
    expect(attrValue(records[1].attributes, 'beacon.field_truncated')).toBe(true);
  });
});
