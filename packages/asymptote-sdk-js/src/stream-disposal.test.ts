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

const nativeDisposal = typeof (async function* () {})()[Symbol.asyncDispose] === "function";

it.skipIf(!nativeDisposal).each([false, true])("ends a natively disposed generator span (started=%s)", async started => {
  let cleanupParent: string | undefined;
  const source = async function* () {
    try {
      yield "item";
    } finally {
      await Promise.resolve();
      cleanupParent = trace.getSpan(context.active())?.spanContext().spanId;
    }
  };
  const stream = observe({ name: "disposed" }, source)();
  if (started) await stream.next();
  expect(exporter.getFinishedSpans()).toHaveLength(0);
  const dispose = stream[Symbol.asyncDispose];
  expect(stream[Symbol.asyncDispose]).toBe(dispose);
  await dispose();
  await flush();
  expect(exporter.getFinishedSpans()).toHaveLength(1);
  const span = exporter.getFinishedSpans()[0]!;
  expect(span.status.code).toBe(SpanStatusCode.UNSET);
  if (started) expect(cleanupParent).toBe(span.spanContext().spanId);
  await stream[Symbol.asyncDispose]();
  expect(await stream.next()).toEqual({ value: undefined, done: true });
  await flush();
  expect(exporter.getFinishedSpans()).toHaveLength(1);
});

it.skipIf(!nativeDisposal)("records errors from native async disposal", async () => {
  const failure = new Error("cleanup failed");
  const stream = observe({ name: "failing disposal" }, async function* () {
    try { yield "item"; } finally { throw failure; }
  })();
  await stream.next();
  await expect(stream[Symbol.asyncDispose]()).rejects.toBe(failure);
  await flush();
  expect(exporter.getFinishedSpans()).toHaveLength(1);
  expect(exporter.getFinishedSpans()[0]?.status).toEqual({ code: SpanStatusCode.ERROR, message: failure.message });
});

it.each([false, true])("ends the span at await-using scope exit through a custom disposer (started=%s)", async started => {
  const receivers: unknown[] = [];
  class Resource {
    #disposed = false;
    [Symbol.asyncIterator]() { return this; }
    async next(): Promise<IteratorResult<string>> { return { done: this.#disposed, value: "item" }; }
    async [Symbol.asyncDispose]() { receivers.push(this); this.#disposed = true; }
    disposed() { return this.#disposed; }
  }
  const source = new Resource();
  {
    await using stream = observe({ name: "custom disposal" }, () => source)();
    if (started) await stream.next();
    expect(exporter.getFinishedSpans()).toHaveLength(0);
  }
  await flush();
  expect(receivers).toHaveLength(1);
  expect(receivers[0]).toBe(source);
  expect(source.disposed()).toBe(true);
  expect(exporter.getFinishedSpans()).toHaveLength(1);
  expect(exporter.getFinishedSpans()[0]?.status.code).toBe(SpanStatusCode.UNSET);
});

it.each([false, true])("records errors from a custom disposer and still ends the span (started=%s)", async started => {
  const failure = new Error("cleanup failed");
  const source = {
    [Symbol.asyncIterator]() { return this; },
    async next(): Promise<IteratorResult<string>> { return { done: false, value: "item" }; },
    async [Symbol.asyncDispose]() { throw failure; },
  };
  const stream = observe({ name: "failing custom disposal" }, () => source)();
  if (started) await stream.next();
  await expect(stream[Symbol.asyncDispose]()).rejects.toBe(failure);
  await flush();
  expect(exporter.getFinishedSpans()).toHaveLength(1);
  expect(exporter.getFinishedSpans()[0]?.status).toEqual({ code: SpanStatusCode.ERROR, message: failure.message });
});

it.each([false, true])("closes a Claude Agent SDK query at await-using scope exit (started=%s)", async started => {
  const receivers: unknown[] = [];
  class Query {
    #closed = false;
    [Symbol.asyncIterator]() { return this; }
    async next(): Promise<IteratorResult<string>> {
      return this.#closed ? { value: undefined, done: true } : { value: "message", done: false };
    }
    async return(value?: unknown): Promise<IteratorResult<unknown>> { this.#closed = true; return { value, done: true }; }
    async throw(error: unknown): Promise<IteratorResult<unknown>> { throw error; }
    async interrupt() {}
    async cleanup() { receivers.push(this); this.#closed = true; }
    async [Symbol.asyncDispose]() { await this.cleanup(); }
    closed() { return this.#closed; }
  }
  const source = new Query();
  {
    await using stream = wrapClaudeAgentQuery(() => source)();
    if (started) await stream.next();
    await stream.interrupt();
    expect(exporter.getFinishedSpans()).toHaveLength(0);
  }
  await flush();
  expect(receivers).toHaveLength(1);
  expect(receivers[0]).toBe(source);
  expect(source.closed()).toBe(true);
  expect(exporter.getFinishedSpans()).toHaveLength(1);
});

it.skipIf(!nativeDisposal)("closes a promised observed generator at await-using scope exit", async () => {
  {
    await using stream = await observe({ name: "scoped" }, async () => (async function* () {
      yield "item";
    })())();
    await stream.next();
    expect(exporter.getFinishedSpans()).toHaveLength(0);
  }
  await flush();
  expect(exporter.getFinishedSpans()).toHaveLength(1);
  expect(exporter.getFinishedSpans()[0]?.status.code).toBe(SpanStatusCode.UNSET);
});
