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
// (path). We maintain a single "root" object and apply add/append/replace/patch
// to it, tracking the visible assistant message (author.role === 'assistant',
// recipient 'all') and accumulating its content/parts text.
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
  private reqId: number;
  private sessionId = '';
  private requestModel?: string;
  private promptText = '';
  private inputMessages: Msg[] = [];
  private startedAt = Date.now();
  private completedAt?: number;

  // delta_encoding v1 cursor + document state
  private lastOp = '';
  private lastPath = '';
  private root: any = null;
  /** Reference to the visible assistant message object (mutated in place by ops). */
  private assistantMsg: any = null;

  constructor(reqId: number) {
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

    // Delta op: resolve op/path with bare-value inheritance.
    if ('o' in obj) this.lastOp = obj.o;
    if ('p' in obj) this.lastPath = obj.p;
    this.apply(this.lastOp, this.lastPath, obj.v);
  }

  private apply(op: string, path: string, v: any): void {
    switch (op) {
      case 'add':
        if (path === '' || path === '/') {
          this.root = v;
          this.trackAssistant();
        } else {
          setAt(this.root, path, v);
        }
        break;
      case 'append':
        appendAt(this.root, path, v);
        break;
      case 'replace':
        setAt(this.root, path, v);
        break;
      case 'patch':
        if (Array.isArray(v)) for (const s of v) this.apply(s.o, s.p ?? '', s.v);
        break;
    }
  }

  /** After an add at root, record the visible assistant message reference. */
  private trackAssistant(): void {
    const m = this.root?.message;
    if (!m || m.author?.role !== 'assistant') return;
    if (m.recipient != null && m.recipient !== 'all') return; // tool/aside message
    if (!m.content || !Array.isArray(m.content.parts)) m.content = { parts: [''] };
    this.assistantMsg = m; // subsequent ops mutate this in place
  }

  getTurn(): ChatTurn | null {
    const m = this.assistantMsg;
    const responseText = m?.content?.parts?.filter((p: any) => typeof p === 'string').join('') ?? '';
    if (!responseText && !this.promptText) return null;

    const responseModel = m?.metadata?.model_slug ?? this.requestModel;
    const messageId: string = m?.id ?? '';
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
      toolCalls: extractToolCalls(m, messageId),
      usage: {}, // ChatGPT web does not stream token usage
      startedAt: this.startedAt,
      completedAt: this.completedAt,
      captureMode: 'sse',
    };
  }
}

/** Best-effort web-search/tool capture from the assistant message metadata. */
function extractToolCalls(m: any, messageId: string): ToolCall[] {
  const groups = m?.metadata?.search_result_groups;
  if (Array.isArray(groups) && groups.length > 0) {
    return [{ id: `${messageId}:search`, name: 'web_search', arguments: { search_result_groups: groups } }];
  }
  return [];
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
