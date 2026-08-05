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
  const parser = chatgptAdapter.createParser(1);
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
