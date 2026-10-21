import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chatgptAdapter } from '../../src/adapters/chatgpt.js';
import type { TurnParser } from '../../src/adapters/adapter.js';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const fixture = (name: string) =>
  readFileSync(path.join(__dirname, '..', '..', 'fixtures', 'chatgpt', `${name}.sse`), 'utf8');

const CONV = '6a73b75a-18d8-83ea-bbcf-984208806963';

/** ChatGPT request body shape (messages[].content.parts). */
function reqBody(prompt: string, conversationId: string | null): string {
  return JSON.stringify({
    action: 'next',
    conversation_id: conversationId,
    model: 'gpt-5-6-thinking',
    messages: [
      { id: 'u1', author: { role: 'user' }, content: { content_type: 'text', parts: [prompt] } },
    ],
  });
}

function run(name: string, prompt: string, conversationId: string | null, chunkSize = 7): TurnParser {
  const parser = chatgptAdapter.createParser('req-0001');
  parser.onRequest('https://chatgpt.com/backend-api/f/conversation', 'POST', reqBody(prompt, conversationId));
  const body = fixture(name);
  for (let i = 0; i < body.length; i += chunkSize) parser.onChunk(body.slice(i, i + chunkSize));
  parser.onDone();
  return parser;
}

describe('chatgpt adapter — simple turn (delta_encoding v1)', () => {
  const turn = run('simple-turn', 'good evening', null)!.getTurn()!;

  it('reconstructs the response across add/append/bare/patch ops', () => {
    expect(turn.responseText).toBe('Good evening! Hello to you as well. Hope your day is going well.');
  });
  it('captures the prompt from the request body', () => {
    expect(turn.promptText).toBe('good evening');
  });
  it('takes the conversation id from the stream (not the message id)', () => {
    expect(turn.sessionId).toBe(CONV);
  });
  it('captures the response model from metadata.model_slug', () => {
    expect(turn.responseModel).toBe('gpt-5-6-thinking');
  });
  it('selects the assistant message id for the turn key', () => {
    expect(turn.turnId).toBe(`${CONV}:asst-0001`);
  });
  it('marks the turn completed and has no tool calls (empty search groups)', () => {
    expect(turn.completedAt).toBeTypeOf('number');
    expect(turn.toolCalls).toHaveLength(0);
  });
});

describe('chatgpt adapter — chunk-size invariance', () => {
  it('produces the same response text regardless of chunk boundaries', () => {
    const texts = [1, 3, 13, 100, 9999].map((n) => run('simple-turn', 'x', null, n).getTurn()!.responseText);
    for (const t of texts)
      expect(t).toBe('Good evening! Hello to you as well. Hope your day is going well.');
  });
});

describe('chatgpt adapter — existing conversation', () => {
  it('uses the request-body conversation_id when present', () => {
    const turn = run('simple-turn', 'hi', 'body-conv-123')!.getTurn()!;
    expect(turn.sessionId).toBe('body-conv-123');
  });
});

describe('chatgpt adapter — structural message updates', () => {
  type Delta = { o?: string; p?: string; v: unknown };
  const message = (text: string, id = 'original', recipient = 'all') => ({
    id,
    author: { role: 'assistant' },
    recipient,
    content: { parts: [text] },
    metadata: { model_slug: `${id}-model`, search_result_groups: [{ title: id }] },
  });
  const seed: Delta = { o: 'add', p: '', v: { message: message('first') } };

  function parse(deltas: Delta[], chunkSize = 7) {
    const parser = chatgptAdapter.createParser('req-replacement');
    parser.onRequest('https://chatgpt.com/backend-api/f/conversation', 'POST', reqBody('hello', CONV));
    const body = deltas.map((delta) => `event: delta\ndata: ${JSON.stringify(delta)}\n\n`).join('') +
      'data: [DONE]\n\n';
    for (let i = 0; i < body.length; i += chunkSize) parser.onChunk(body.slice(i, i + chunkSize));
    parser.onDone();
    return parser.getTurn()!;
  }

  it.each([
    ['replace', ''],
    ['replace', '/'],
    ['replace', '/message'],
    ['add', '/message'],
  ])('exports the new message after %s at %j', (o, p) => {
    const replacement = message('corrected', 'replacement');
    const turn = parse([seed, { o, p, v: p === '/message' ? replacement : { message: replacement } }]);
    expect(turn.responseText).toBe('corrected');
    expect(turn.outputMessages).toEqual([{ role: 'assistant', text: 'corrected' }]);
    expect(turn.turnId).toBe(`${CONV}:replacement`);
    expect(turn.responseModel).toBe('replacement-model');
    expect(turn.toolCalls).toEqual([{
      id: 'replacement:search', name: 'web_search',
      arguments: { search_result_groups: [{ title: 'replacement' }] },
    }]);
    expect(turn.completedAt).toBeTypeOf('number');
  });

  it.each([1, 7, 10_000])('applies nested replacements and subsequent appends with chunk size %i', (size) => {
    const turn = parse([seed, {
      o: 'patch', p: '', v: [
        { o: 'replace', p: '', v: { message: message('root', 'root') } },
        { o: 'patch', p: '', v: [
          { o: 'replace', p: '/message', v: message('nested', 'nested') },
          { o: 'append', p: '/message/content/parts/0', v: ' patch' },
        ] },
      ],
    }, { o: 'append', p: '/message/content/parts/0', v: ' append' }, { v: ' inherited' }], size);
    expect(turn.responseText).toBe('nested patch append inherited');
    expect(turn.turnId).toBe(`${CONV}:nested`);
    expect(turn.responseModel).toBe('nested-model');
  });

  it('keeps bare operation and path inheritance when replacing the root', () => {
    const turn = parse([seed,
      { o: 'replace', p: '', v: { message: message('second', 'second') } },
      { v: { message: message('third', 'third') } },
    ]);
    expect(turn.responseText).toBe('third');
    expect(turn.turnId).toBe(`${CONV}:third`);
  });

  it('preserves direct content and metadata edits', () => {
    const turn = parse([seed,
      { o: 'replace', p: '/message/content/parts/0', v: 'leaf' },
      { o: 'append', p: '/message/content/parts/0', v: ' appended' },
      { o: 'replace', p: '/message/metadata', v: { model_slug: 'leaf-model' } },
      { o: 'append', p: '/message/metadata', v: { search_result_groups: [{ title: 'leaf' }] } },
    ]);
    expect(turn.responseText).toBe('leaf appended');
    expect(turn.responseModel).toBe('leaf-model');
    expect(turn.toolCalls[0].arguments).toEqual({ search_result_groups: [{ title: 'leaf' }] });
  });

  it.each(['', '/message'])('keeps the last visible answer when %j changes to a tool or aside', (p) => {
    const wrap = (m: ReturnType<typeof message>) => p === '' ? { message: m } : m;
    const turn = parse([seed,
      { o: 'add', p: '', v: { message: message('latest visible', 'visible') } },
      { o: 'replace', p, v: wrap(message('hidden tool call', 'tool', 'browser')) },
      { o: 'append', p: '/message/content/parts/0', v: ' tool detail' },
      { o: 'replace', p, v: wrap({ ...message('hidden tool reply', 'reply'), author: { role: 'tool' } }) },
    ]);
    expect(turn.responseText).toBe('latest visible');
    expect(turn.turnId).toBe(`${CONV}:visible`);
    expect(turn.responseModel).toBe('visible-model');
  });

  it('ignores malformed and no-message frames and flushes the final completion marker', () => {
    const parser = chatgptAdapter.createParser('req-no-message');
    parser.onRequest('https://chatgpt.com/backend-api/f/conversation', 'POST', null);
    parser.onChunk('data: not-json\n\ndata: null\n\ndata: {"o":"replace","p":"","v":{}}\n\n');
    expect(parser.getTurn()).toBeNull();
    parser.onChunk(`data: ${JSON.stringify(seed)}\n\ndata: [DONE]`);
    parser.onDone();
    expect(parser.getTurn()?.responseText).toBe('first');
    expect(parser.getTurn()?.completedAt).toBeTypeOf('number');
  });
});
