// Claude (claude.ai) adapter. Parses the streamed response into a ChatTurn.
//
// claude.ai streams Server-Sent Events whose `data:` payloads are Anthropic
// streaming objects keyed by `type`:
//   message_start { message: { model, usage:{input_tokens} } }
//   content_block_start { index, content_block:{ type:'text'|'tool_use', id?, name? } }
//   content_block_delta { index, delta:{ type:'text_delta', text } | { type:'input_json_delta', partial_json } }
//   content_block_stop { index }
//   message_delta { delta:{ stop_reason }, usage:{ output_tokens } }
//   message_stop
// A legacy shape ({ completion: "…delta" }) is also tolerated.
//
// The exact claude.ai envelope must be confirmed against recorded live traffic
// (the live-smoke drift alarm exists for exactly this); the parser is written
// defensively so minor wrapping differences degrade gracefully.

import type { ChatTurn, Msg, ToolCall } from '../shared/types.js';
import { registerAdapter, type SiteAdapter, type TurnParser } from './adapter.js';
import { SSEParser } from './sse.js';
import { turnId } from '../shared/ids.js';

class ClaudeParser implements TurnParser {
  private sse = new SSEParser();
  private sessionId = '';
  private model?: string;
  private promptText = '';
  private inputMessages: Msg[] = [];
  private responseText = '';
  private inputTokens?: number;
  private outputTokens?: number;
  private startedAt = Date.now();
  private completedAt?: number;
  private reqId: number;
  // tool_use blocks keyed by content-block index.
  private blocks = new Map<number, { type: string; tool?: ToolCall; argBuf: string }>();

  constructor(reqId: number) {
    this.reqId = reqId;
  }

  onRequest(url: string, _method: string, body: string | null): void {
    this.sessionId = conversationIdFromUrl(url) ?? this.sessionId;
    if (!body) return;
    try {
      const parsed = JSON.parse(body);
      const { text, messages } = extractPrompt(parsed);
      this.promptText = text;
      this.inputMessages = messages;
      if (typeof parsed?.model === 'string') this.model = parsed.model;
      if (typeof parsed?.conversation_id === 'string' && !this.sessionId)
        this.sessionId = parsed.conversation_id;
    } catch {
      // Non-JSON body — leave prompt empty; DOM fallback may fill it later.
    }
  }

  onChunk(chunk: string): void {
    for (const evt of this.sse.push(chunk)) this.handle(evt.data);
  }

  onDone(): void {
    for (const evt of this.sse.flush()) this.handle(evt.data);
    if (this.completedAt == null) this.completedAt = undefined; // stayed partial
  }

  private handle(data: string): void {
    if (!data || data === '[DONE]') return;
    let obj: any;
    try {
      obj = JSON.parse(data);
    } catch {
      return;
    }

    // Legacy delta shape.
    if (typeof obj.completion === 'string' && obj.type == null) {
      this.responseText += obj.completion;
      if (obj.stop_reason) this.completedAt = Date.now();
      return;
    }

    switch (obj.type) {
      case 'message_start': {
        const m = obj.message ?? {};
        if (typeof m.model === 'string') this.model = m.model;
        if (typeof m.id === 'string' && !this.sessionId) this.sessionId = m.id;
        if (typeof m.usage?.input_tokens === 'number') this.inputTokens = m.usage.input_tokens;
        break;
      }
      case 'content_block_start': {
        const cb = obj.content_block ?? {};
        const entry = { type: cb.type ?? 'text', argBuf: '' } as {
          type: string;
          tool?: ToolCall;
          argBuf: string;
        };
        if (cb.type === 'tool_use') {
          entry.tool = { id: String(cb.id ?? ''), name: String(cb.name ?? '') };
        }
        this.blocks.set(obj.index ?? this.blocks.size, entry);
        break;
      }
      case 'content_block_delta': {
        const d = obj.delta ?? {};
        if (d.type === 'text_delta' && typeof d.text === 'string') {
          this.responseText += d.text;
        } else if (d.type === 'input_json_delta' && typeof d.partial_json === 'string') {
          const entry = this.blocks.get(obj.index);
          if (entry) entry.argBuf += d.partial_json;
        }
        break;
      }
      case 'message_delta': {
        if (typeof obj.usage?.output_tokens === 'number') this.outputTokens = obj.usage.output_tokens;
        break;
      }
      case 'message_stop': {
        this.completedAt = Date.now();
        break;
      }
    }
  }

  getTurn(): ChatTurn | null {
    if (!this.responseText && this.blocks.size === 0 && !this.promptText) return null;
    const toolCalls: ToolCall[] = [];
    for (const entry of this.blocks.values()) {
      if (entry.tool) {
        let args: unknown = entry.argBuf;
        try {
          args = entry.argBuf ? JSON.parse(entry.argBuf) : undefined;
        } catch {
          /* keep raw string */
        }
        toolCalls.push({ ...entry.tool, arguments: args });
      }
    }
    const sessionId = this.sessionId || `claude-${this.reqId}`;
    return {
      site: 'claude_web',
      sessionId,
      turnId: turnId(sessionId, this.reqId),
      requestModel: this.model,
      responseModel: this.model,
      promptText: this.promptText,
      inputMessages: this.inputMessages,
      outputMessages: [{ role: 'assistant', text: this.responseText }],
      responseText: this.responseText,
      toolCalls,
      usage: { inputTokens: this.inputTokens, outputTokens: this.outputTokens },
      startedAt: this.startedAt,
      completedAt: this.completedAt,
      captureMode: 'sse',
    };
  }
}

function conversationIdFromUrl(url: string): string | undefined {
  const m = url.match(/chat_conversations\/([0-9a-f-]{16,})/i);
  return m?.[1];
}

/** Pull prompt text + input messages from a variety of request-body shapes. */
function extractPrompt(body: any): { text: string; messages: Msg[] } {
  const messages: Msg[] = [];
  let text = '';
  if (typeof body?.prompt === 'string') {
    text = body.prompt;
    messages.push({ role: 'user', text });
  } else if (Array.isArray(body?.messages)) {
    for (const m of body.messages) {
      const t = coerceContent(m?.content);
      messages.push({ role: normalizeRole(m?.role), text: t });
      if (m?.role === 'user') text = t;
    }
  }
  return { text, messages };
}

function coerceContent(content: unknown): string {
  if (typeof content === 'string') return content;
  if (Array.isArray(content)) {
    return content
      .map((p: any) => (typeof p === 'string' ? p : typeof p?.text === 'string' ? p.text : ''))
      .join('');
  }
  return '';
}

function normalizeRole(role: unknown): Msg['role'] {
  return role === 'assistant' || role === 'system' || role === 'tool' ? role : 'user';
}

export const claudeAdapter: SiteAdapter = {
  name: 'claude_web',
  matchesHost: (host) => host === 'claude.ai' || host.endsWith('.claude.ai'),
  matchesRequest: (url, method) =>
    /chat_conversations\/.+\/(completion|retry_completion)/.test(url) &&
    method.toUpperCase() === 'POST',
  createParser: () => new ClaudeParser(nextReqId()),
};

let reqCounter = 0;
function nextReqId(): number {
  return ++reqCounter;
}

registerAdapter(claudeAdapter);
