// Helpers to read OTLP log envelopes captured by the mock collector.

import type { KeyValue, LogRecord, LogsEnvelope } from '../../src/shared/otlp.js';
import { attrValue } from '../../src/shared/otlp.js';

export function flatAttrs(attrs: KeyValue[]): Record<string, string | number | boolean> {
  const out: Record<string, string | number | boolean> = {};
  for (const a of attrs) {
    const v = attrValue(attrs, a.key);
    if (v !== undefined) out[a.key] = v;
  }
  return out;
}

export function resourceAttrs(env: LogsEnvelope): Record<string, string | number | boolean> {
  return flatAttrs(env.resourceLogs[0]?.resource.attributes ?? []);
}

/** All log records across all captured envelopes. */
export function allRecords(envs: LogsEnvelope[]): LogRecord[] {
  return envs.flatMap((e) => e.resourceLogs.flatMap((rl) => rl.scopeLogs.flatMap((sl) => sl.logRecords)));
}

/** Find the first record whose beacon.event.action matches. */
export function recordByAction(envs: LogsEnvelope[], action: string): LogRecord | undefined {
  return allRecords(envs).find((r) => flatAttrs(r.attributes)['beacon.event.action'] === action);
}
