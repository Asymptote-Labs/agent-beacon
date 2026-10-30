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
  const parser = claudeAdapter.createParser('req-0001');
  parser.onRequest(URL, 'POST', JSON.stringify({ prompt, conversation_id: CONV }));
  const body = fixture(name);
  for (let i = 0; i < body.length; i += chunkSize) parser.onChunk(body.slice(i, i + chunkSize));
  parser.onDone();
  return parser;
}

describe('claude adapter — simple turn', () => {
  const turn = run('simple-turn', 'What is the capital of France?')!.getTurn()!;

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
});

// The Messages API breaks cache writes down by TTL under usage.cache_creation.
// The one-hour part is billed at 2x input against 1.25x for five-minute writes,
// so it is kept as the subset of cache_creation_input_tokens it is.
describe('claude adapter -- one-hour cache writes', () => {
  const turn = run('with-cache-ttl', 'What is the capital of France?')!.getTurn()!;

  it('captures the one-hour subset beside the write count', () => {
    expect(turn.usage?.cacheCreationInputTokens).toBe(20000);
    expect(turn.usage?.cacheCreation1hInputTokens).toBe(16000);
  });

  it('keeps the subset when message_delta repeats the counts without the breakdown', () => {
    // message_delta repeats the flat counts but not cache_creation; a payload
    // that omits a field must never clear it.
    expect(turn.usage?.outputTokens).toBe(214);
    expect(turn.usage?.cacheCreation1hInputTokens).toBe(16000);
  });

  it('reports no subset for a stream without the breakdown', () => {
    const plain = run('with-usage', 'What is the capital of France?')!.getTurn()!;
    expect(plain.usage?.cacheCreation1hInputTokens).toBeUndefined();
  });
});

describe('claude adapter -- partial stream usage', () => {
  it('reports no output tokens when the stream aborts before message_delta', () => {
    const parser = claudeAdapter.createParser('req-0001');
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

// web-search.sse is a sanitized October 2026 claude.ai capture: claude.ai runs
// web_search itself and streams each result as a tool_result block, between
// thinking blocks, pings and citation deltas.
describe('claude adapter -- server-side tool results', () => {
  const turn = run('web-search', 'Search the web for the latest NASA news.')!.getTurn()!;

  it('pairs each tool_result with the tool_use it answers', () => {
    expect(turn.toolCalls.map((t) => [t.id, t.name, t.arguments])).toEqual([
      ['toolu_search_1', 'web_search', { mode: 'standard', query: 'NASA news today' }],
      ['toolu_search_2', 'web_search', { mode: 'standard', query: 'NASA October 2026' }],
    ]);
    const [first, second] = turn.toolCalls.map((t) => t.result as { title: string }[]);
    expect(first.map((r) => r.title)).toEqual(['NASA News Today', 'Latest NASA Updates']);
    expect(second.map((r) => r.title)).toEqual(['Artemis Accords Signatories']);
  });

  it('keeps only the answer text, not thinking summaries or citations', () => {
    expect(turn.responseText).toBe('One recent NASA headline: a new country signed the Artemis Accords.');
  });

  it('takes the model and turn key from message_start', () => {
    expect(turn.responseModel).toBe('claude-opus-5-5');
    expect(turn.turnId).toBe(`${CONV}:00000000-0000-7000-8000-000000000002`);
    expect(turn.completedAt).toBeTypeOf('number');
  });

  it('ignores a tool_result that names no captured tool_use', () => {
    const parser = claudeAdapter.createParser('req-0002');
    parser.onRequest(URL, 'POST', JSON.stringify({ prompt: 'hi' }));
    parser.onChunk(
      'event: content_block_start\ndata: {"type":"content_block_start","index":0,"content_block":' +
        '{"type":"tool_result","tool_use_id":"toolu_missing","content":[{"type":"text","text":"x"}]}}\n\n',
    );
    parser.onDone();
    expect(parser.getTurn()!.toolCalls).toEqual([]);
  });
});

describe('claude adapter -- regenerate', () => {
  it('records no prompt for a retry_completion, which sends an empty one', () => {
    const parser = claudeAdapter.createParser('req-0003');
    parser.onRequest(URL.replace(/completion$/, 'retry_completion'), 'POST', JSON.stringify({ prompt: '', model: 'claude-opus-5-5' }));
    const body = fixture('simple-turn');
    parser.onChunk(body);
    parser.onDone();
    const turn = parser.getTurn()!;
    expect(turn.promptText).toBe('');
    expect(turn.inputMessages).toEqual([]);
    expect(turn.responseText).toBe('Paris.');
  });
});
