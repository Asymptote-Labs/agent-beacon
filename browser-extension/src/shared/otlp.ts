// OTLP/HTTP JSON (logs) envelope builders + attribute coercion.
// Reference: OpenTelemetry OTLP/JSON encoding. int64 values are encoded as
// strings ("intValue": "42") per the proto3 JSON mapping — the collector and
// our tests both rely on this.

export type AnyValue =
  | { stringValue: string }
  | { intValue: string }
  | { doubleValue: number }
  | { boolValue: boolean };

export interface KeyValue {
  key: string;
  value: AnyValue;
}

export interface LogRecord {
  timeUnixNano: string;
  severityNumber: number;
  severityText: string;
  body: AnyValue;
  attributes: KeyValue[];
}

export interface LogsEnvelope {
  resourceLogs: Array<{
    resource: { attributes: KeyValue[] };
    scopeLogs: Array<{
      scope: { name: string };
      logRecords: LogRecord[];
    }>;
  }>;
}

export function str(key: string, value: string): KeyValue {
  return { key, value: { stringValue: value } };
}

/** int64 attribute — encoded as a string per OTLP/JSON. */
export function int(key: string, value: number): KeyValue {
  return { key, value: { intValue: String(Math.trunc(value)) } };
}

export function bool(key: string, value: boolean): KeyValue {
  return { key, value: { boolValue: value } };
}

/** epoch milliseconds → OTLP nanosecond timestamp string. */
export function msToUnixNano(ms: number): string {
  return String(Math.trunc(ms)) + '000000';
}

/** Look up a single attribute's primitive value (used by tests/delivery). */
export function attrValue(attrs: KeyValue[], key: string): string | number | boolean | undefined {
  const kv = attrs.find((a) => a.key === key);
  if (!kv) return undefined;
  const v = kv.value;
  if ('stringValue' in v) return v.stringValue;
  if ('intValue' in v) return Number(v.intValue);
  if ('doubleValue' in v) return v.doubleValue;
  if ('boolValue' in v) return v.boolValue;
  return undefined;
}

export function buildLogsEnvelope(
  resourceAttributes: KeyValue[],
  logRecords: LogRecord[],
  scopeName: string,
): LogsEnvelope {
  return {
    resourceLogs: [
      {
        resource: { attributes: resourceAttributes },
        scopeLogs: [{ scope: { name: scopeName }, logRecords }],
      },
    ],
  };
}
