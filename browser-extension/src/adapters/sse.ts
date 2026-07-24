// Minimal, robust SSE frame parser. Handles chunks that split mid-line or
// mid-event (the common source of streaming bugs), multi-line `data:` fields,
// and CRLF. Site adapters feed raw chunks in; they get whole events out.

export interface SSEEvent {
  event: string; // the `event:` field, or 'message' if absent
  data: string; // concatenated `data:` lines (newline-joined)
}

export class SSEParser {
  private buffer = '';

  /** Push a raw chunk; returns any complete events it produced. */
  push(chunk: string): SSEEvent[] {
    this.buffer += chunk;
    const out: SSEEvent[] = [];
    // Events are separated by a blank line. Normalize CRLF first.
    const normalized = this.buffer.replace(/\r\n/g, '\n');
    const frames = normalized.split('\n\n');
    // The last element may be a partial frame — keep it buffered.
    this.buffer = frames.pop() ?? '';
    for (const frame of frames) {
      const evt = parseFrame(frame);
      if (evt) out.push(evt);
    }
    return out;
  }

  /** Flush any trailing buffered frame (call on stream end). */
  flush(): SSEEvent[] {
    if (!this.buffer.trim()) {
      this.buffer = '';
      return [];
    }
    const evt = parseFrame(this.buffer.replace(/\r\n/g, '\n'));
    this.buffer = '';
    return evt ? [evt] : [];
  }
}

function parseFrame(frame: string): SSEEvent | null {
  let event = 'message';
  const dataLines: string[] = [];
  for (const line of frame.split('\n')) {
    if (line === '' || line.startsWith(':')) continue; // blank or comment
    const idx = line.indexOf(':');
    const field = idx === -1 ? line : line.slice(0, idx);
    // Spec: strip one leading space after the colon.
    let value = idx === -1 ? '' : line.slice(idx + 1);
    if (value.startsWith(' ')) value = value.slice(1);
    if (field === 'event') event = value;
    else if (field === 'data') dataLines.push(value);
  }
  if (dataLines.length === 0 && event === 'message') return null;
  return { event, data: dataLines.join('\n') };
}
