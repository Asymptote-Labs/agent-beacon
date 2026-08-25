// The normalized event vocabulary, mirroring the beacon endpoint schema
// (pkg/asymptoteobserve/event.go) and the actions the collector's converter.go
// recognizes.

import type { SiteName } from './types.js';

export type EventAction =
  | 'prompt.submitted'
  | 'agent.response'
  | 'agent.response.completed'
  | 'tool.invoked'
  | 'tool.completed';

export type Severity = 'info' | 'low' | 'medium' | 'high' | 'critical';

/** Map an action to its event.category (matches the collector's inference). */
export function categoryFor(action: EventAction): string {
  if (action.startsWith('prompt.')) return 'prompt';
  if (action.startsWith('tool.')) return 'tool';
  if (action.startsWith('agent.')) return 'agent';
  return 'trace';
}

/** OTLP severity number for a severity text (INFO=9 per OTel spec). */
export function severityNumber(sev: Severity): number {
  switch (sev) {
    case 'info':
      return 9;
    case 'low':
      return 5;
    case 'medium':
      return 13;
    case 'high':
      return 17;
    case 'critical':
      return 21;
  }
}

/** gen_ai.provider.name for a site. */
export function providerFor(site: SiteName): string {
  return site === 'claude_web' ? 'anthropic' : 'openai';
}

/** service.name reported to the collector. */
export const SERVICE_NAME = 'agent-beacon-browser-collector';

/** beacon.origin value identifying browser-sourced telemetry. */
export const ORIGIN = 'browser-extension';

/**
 * Per-event byte cap matching the collector's max_event_bytes (64 KiB). We
 * truncate oversized string fields client-side and flag it, mirroring the
 * exporter's field_truncated contract.
 */
export const MAX_EVENT_BYTES = 65536;
/** Leave headroom for envelope/attribute overhead when capping field text. */
export const MAX_FIELD_BYTES = 48000;
