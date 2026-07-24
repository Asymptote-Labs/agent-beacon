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
  it('leaves usage tokens undefined (claude.ai does not stream them)', () => {
    expect(turn.usage?.inputTokens).toBeUndefined();
    expect(turn.usage?.outputTokens).toBeUndefined();
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
