import { readFileSync } from 'node:fs';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { claudeAdapter } from '../../src/adapters/claude.js';
import { chatgptAdapter } from '../../src/adapters/chatgpt.js';
import { Assembler } from '../../src/background/assembler.js';
import { normalizeTurn } from '../../src/shared/normalize.js';
import { attrValue } from '../../src/shared/otlp.js';
import type { ChatTurn } from '../../src/shared/types.js';

const CONV = '11111111-2222-3333-4444-555555555555';
const COMPLETION_URL = `/api/organizations/org-x/chat_conversations/${CONV}/completion`;
const PROMPT = 'What is the capital of France?';
const REQUESTED = 'requested-claude-alias';
const RESPONDED = 'claude-opus-4-8';
const fixture = (site: string, name = 'simple-turn') =>
  readFileSync(new URL(`../../fixtures/${site}/${name}.sse`, import.meta.url), 'utf8');
const recorded = fixture('claude');
const startFrame = recorded.split('\n\n').find((frame) => frame.startsWith('event: message_start\n'))!;
const startWithoutModel = startFrame.split('\n').map((line) => {
  if (!line.startsWith('data: ')) return line;
  const value = JSON.parse(line.slice(6));
  delete value.message.model;
  return `data: ${JSON.stringify(value)}`;
}).join('\n');
const request = (model: unknown = REQUESTED) => ({ prompt: PROMPT, conversation_id: CONV, model });

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date('2026-10-22T10:00:00Z'));
});
afterEach(() => vi.useRealTimers());

function replay(body: object | null, source = recorded, chunkSize = 7): ChatTurn {
  const parser = claudeAdapter.createParser('model-request');
  parser.onRequest(COMPLETION_URL, 'POST', body == null ? null : JSON.stringify(body));
  for (let i = 0; i < source.length; i += chunkSize) parser.onChunk(source.slice(i, i + chunkSize));
  parser.onDone();
  return parser.getTurn()!;
}

function withoutRequestedModel(turn: ChatTurn) {
  const { requestModel: _requestModel, ...rest } = turn;
  return rest;
}

describe('Claude model source attribution', () => {
  it.each([1, 7, 4096])('keeps the request alias with recorded response chunks of %i characters', (size) => {
    const turn = replay(request(), recorded, size);
    expect(turn.requestModel).toBe(REQUESTED);
    expect(turn.responseModel).toBe(RESPONDED);
    expect(turn.responseText).toBe('Paris.');
    expect(withoutRequestedModel(turn)).toEqual(withoutRequestedModel(replay(request(RESPONDED))));
  });

  it('preserves a request that names the responding model', () => {
    const turn = replay(request(RESPONDED));
    expect(turn.requestModel).toBe(RESPONDED);
    expect(turn.responseModel).toBe(RESPONDED);
  });

  it.each([undefined, null, 17])('does not infer an absent or nonstring requested model (%s)', (model) => {
    const turn = replay({ prompt: PROMPT, model });
    expect(turn.requestModel).toBeUndefined();
    expect(turn.responseModel).toBe(RESPONDED);
    expect(turn.responseText).toBe('Paris.');
  });

  it('does not borrow a returned model when the request body was unavailable', () => {
    const turn = replay(null);
    expect(turn.requestModel).toBeUndefined();
    expect(turn.responseModel).toBe(RESPONDED);
    expect(turn.promptText).toBe('');
    expect(turn.inputMessages).toEqual([]);
  });

  it.each([
    ['missing message_start', recorded.replace(`${startFrame}\n\n`, '')],
    ['missing response model', recorded.replace(startFrame, startWithoutModel)],
    ['partial message_start', recorded.slice(0, recorded.indexOf('"model"') + '"model":'.length)],
  ])('retains the existing request-only response fallback with %s', (_name, source) => {
    const turn = replay(request(), source);
    expect(turn.requestModel).toBe(REQUESTED);
    expect(turn.responseModel).toBe(REQUESTED);
    expect(turn.promptText).toBe(PROMPT);
  });

  it('does not manufacture either model when neither source supplied one', () => {
    const turn = replay({ prompt: PROMPT }, recorded.replace(`${startFrame}\n\n`, ''));
    expect(turn.requestModel).toBeUndefined();
    expect(turn.responseModel).toBeUndefined();
    expect(turn.responseText).toBe('Paris.');
  });

  it('keeps response attribution when a repeated start omits its model', () => {
    // Replay the existing start frame with an omitted optional field; this is
    // a defensive replay control, not a newly claimed provider wire shape.
    const source = recorded.replace(startFrame, `${startFrame}\n\n${startWithoutModel}`);
    const turn = replay(request(), source);
    expect(turn.requestModel).toBe(REQUESTED);
    expect(turn.responseModel).toBe(RESPONDED);
    expect(withoutRequestedModel(turn)).toEqual(withoutRequestedModel(replay(request(RESPONDED))));
  });

  it.each(['with-tool-call', 'with-usage'])('leaves existing %s text, tools, IDs and usage intact', (name) => {
    // with-usage is the repository's existing synthetic supported-shape fixture;
    // it is not represented as recorded claude.ai usage traffic.
    const source = fixture('claude', name);
    const turn = replay(request(), source);
    expect(turn.requestModel).toBe(REQUESTED);
    expect(withoutRequestedModel(turn)).toEqual(withoutRequestedModel(replay(request(RESPONDED), source)));
  });

  it('leaves the existing recorded ChatGPT attribution contract unchanged', () => {
    const parser = chatgptAdapter.createParser('chatgpt-model-control');
    parser.onRequest('https://chatgpt.com/backend-api/f/conversation', 'POST', JSON.stringify({
      model: 'requested-chatgpt-alias', messages: [{ author: { role: 'user' }, content: { parts: ['good evening'] } }],
    }));
    const source = fixture('chatgpt');
    for (let i = 0; i < source.length; i += 7) parser.onChunk(source.slice(i, i + 7));
    parser.onDone();
    const turn = parser.getTurn()!;
    expect(turn.requestModel).toBe('requested-chatgpt-alias');
    expect(turn.responseModel).toBe('gpt-5-6-thinking');
    expect(turn.responseText).toBe('Good evening! Hello to you as well. Hope your day is going well.');
  });
});

describe('Claude capture model attributes', () => {
  it.each([REQUESTED, undefined])('keeps source-owned model attributes through assembly (%s)', (model) => {
    const assembler = new Assembler();
    assembler.handle('claude.ai', 1, {
      kind: 'request', reqId: 'assembled-model', url: COMPLETION_URL, method: 'POST',
      body: JSON.stringify({ prompt: PROMPT, model }),
    });
    for (let i = 0; i < recorded.length; i += 7) {
      assembler.handle('claude.ai', 1, { kind: 'chunk', reqId: 'assembled-model', chunk: recorded.slice(i, i + 7) });
    }
    const turns = assembler.handle('claude.ai', 1, { kind: 'done', reqId: 'assembled-model' });
    expect(turns).toHaveLength(1);
    expect(assembler.active).toBe(0);
    const turn = turns[0];
    expect(turn.requestModel).toBe(model);
    expect(turn.responseModel).toBe(RESPONDED);
    const records = normalizeTurn(turn, 'full').logRecords;
    expect(records).toHaveLength(2);
    for (const record of records) {
      expect(attrValue(record.attributes, 'gen_ai.request.model')).toBe(model);
      expect(attrValue(record.attributes, 'beacon.session.id')).toBe(CONV);
    }
    expect(attrValue(records[0].attributes, 'beacon.prompt.text')).toBe(PROMPT);
    expect(attrValue(records[0].attributes, 'gen_ai.response.model')).toBeUndefined();
    expect(attrValue(records[1].attributes, 'gen_ai.response.model')).toBe(RESPONDED);
    expect(attrValue(records[1].attributes, 'beacon.response.chars')).toBe(6);
    expect(attrValue(records[0].attributes, 'beacon.record.id')).toBe(`${turn.turnId}#prompt.submitted#0`);
    expect(attrValue(records[1].attributes, 'beacon.record.id')).toBe(`${turn.turnId}#agent.response.completed#0`);
  });
});
