// ChatGPT (chatgpt.com / chat.openai.com) adapter.
//
// ChatGPT streams the assistant turn as Server-Sent Events over `fetch` from
// POST /backend-api/f/conversation, using OpenAI's `delta_encoding: v1` format:
//   event: delta_encoding   data: "v1"
//   data: {"type":"resume_conversation_token", "conversation_id":"..."}
//   event: delta  data: {"p":"","o":"add","v":{"message":{...}}}      ← seed a message
//   event: delta  data: {"v":{"message":{...}}}                        ← bare: inherits o/p (add @ "")
//   event: delta  data: {"p":"/message/content/parts/0","o":"append","v":"tok"}
//   event: delta  data: {"v":"more"}                                   ← bare: inherits append @ path
//   event: delta  data: {"o":"patch","p":"","v":[{op},{op},...]}       ← batched ops
//   data: {"type":"message_stream_complete", ...}
//   data: [DONE]
//
// A frame carrying only `v` inherits the previous frame's `o` (op) and `p`
// (path). Each root `add` creates a message, numbered from 0 in stream order;
// a frame's `c` names the message it applies to and is inherited the same way.
// Ops for different messages interleave, so applying every op to the newest
// message duplicates text.
//
// One turn adds many messages: system and context messages, the assistant's
// call to a tool (recipient `web.run`), the tool's replies (author.role 'tool',
// with metadata.search_model_queries and metadata.search_result_groups),
// reasoning (`thoughts`, `reasoning_recap`), and the visible answer. An answer
// interrupted by a tool call streams one text message per segment and, when
// there are several, also a merged message (metadata.is_merged_message) whose
// text is all of them joined.
//
// NOTE: ChatGPT sometimes hands the stream off to a resume endpoint instead of
// streaming inline on this POST (a `stream_handoff` event). That resume stream
// is also SSE-over-fetch; capturing it is a documented follow-up.

import type { ChatTurn, Msg, ToolCall } from '../shared/types.js';
import { registerAdapter, type SiteAdapter, type TurnParser } from './adapter.js';
import { SSEParser, type SSEEvent } from './sse.js';
import { turnId } from '../shared/ids.js';

class ChatGptParser implements TurnParser {
  private sse = new SSEParser();
  private reqId: string;
  private sessionId = '';
  private requestModel?: string;
  private promptText = '';
  private inputMessages: Msg[] = [];
  private startedAt = Date.now();
  private completedAt?: number;

  // delta_encoding v1 cursor + document state
  private lastOp = '';
  private lastPath = '';
  private lastC = -1;
  private nextC = 0;
  /** Every message the stream added, keyed by its `c` index. */
  private messages = new Map<number, any>();

  constructor(reqId: string) {
    this.reqId = reqId;
  }

  onRequest(_url: string, _method: string, body: string | null): void {
    if (!body) return;
    try {
      const parsed = JSON.parse(body);
      if (typeof parsed?.conversation_id === 'string') this.sessionId = parsed.conversation_id;
      if (typeof parsed?.model === 'string') this.requestModel = parsed.model;
      const { text, messages } = extractPrompt(parsed);
      this.promptText = text;
      this.inputMessages = messages;
    } catch {
      /* non-JSON body — prompt stays empty */
    }
  }

  onChunk(chunk: string): void {
    for (const evt of this.sse.push(chunk)) this.handle(evt);
  }

  onDone(): void {
    for (const evt of this.sse.flush()) this.handle(evt);
  }

  private handle(evt: SSEEvent): void {
    const data = evt.data;
    if (!data) return;
    if (data === '[DONE]') {
      this.completedAt ??= Date.now();
      return;
    }
    if (evt.event === 'delta_encoding') return; // just declares "v1"

    let obj: any;
    try {
      obj = JSON.parse(data);
    } catch {
      return;
    }
    if (obj == null || typeof obj !== 'object') return;

    // Typed control events carry a `type` and are not delta ops.
    if (typeof obj.type === 'string' && !('v' in obj && (('o' in obj) || ('p' in obj)))) {
      switch (obj.type) {
        case 'resume_conversation_token':
          if (!this.sessionId && typeof obj.conversation_id === 'string')
            this.sessionId = obj.conversation_id;
          break;
        case 'message_stream_complete':
          this.completedAt ??= Date.now();
          break;
        // input_message / message_marker / title_generation / *_metadata → ignore
      }
      return;
    }

    // Delta op: resolve op, path and target message with bare-value inheritance.
    if ('o' in obj) this.lastOp = obj.o;
    if ('p' in obj) this.lastPath = obj.p;
    if (typeof obj.c === 'number') this.lastC = obj.c;
    if (this.lastOp === 'add' && (this.lastPath === '' || this.lastPath === '/')) {
      const c = typeof obj.c === 'number' ? obj.c : this.nextC;
      if (obj.v != null && typeof obj.v === 'object') this.messages.set(c, withTextParts(obj.v));
      this.lastC = c;
      this.nextC = Math.max(this.nextC, c + 1);
      return;
    }
    const root = this.messages.get(this.lastC);
    if (root) apply(root, this.lastOp, this.lastPath, obj.v);
  }

  getTurn(): ChatTurn | null {
    const messages = [...this.messages.entries()]
      .sort(([a], [b]) => a - b)
      .map(([, root]) => root?.message)
      .filter((m) => m != null && typeof m === 'object');
    const answer = answerMessages(messages);
    const responseText = answer.map(textOf).join('');
    if (!responseText && !this.promptText) return null;

    const m = answer[answer.length - 1];
    const responseModel = m?.metadata?.model_slug ?? this.requestModel;
    const messageId: string = typeof m?.id === 'string' ? m.id : '';
    const sessionId = this.sessionId || `chatgpt-unknown-${this.reqId}`;
    const turnKey = messageId || `r${this.reqId}`;

    return {
      site: 'chatgpt_web',
      sessionId,
      turnId: turnId(sessionId, turnKey),
      requestModel: this.requestModel,
      responseModel,
      promptText: this.promptText,
      inputMessages: this.inputMessages,
      outputMessages: [{ role: 'assistant', text: responseText }],
      responseText,
      toolCalls: extractToolCalls(messages, m, messageId),
      usage: {}, // ChatGPT web does not stream token usage
      startedAt: this.startedAt,
      completedAt: this.completedAt,
      captureMode: 'sse',
    };
  }
}

function apply(root: any, op: string, path: string, v: any): void {
  switch (op) {
    case 'add':
    case 'replace':
      setAt(root, path, v);
      break;
    case 'append':
      appendAt(root, path, v);
      break;
    case 'remove':
      removeAt(root, path);
      break;
    case 'patch':
      if (Array.isArray(v)) for (const s of v) apply(root, s?.o, s?.p ?? '', s?.v);
      break;
  }
}

/** Give an assistant text message with no parts an empty one, so the appends
 *  to /message/content/parts/0 that follow have somewhere to land. */
function withTextParts(root: any): any {
  const m = root.message;
  if (m && typeof m === 'object' && m.author?.role === 'assistant' && !Array.isArray(m.content?.parts)) {
    const type = m.content?.content_type ?? 'text';
    if (type === 'text') m.content = { ...m.content, content_type: 'text', parts: [''] };
  }
  return root;
}

/** The messages whose text is the visible answer: the merged message when
 *  the stream has one, otherwise every answer segment in order. */
function answerMessages(messages: any[]): any[] {
  const visible = messages.filter(isAnswerText);
  const merged = visible.filter((m) => m.metadata?.is_merged_message === true);
  return merged.length > 0 ? [merged[merged.length - 1]] : visible;
}

function isAnswerText(m: any): boolean {
  if (m.author?.role !== 'assistant') return false;
  if (m.recipient != null && m.recipient !== 'all') return false; // a call to a tool
  const type = m.content?.content_type;
  if (type !== 'text' && type !== 'multimodal_text') return false; // thoughts, context
  return m.channel == null || m.channel === 'final';
}

function textOf(m: any): string {
  const parts = m?.content?.parts;
  return Array.isArray(parts) ? parts.filter((p: unknown) => typeof p === 'string').join('') : '';
}

/**
 * Tool calls from the turn's messages. An assistant message addressed to a
 * tool opens a call; the tool's replies that follow fill in the search queries
 * it ran (the arguments) and the results it returned. The call message itself
 * carries no input for web.run.
 */
function extractToolCalls(messages: any[], answer: any, messageId: string): ToolCall[] {
  const calls: ToolCall[] = [];
  const open = new Map<string, ToolCall>();
  for (const m of messages) {
    const role = m.author?.role;
    if (role === 'assistant' && typeof m.recipient === 'string' && m.recipient !== 'all') {
      const text = textOf(m).trim();
      const call: ToolCall = { id: String(m.id ?? ''), name: toolName(m.recipient), arguments: text ? parseJson(text) : undefined };
      calls.push(call);
      open.set(m.recipient, call);
    } else if (role === 'tool') {
      const name = typeof m.author?.name === 'string' ? m.author.name : 'tool';
      let call = open.get(name);
      if (!call) {
        call = { id: String(m.id ?? ''), name: toolName(name) };
        calls.push(call);
        open.set(name, call);
      }
      absorbToolReply(call, m);
    }
  }
  if (calls.length > 0) return calls;
  // A stream that names no tool messages may still attach its search results
  // to the answer.
  const groups = answer?.metadata?.search_result_groups;
  if (Array.isArray(groups) && groups.length > 0) {
    return [{ id: `${messageId}:search`, name: 'web_search', result: { search_result_groups: groups } }];
  }
  return [];
}

function absorbToolReply(call: ToolCall, m: any): void {
  const queries = m.metadata?.search_model_queries?.queries;
  if (Array.isArray(queries) && queries.length > 0 && call.arguments === undefined) {
    call.arguments = { queries };
  }
  const groups = m.metadata?.search_result_groups;
  if (Array.isArray(groups) && groups.length > 0) {
    const prev = (call.result as { search_result_groups?: unknown[] } | undefined)?.search_result_groups ?? [];
    call.result = { search_result_groups: [...prev, ...groups] };
    return;
  }
  const text = textOf(m);
  if (text && call.result === undefined) call.result = text;
}

/** web.run is ChatGPT's web tool; it is recorded as web_search, the name the
 *  Claude adapter and the threat rules use for the same thing. */
function toolName(recipient: string): string {
  return recipient === 'web.run' || recipient === 'web' ? 'web_search' : recipient;
}

function parseJson(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return text;
  }
}

/** Navigate to the parent of `path` and return [container, key]. */
function locate(root: any, path: string): [any, string | number] | null {
  const segs = path.split('/').filter(Boolean);
  if (segs.length === 0) return null;
  let obj = root;
  for (let i = 0; i < segs.length - 1; i++) {
    if (obj == null) return null;
    const k = segs[i];
    obj = Array.isArray(obj) ? obj[Number(k)] : obj[k];
  }
  if (obj == null) return null;
  const last = segs[segs.length - 1];
  return [obj, Array.isArray(obj) ? Number(last) : last];
}

function appendAt(root: any, path: string, v: any): void {
  const loc = locate(root, path);
  if (!loc) return;
  const [obj, key] = loc;
  const cur = (obj as any)[key];
  if (typeof v === 'string') {
    (obj as any)[key] = (typeof cur === 'string' ? cur : '') + v;
  } else if (Array.isArray(v) && Array.isArray(cur)) {
    (obj as any)[key] = [...cur, ...v];
  } else if (v && typeof v === 'object' && !Array.isArray(v) && cur && typeof cur === 'object') {
    (obj as any)[key] = { ...cur, ...v }; // merge (e.g. metadata) — preserves model_slug
  } else {
    (obj as any)[key] = v;
  }
}

function setAt(root: any, path: string, v: any): void {
  const loc = locate(root, path);
  if (!loc) return;
  const [obj, key] = loc;
  (obj as any)[key] = v;
}

function removeAt(root: any, path: string): void {
  const loc = locate(root, path);
  if (!loc) return;
  const [obj, key] = loc;
  if (Array.isArray(obj) && typeof key === 'number') obj.splice(key, 1);
  else delete (obj as any)[key];
}

function extractPrompt(body: any): { text: string; messages: Msg[] } {
  const messages: Msg[] = [];
  let text = '';
  for (const m of Array.isArray(body?.messages) ? body.messages : []) {
    const role = normalizeRole(m?.author?.role ?? m?.role);
    const t = coerceParts(m?.content);
    messages.push({ role, text: t });
    if (role === 'user') text = t;
  }
  return { text, messages };
}

function coerceParts(content: any): string {
  if (typeof content === 'string') return content;
  const parts = content?.parts;
  if (Array.isArray(parts)) {
    return parts.map((p: any) => (typeof p === 'string' ? p : typeof p?.text === 'string' ? p.text : '')).join('');
  }
  return '';
}

function normalizeRole(role: unknown): Msg['role'] {
  return role === 'assistant' || role === 'system' || role === 'tool' ? role : 'user';
}

export const chatgptAdapter: SiteAdapter = {
  name: 'chatgpt_web',
  matchesHost: (host) =>
    host === 'chatgpt.com' ||
    host.endsWith('.chatgpt.com') ||
    host === 'chat.openai.com' ||
    host.endsWith('.chat.openai.com'),
  // The message-send endpoint is a POST to /backend-(api|anon)/(f/)?conversation
  // exactly — NOT sub-resources like /conversation/init or /conversation/{id}.
  matchesRequest: (url, method) =>
    /\/backend-(api|anon)\/(f\/)?conversation(?:\?|$)/.test(url) && method.toUpperCase() === 'POST',
  createParser: (reqId) => new ChatGptParser(reqId),
};

registerAdapter(chatgptAdapter);
