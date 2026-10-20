import { describe, expect, it } from 'vitest';
import { SSEParser } from '../../src/adapters/sse';

describe('SSE line endings', () => {
  const expected = [{ event: 'token', data: 'first\nsecond' }, { event: 'message', data: 'last' }];
  for (const ending of ['\n', '\r\n', '\r']) {
    it(`dispatches complete events for ${JSON.stringify(ending)} at every chunk boundary`, () => {
      // HTML event-stream grammar permits CR, LF, and CRLF line endings.
      const stream = ['event: token', 'data: first', 'data: second', '', 'data: last', '', ''].join(ending);
      for (let split = 0; split <= stream.length; split++) {
        const parser = new SSEParser();
        expect([...parser.push(stream.slice(0, split)), ...parser.push(''), ...parser.push(stream.slice(split))]).toEqual(expected);
        expect(parser.flush()).toEqual([]);
      }
    });
  }
  it('accepts mixed legal endings one character at a time without duplicating CRLF boundaries', () => {
    const parser = new SSEParser();
    const events = [...'event: token\rdata: first\r\ndata: second\n\rdata: last\r\n\r\n'].flatMap(c => parser.push(c));
    expect(events).toEqual(expected);
    expect(parser.flush()).toEqual([]);
  });
});
