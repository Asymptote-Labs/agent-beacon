import { context, trace, SpanStatusCode } from "@opentelemetry/api";
import { InMemorySpanExporter } from "@opentelemetry/sdk-trace-base";
import { afterEach, beforeEach, expect, it } from "vitest";
import { flush, initialize, observe, shutdown, wrapClaudeAgentQuery } from "./index.js";

let exporter: InMemorySpanExporter;

beforeEach(() => {
  exporter = new InMemorySpanExporter();
  initialize({ spanExporter: exporter, disableDefaultExporter: true, disableBatch: true, disableInstrumentations: true });
});

afterEach(async () => { await shutdown(); });

it("exports a streamed step when its consumer breaks, after running cleanup", async () => {
  let cleaned = false;
  const step = observe({ name: "agent.stream" }, async function* () {
    try { yield "first"; yield "second"; }
    finally { cleaned = true; }
  });
  for await (const value of step()) { expect(value).toBe("first"); break; }
  await flush();
  expect(cleaned).toBe(true);
  expect(exporter.getFinishedSpans()).toHaveLength(1);
  expect(exporter.getFinishedSpans()[0].status.code).toBe(SpanStatusCode.OK);
});

it("exports a Claude query explicitly returned before its first next", async () => {
  const query = wrapClaudeAgentQuery(async function* (_prompt: string) { yield "first"; });
  const result = await query("synthetic prompt").return(undefined);
  expect(result.done).toBe(true);
  await flush();
  expect(exporter.getFinishedSpans()).toHaveLength(1);
});

it("keeps a promised stream open until consumption finishes", async () => {
  const step = observe({ name: "agent.promised-stream" }, async () => (async function* () { yield 7; })());
  const stream = await step();
  await flush();
  expect(exporter.getFinishedSpans()).toHaveLength(0);
  expect(await stream.next()).toEqual({ value: 7, done: false });
  expect(await stream.next()).toEqual({ value: undefined, done: true });
  await flush();
  expect(exporter.getFinishedSpans()).toHaveLength(1);
});

it("runs deferred generator work and cleanup in the observed span context", async () => {
  const activeIds: string[] = [];
  const active = () => activeIds.push(trace.getSpan(context.active())?.spanContext().spanId ?? "missing");
  const step = observe({ name: "agent.context-stream" }, async function* () {
    try { active(); yield 1; }
    finally { active(); }
  });
  const stream = step();
  await stream.next();
  await stream.next();
  await flush();
  const spans = exporter.getFinishedSpans();
  expect(spans).toHaveLength(1);
  expect(activeIds).toEqual([spans[0].spanContext().spanId, spans[0].spanContext().spanId]);
  expect(trace.getSpan(context.active())).toBeUndefined();
});

it("exports an error once when the underlying stream fails", async () => {
  const failure = new Error("synthetic stream failure");
  const step = observe({ name: "agent.failed-stream" }, async function* () { yield 1; throw failure; });
  const stream = step();
  await stream.next();
  await expect(stream.next()).rejects.toBe(failure);
  await flush();
  expect(exporter.getFinishedSpans()).toHaveLength(1);
  expect(exporter.getFinishedSpans()[0].status).toEqual({ code: SpanStatusCode.ERROR, message: failure.message });
});

it("closes an iterable without throw support when its consumer throws", async () => {
  let cleaned = false;
  const failure = new Error("synthetic consumer failure");
  const step = observe({ name: "agent.custom-stream" }, () => ({
    [Symbol.asyncIterator]() {
      return {
        next: async () => ({ value: 1, done: false }),
        return: async () => { cleaned = true; return { value: undefined, done: true }; },
      };
    },
  }));
  // observe already supplies an iterator wrapper for async iterables.
  const stream = step() as unknown as AsyncIterableIterator<number>;
  await stream.next();
  await expect(stream.throw!(failure)).rejects.toBe(failure);
  await flush();
  expect(cleaned).toBe(true);
  expect(exporter.getFinishedSpans()).toHaveLength(1);
  expect(exporter.getFinishedSpans()[0].status.code).toBe(SpanStatusCode.ERROR);
});

it("preserves sent values and the underlying generator's terminal value", async () => {
  const step = observe({ name: "agent.protocol-stream" }, async function* () {
    const value: unknown = yield "ready";
    return value;
  });
  const stream = step();
  expect(await stream.next()).toEqual({ value: "ready", done: false });
  expect(await stream.next("answer")).toEqual({ value: "answer", done: true });
  await stream.next();
  await flush();
  expect(exporter.getFinishedSpans()).toHaveLength(1);
});
