import { describe, it, expect } from 'vitest';
import { gzipSync, deflateSync, brotliCompressSync } from 'node:zlib';
import { captureRequestBody, decodeBytes, MAX_BODY_CHARS } from '../../src/interceptor/request-body.js';

const BODY = JSON.stringify({ prompt: 'What is the capital of France? Café ☕', model: 'claude-opus-5-5' });
const URL = 'https://claude.ai/api/organizations/o/chat_conversations/c/completion';
const gzipped = () => new Uint8Array(gzipSync(BODY));

describe('captureRequestBody', () => {
  it('reads a string body as is', async () => {
    expect(await captureRequestBody(URL, { method: 'POST', body: BODY })).toBe(BODY);
  });

  // What claude.ai sends as of October 2026.
  it('decompresses a gzip Uint8Array body sent with Content-Encoding: gzip', async () => {
    const init = { method: 'POST', body: gzipped(), headers: { 'Content-Type': 'application/json', 'Content-Encoding': 'gzip' } };
    expect(await captureRequestBody(URL, init)).toBe(BODY);
  });

  it('recognizes gzip by its magic number when the header is missing', async () => {
    expect(await captureRequestBody(URL, { method: 'POST', body: gzipped() })).toBe(BODY);
  });

  it('decompresses deflate when the header names it', async () => {
    const body = new Uint8Array(deflateSync(BODY));
    expect(await captureRequestBody(URL, { method: 'POST', body, headers: [['content-encoding', 'deflate']] })).toBe(BODY);
  });

  it('reads uncompressed bytes, ArrayBuffers, DataViews and Blobs as UTF-8', async () => {
    const bytes = new TextEncoder().encode(BODY);
    expect(await captureRequestBody(URL, { method: 'POST', body: bytes })).toBe(BODY);
    expect(await captureRequestBody(URL, { method: 'POST', body: bytes.buffer.slice(0) })).toBe(BODY);
    expect(await captureRequestBody(URL, { method: 'POST', body: new DataView(bytes.buffer.slice(0)) })).toBe(BODY);
    expect(await captureRequestBody(URL, { method: 'POST', body: new Blob([gzipped()]), headers: { 'content-encoding': 'gzip' } })).toBe(BODY);
  });

  it('reads only the viewed slice of a larger buffer', async () => {
    const bytes = new TextEncoder().encode(`xx${BODY}yy`);
    expect(await captureRequestBody(URL, { method: 'POST', body: bytes.subarray(2, bytes.length - 2) })).toBe(BODY);
  });

  it('copies the bytes before the page can reuse its buffer', async () => {
    const bytes = gzipped();
    const pending = captureRequestBody(URL, { method: 'POST', body: bytes });
    bytes.fill(0);
    expect(await pending).toBe(BODY);
  });

  it('reads a compressed body from a Request, leaving the original unread for fetch', async () => {
    const request = new Request(URL, { method: 'POST', body: gzipped(), headers: { 'content-encoding': 'gzip' } });
    expect(await captureRequestBody(request)).toBe(BODY);
    expect(request.bodyUsed).toBe(false);
  });

  it('reads URLSearchParams as a form string', async () => {
    expect(await captureRequestBody(URL, { method: 'POST', body: new URLSearchParams({ a: '1' }) })).toBe('a=1');
  });

  it('gives null for bodies it cannot read without consuming them, or no body', async () => {
    const form = new FormData();
    form.set('prompt', 'hi');
    expect(await captureRequestBody(URL, { method: 'POST', body: form })).toBeNull();
    expect(await captureRequestBody(URL, { method: 'POST', body: new Blob([BODY]).stream(), duplex: 'half' } as RequestInit)).toBeNull();
    expect(await captureRequestBody(URL)).toBeNull();
    expect(await captureRequestBody(new Request(URL))).toBeNull();
  });

  it('gives null, never throws, for a Request whose body was already read', async () => {
    const request = new Request(URL, { method: 'POST', body: BODY });
    await request.text();
    expect(await captureRequestBody(request)).toBeNull();
  });
});

describe('decodeBytes', () => {
  it('gives null for a coding the browser cannot decompress', async () => {
    expect(await decodeBytes(new Uint8Array(brotliCompressSync(BODY)), 'br')).toBeNull();
    expect(await decodeBytes(gzipped(), 'gzip, br')).toBeNull();
  });

  it('gives null for a corrupt gzip stream', async () => {
    const bytes = gzipped().slice(0, 12);
    expect(await decodeBytes(bytes, 'gzip')).toBeNull();
  });

  // The service worker refuses a request event over the limit, which would lose
  // the whole turn, so an oversized body is dropped and only the prompt is lost.
  it('gives null for a body that decompresses past the limit', async () => {
    const bomb = new Uint8Array(gzipSync(Buffer.alloc(MAX_BODY_CHARS + 1, 0x61)));
    expect(bomb.length).toBeLessThan(64 * 1024);
    expect(await decodeBytes(bomb, 'gzip')).toBeNull();
    expect(await decodeBytes(new Uint8Array(gzipSync(Buffer.alloc(1024, 0x61))), 'gzip')).toBe('a'.repeat(1024));
  });

  it('gives null for an uncompressed string over the limit', async () => {
    expect(await captureRequestBody(URL, { method: 'POST', body: 'a'.repeat(MAX_BODY_CHARS + 1) })).toBeNull();
  });
});
