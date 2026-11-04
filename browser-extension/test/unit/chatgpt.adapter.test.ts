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

// web-search-split.sse follows the op pattern of an October 2026 chatgpt.com
// turn (sanitized and shortened): an answer interrupted by a web.run search.
// Each delta's `c` names the message it applies to, and the ops for the answer
// segments interleave with the same text sent to a merged message.
describe('chatgpt adapter — answer split around a web search', () => {
  const ANSWER = "The Boston Marathon is the world's oldest annual marathon.\n\n**Next race:** Monday, April 19, 2027.";
  const turn = run('web-search-split', 'Boston Marathon', null)!.getTurn()!;

  it('records the whole answer once, not the newest segment or a doubled copy', () => {
    expect(turn.responseText).toBe(ANSWER);
    expect(turn.outputMessages).toEqual([{ role: 'assistant', text: ANSWER }]);
  });

  it('is the same for every chunk size', () => {
    for (const n of [1, 5, 64, 100000]) {
      expect(run('web-search-split', 'x', null, n).getTurn()!.responseText).toBe(ANSWER);
    }
  });

  it('keys the turn by the merged answer message', () => {
    expect(turn.turnId).toBe('6adb0000-0000-8000-8000-000000000001:merged-0001');
    expect(turn.responseModel).toBe('gpt-6');
  });

  it('records the search with its query as arguments and its results as the result', () => {
    expect(turn.toolCalls).toHaveLength(1);
    const [call] = turn.toolCalls;
    expect(call.id).toBe('call-0001');
    expect(call.name).toBe('web_search');
    expect(call.arguments).toEqual({ queries: ['2027 Boston Marathon date'] });
    const result = call.result as { search_result_groups: { entries: { url: string }[] }[] };
    expect(result.search_result_groups.flatMap((g) => g.entries.map((e) => e.url))).toEqual([
      'https://www.example.org/boston-2027',
    ]);
  });

  it('joins the segments in order when the stream has no merged message', () => {
    const frames = fixture('web-search-split').split('\n\n').filter((f) => !f.includes('"c":4}'));
    const parser = chatgptAdapter.createParser('req-0002');
    parser.onRequest('https://chatgpt.com/backend-api/f/conversation', 'POST', reqBody('x', null));
    parser.onChunk(frames.join('\n\n'));
    parser.onDone();
    expect(parser.getTurn()!.responseText).toBe(ANSWER);
  });
});
