import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { claudeAdapter } from '../../src/adapters/claude.js';
import type { TurnParser } from '../../src/adapters/adapter.js';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const fixture = (name: string) =>
  readFileSync(path.join(__dirname, '..', '..', 'fixtures', 'claude', `${name}.sse`), 'utf8');

const CONV = '11111111-2222-3333-4444-555555555555';
const URL = `/api/organizations/org-x/chat_conversations/${CONV}/completion`;

/** Feed a body through a fresh parser in tiny, boundary-crossing chunks. */
function run(name: string, prompt: string, chunkSize = 7): TurnParser {
  const parser = claudeAdapter.createParser(1);
  parser.onRequest(URL, 'POST', JSON.stringify({ prompt, conversation_id: CONV }));
  const body = fixture(name);
  for (let i = 0; i < body.length; i += chunkSize) parser.onChunk(body.slice(i, i + chunkSize));
  parser.onDone();
  return parser;
}

describe('claude adapter — simple turn', () => {
  const turn = run('simple-turn', 'What is the capital of France?')!.getTurn()!;

  it('reconstructs the streamed response text across split chunks', () => {
    expect(turn.responseText).toBe('Paris.');
  });
  it('captures the prompt from the request body', () => {
    expect(turn.promptText).toBe('What is the capital of France?');
  });
  it('captures the conversation id from the URL', () => {
    expect(turn.sessionId).toBe(CONV);
  });
  it('captures the model', () => {
    expect(turn.responseModel).toBe('claude-opus-4-8');
  });
  it('leaves every usage count undefined when the stream carries no usage object', () => {
    // This fixture is recorded claude.ai traffic, and it carries no `usage` at
    // all. Unknown must stay unknown rather than becoming zero: a zero would sum
    // into token rollups as a real observation of "this turn cost nothing".
    expect(turn.usage?.inputTokens).toBeUndefined();
    expect(turn.usage?.outputTokens).toBeUndefined();
    expect(turn.usage?.cacheCreationInputTokens).toBeUndefined();
    expect(turn.usage?.cacheReadInputTokens).toBeUndefined();
  });
  it('marks the turn completed', () => {
    expect(turn.completedAt).toBeTypeOf('number');
    expect(turn.toolCalls).toHaveLength(0);
  });
});

describe('claude adapter — tool call', () => {
  const turn = run('with-tool-call', 'What is the weather today?')!.getTurn()!;

  it('captures the tool call with assembled JSON arguments', () => {
    expect(turn.toolCalls).toHaveLength(1);
    expect(turn.toolCalls[0].name).toBe('web_search');
    expect(turn.toolCalls[0].id).toBe('toolu_abc');
    expect(turn.toolCalls[0].arguments).toEqual({ query: 'weather today' });
  });
  it('still captures the accompanying text', () => {
    expect(turn.responseText).toBe('Let me check the weather.');
  });
});

describe('claude adapter — chunk-size invariance', () => {
  it('produces the same response text regardless of chunk boundaries', () => {
    const texts = [1, 3, 13, 100, 5000].map(
      (n) => run('simple-turn', 'x', n).getTurn()!.responseText,
    );
    for (const t of texts) expect(t).toBe('Paris.');
  });
});

// The usage fixture is synthetic, not recorded traffic: no claude.ai capture on
// hand carries a `usage` object (see the simple-turn assertions above). It pins
// the Anthropic streaming shape the parser is written against, so that if and
// when claude.ai does start sending counts, the four fields land in the right
// places instead of silently going to input_tokens or nowhere.
describe('claude adapter -- usage counts', () => {
  const turn = run('with-usage', 'What is the capital of France?')!.getTurn()!;

  it('captures uncached input and both cache counts from message_start', () => {
    expect(turn.usage?.inputTokens).toBe(37);
    expect(turn.usage?.cacheCreationInputTokens).toBe(1024);
    expect(turn.usage?.cacheReadInputTokens).toBe(18500);
  });

  it('takes output_tokens from message_delta, not the message_start placeholder', () => {
    // message_start reports output_tokens:1 as a placeholder. Reading it there
    // would report 1 output token for any stream that aborts before
    // message_delta, which is worse than reporting nothing.
    expect(turn.usage?.outputTokens).toBe(214);
  });

  it('keeps the input counts disjoint so a total never double-counts', () => {
    // Anthropic excludes cached reads and cache writes from input_tokens, which
    // is the disjointness gen_ai.usage requires; nothing is subtracted here the
    // way the Codex turn span needs.
    const u = turn.usage!;
    const total =
      u.inputTokens! + u.outputTokens! + u.cacheCreationInputTokens! + u.cacheReadInputTokens!;
    expect(total).toBe(19775);
  });
});

describe('claude adapter -- partial stream usage', () => {
  it('reports no output tokens when the stream aborts before message_delta', () => {
    const parser = claudeAdapter.createParser(1);
    parser.onRequest(URL, 'POST', JSON.stringify({ prompt: 'hi', conversation_id: CONV }));
    const body = fixture('with-usage');
    parser.onChunk(body.slice(0, body.indexOf('event: message_delta')));
    parser.onDone();
    const turn = parser.getTurn()!;
    expect(turn.usage?.outputTokens).toBeUndefined();
    // The input side is already final at message_start, so it survives the abort.
    expect(turn.usage?.inputTokens).toBe(37);
    expect(turn.usage?.cacheReadInputTokens).toBe(18500);
  });
});
