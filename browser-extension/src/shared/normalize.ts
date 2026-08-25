// PURE: ChatTurn -> normalized OTLP log records for the beacon collector.
// No I/O, no globals, no chrome.* — this is the primary unit-tested seam.
//
// The collector's beaconjson exporter fills vendor/product/schema_version/
// endpoint/user/timestamp itself, so we MUST NOT emit those. We emit the
// beacon.* + gen_ai.* attributes that converter.go maps into the Event schema.

import type { ChatTurn, Retention, ToolCall } from './types.js';
import {
  type KeyValue,
  type LogRecord,
  buildLogsEnvelope,
  bool,
  int,
  msToUnixNano,
  str,
} from './otlp.js';
import {
  type EventAction,
  MAX_FIELD_BYTES,
  ORIGIN,
  SERVICE_NAME,
  categoryFor,
  providerFor,
  severityNumber,
} from './vocab.js';
import { recordId, turnId } from './ids.js';

export interface NormalizedTurn {
  resourceAttributes: KeyValue[];
  logRecords: LogRecord[];
}

const encoder = new TextEncoder();

/** Truncate a string to a UTF-8 byte budget; returns [text, wasTruncated]. */
function capBytes(text: string, max = MAX_FIELD_BYTES): [string, boolean] {
  if (encoder.encode(text).length <= max) return [text, false];
  // Binary-search the largest prefix that fits (chars ≠ bytes for multibyte).
  let lo = 0;
  let hi = text.length;
  while (lo < hi) {
    const mid = (lo + hi + 1) >> 1;
    if (encoder.encode(text.slice(0, mid)).length <= max) lo = mid;
    else hi = mid - 1;
  }
  return [text.slice(0, lo), true];
}

/** Light client-side scrubber for retention=redacted (defense in depth; the
 *  collector also redacts). Masks emails and long token-like strings. */
function scrub(text: string): string {
  return text
    .replace(/[\w.+-]+@[\w-]+\.[\w.-]+/g, '[email]')
    .replace(/\b(sk|pk|ghp|xox[baprs]|AKIA)[A-Za-z0-9_-]{12,}\b/g, '[token]');
}

interface Shaped {
  text: string;
  truncated: boolean;
}

/** Apply retention + size cap to a text field. Returns null when the field
 *  must be dropped entirely (metadata retention). */
function shapeText(text: string, retention: Retention): Shaped | null {
  if (retention === 'metadata') return null;
  const shown = retention === 'redacted' ? scrub(text) : text;
  const [capped, truncated] = capBytes(shown);
  return { text: capped, truncated };
}

/**
 * Normalize a chat turn into 1..N OTLP log records:
 *  - one response record (agent.response.completed, or agent.response if partial)
 *  - one tool.invoked record per captured tool call
 */
export function normalizeTurn(turn: ChatTurn, retention: Retention): NormalizedTurn {
  const resourceAttributes: KeyValue[] = [
    str('beacon.origin', ORIGIN),
    str('beacon.harness.name', turn.site),
    str('service.name', SERVICE_NAME),
    str('gen_ai.provider.name', providerFor(turn.site)),
  ];

  const tid = turn.turnId || turnId(turn.sessionId, 0);
  const startNano = msToUnixNano(turn.startedAt);
  const endNano = msToUnixNano(turn.completedAt ?? turn.startedAt);
  const logRecords: LogRecord[] = [];

  // Correlation attrs shared by every record in the turn.
  const base = (action: EventAction, index = 0): KeyValue[] => [
    str('beacon.event.action', action),
    str('beacon.event.category', categoryFor(action)),
    str('beacon.session.id', turn.sessionId),
    str('gen_ai.conversation.id', turn.sessionId),
    str('beacon.content.retention', retention),
    str('beacon.record.id', recordId(tid, action, index)),
    str('beacon.capture.mode', turn.captureMode),
  ];

  // ---- prompt.submitted ----
  // Emitted as its own event with category "prompt" so the collector's
  // converter lifts the text into the top-level Event.prompt.text field (it
  // only does so for prompt-category events). Without this, the prompt stays
  // buried in raw.attributes and never reaches the queryable prompt.text column.
  const prompt = shapeText(turn.promptText, retention);
  const inputMsgs = shapeText(JSON.stringify(turn.inputMessages), retention);
  if (prompt || inputMsgs) {
    const attrs = base('prompt.submitted');
    if (turn.requestModel) attrs.push(str('gen_ai.request.model', turn.requestModel));
    let truncated = false;
    if (prompt) {
      attrs.push(str('beacon.prompt.text', prompt.text));
      truncated ||= prompt.truncated;
    }
    if (inputMsgs) {
      attrs.push(str('gen_ai.input.messages', inputMsgs.text));
      truncated ||= inputMsgs.truncated;
    }
    if (truncated) attrs.push(bool('beacon.field_truncated', true));
    logRecords.push({
      timeUnixNano: startNano,
      severityNumber: severityNumber('info'),
      severityText: 'INFO',
      body: { stringValue: `${turn.site} prompt.submitted` },
      attributes: attrs,
    });
  }

  // ---- agent.response(.completed) ----
  const completed = turn.completedAt != null;
  const respAction: EventAction = completed ? 'agent.response.completed' : 'agent.response';
  const respAttrs = base(respAction);
  if (turn.requestModel) respAttrs.push(str('gen_ai.request.model', turn.requestModel));
  if (turn.responseModel) respAttrs.push(str('gen_ai.response.model', turn.responseModel));
  if (turn.usage?.inputTokens != null)
    respAttrs.push(int('gen_ai.usage.input_tokens', turn.usage.inputTokens));
  if (turn.usage?.outputTokens != null)
    respAttrs.push(int('gen_ai.usage.output_tokens', turn.usage.outputTokens));

  let respTrunc = false;
  const outputMsgs = shapeText(JSON.stringify(turn.outputMessages), retention);
  if (outputMsgs) {
    respAttrs.push(str('gen_ai.output.messages', outputMsgs.text));
    respTrunc ||= outputMsgs.truncated;
  }
  // Always-available metadata (survives metadata retention).
  respAttrs.push(int('beacon.response.chars', turn.responseText.length));
  respAttrs.push(int('beacon.tool_calls.count', turn.toolCalls.length));
  if (respTrunc) respAttrs.push(bool('beacon.field_truncated', true));

  logRecords.push({
    timeUnixNano: endNano,
    severityNumber: severityNumber('info'),
    severityText: 'INFO',
    body: { stringValue: `${turn.site} ${respAction}` },
    attributes: respAttrs,
  });

  // ---- tool.invoked ----
  turn.toolCalls.forEach((tool, i) => {
    logRecords.push(toolRecord(turn, tool, i, retention, endNano));
  });

  return { resourceAttributes, logRecords };
}

function toolRecord(
  turn: ChatTurn,
  tool: ToolCall,
  index: number,
  retention: Retention,
  when: string,
): LogRecord {
  const tid = turn.turnId || turnId(turn.sessionId, 0);
  const action: EventAction = 'tool.invoked';
  const attrs: KeyValue[] = [
    str('beacon.event.action', action),
    str('beacon.event.category', categoryFor(action)),
    str('beacon.session.id', turn.sessionId),
    str('gen_ai.conversation.id', turn.sessionId),
    str('beacon.content.retention', retention),
    str('beacon.record.id', recordId(tid, action, index)),
    str('tool.name', tool.name),
    str('gen_ai.tool.call.id', tool.id),
    str('gen_ai.tool.call.name', tool.name),
  ];
  const args = shapeText(stringify(tool.arguments), retention);
  if (args) attrs.push(str('gen_ai.tool.call.arguments', args.text));
  if (tool.result !== undefined) {
    const res = shapeText(stringify(tool.result), retention);
    if (res) attrs.push(str('gen_ai.tool.call.result', res.text));
  }
  return {
    timeUnixNano: when,
    severityNumber: severityNumber('info'),
    severityText: 'INFO',
    body: { stringValue: `${turn.site} ${action} ${tool.name}` },
    attributes: attrs,
  };
}

function stringify(v: unknown): string {
  return typeof v === 'string' ? v : JSON.stringify(v ?? null);
}

/** Convenience: normalize straight into a single OTLP logs envelope. */
export function turnToEnvelope(turn: ChatTurn, retention: Retention) {
  const { resourceAttributes, logRecords } = normalizeTurn(turn, retention);
  return buildLogsEnvelope(resourceAttributes, logRecords, SERVICE_NAME);
}
