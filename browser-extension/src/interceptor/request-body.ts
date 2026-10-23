// Reads the body a page passes to fetch as text, for the MAIN-world
// interceptor. The prompt lives in that body, so a body this cannot read is a
// turn recorded with no prompt.
//
// claude.ai sends its completion body as a gzip-compressed Uint8Array with
// `Content-Encoding: gzip`, so a string-only read sees nothing. Bytes, Blobs
// and Request bodies are read here, and gzip or deflate is decompressed with
// the browser's own DecompressionStream.
//
// Everything that must happen before fetch consumes or the page reuses the
// body (copying bytes, cloning a Request) is done synchronously, in
// captureRequestBody. The decoding runs alongside the request instead of
// ahead of it, so the page's fetch is never delayed.

import { LIMITS } from '../shared/validate.js';

/** A decoded body longer than this would fail the service worker's request
 *  check and lose the whole turn, so it is dropped and only the prompt is lost. */
export const MAX_BODY_CHARS = LIMITS.body;

type Format = 'gzip' | 'deflate';

/**
 * Snapshot the request body now and decode it in the background. Never
 * throws and never rejects: anything it cannot read resolves to null.
 */
export function captureRequestBody(input: RequestInfo | URL, init?: RequestInit): Promise<string | null> {
  try {
    const body = init?.body;
    if (body != null) return fromBodyInit(body, contentEncoding(init?.headers, input));
    if (typeof Request !== 'undefined' && input instanceof Request && input.body != null) {
      // Clone before fetch consumes the original; read the clone later.
      const clone = input.clone();
      const encoding = contentEncoding(init?.headers, input);
      return clone
        .arrayBuffer()
        .then((buf) => decodeBytes(new Uint8Array(buf), encoding))
        .catch(() => null);
    }
  } catch {
    // A used Request, an invalid header list: nothing readable.
  }
  return Promise.resolve(null);
}

function fromBodyInit(body: BodyInit, encoding: string | null): Promise<string | null> {
  if (typeof body === 'string') return Promise.resolve(capChars(body));
  if (body instanceof URLSearchParams) return Promise.resolve(capChars(body.toString()));
  if (body instanceof ArrayBuffer) return decodeBytes(new Uint8Array(body.slice(0)), encoding);
  if (ArrayBuffer.isView(body)) {
    // Copy now: the page owns the buffer and may reuse it once fetch returns.
    const view = new Uint8Array(body.buffer, body.byteOffset, body.byteLength);
    return decodeBytes(view.slice(), encoding);
  }
  if (typeof Blob !== 'undefined' && body instanceof Blob) {
    // Blobs are immutable, so reading later sees what was sent.
    return body
      .arrayBuffer()
      .then((buf) => decodeBytes(new Uint8Array(buf), encoding))
      .catch(() => null);
  }
  // FormData and ReadableStream bodies cannot be read without consuming them.
  return Promise.resolve(null);
}

/** The request's Content-Encoding, from init.headers or else the Request's own. */
function contentEncoding(headers: HeadersInit | undefined, input: RequestInfo | URL): string | null {
  if (headers != null) return new Headers(headers).get('content-encoding');
  if (typeof Request !== 'undefined' && input instanceof Request) return input.headers.get('content-encoding');
  return null;
}

/**
 * Bytes to text: decompressed when the header (or, without one, the gzip
 * magic number) says so, UTF-8 otherwise. An encoding the browser cannot
 * decompress, a corrupt stream, or output over MAX_BODY_CHARS gives null.
 */
export async function decodeBytes(bytes: Uint8Array, encoding: string | null): Promise<string | null> {
  const format = compressionFormat(bytes, encoding);
  if (format === undefined) return null;
  try {
    if (format == null) return capChars(new TextDecoder().decode(bytes));
    return await decompress(bytes, format);
  } catch {
    return null;
  }
}

/** null for an uncompressed body, undefined for one that cannot be decoded. */
function compressionFormat(bytes: Uint8Array, encoding: string | null): Format | null | undefined {
  const enc = encoding?.trim().toLowerCase() ?? '';
  if (enc === 'gzip' || enc === 'x-gzip') return 'gzip';
  if (enc === 'deflate') return 'deflate';
  if (enc !== '' && enc !== 'identity') return undefined; // br, zstd, a chain of codings
  return bytes[0] === 0x1f && bytes[1] === 0x8b ? 'gzip' : null;
}

async function decompress(bytes: Uint8Array, format: Format): Promise<string | null> {
  if (typeof DecompressionStream === 'undefined') return null;
  const stream = new Blob([bytes as BlobPart]).stream().pipeThrough(new DecompressionStream(format));
  const reader = stream.getReader();
  const decoder = new TextDecoder();
  let text = '';
  let size = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    // UTF-8 never takes fewer bytes than UTF-16 code units, so a byte count
    // within the cap keeps the text within it too.
    size += value.byteLength;
    if (size > MAX_BODY_CHARS) {
      await reader.cancel().catch(() => {});
      return null;
    }
    text += decoder.decode(value, { stream: true });
  }
  return text + decoder.decode();
}

function capChars(text: string): string | null {
  return text.length > MAX_BODY_CHARS ? null : text;
}
