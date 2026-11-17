import { context, trace, SpanStatusCode } from "@opentelemetry/api";
import { InMemorySpanExporter } from "@opentelemetry/sdk-trace-base";
import { afterEach, beforeEach, expect, it } from "vitest";

import { flush, initialize, observe, shutdown } from "./index.js";

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

it("keeps custom disposal methods bound to their original resource", async () => {
  class Resource {
    #disposed = false;
    [Symbol.asyncIterator]() { return this; }
    async next(): Promise<IteratorResult<string>> { return { done: this.#disposed, value: "item" }; }
    async [Symbol.asyncDispose]() { this.#disposed = true; }
    disposed() { return this.#disposed; }
  }
  const source = new Resource();
  const stream = observe({ name: "custom disposal" }, () => source)();
  await stream[Symbol.asyncDispose]();
  expect(source.disposed()).toBe(true);
  await stream.next();
  await flush();
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

it.skipIf(!nativeDisposal)("does not reuse an original-receiver binding cached for a disposal alias", async () => {
  const original = (async function* () { yield "item"; })();
  const source = Object.assign(original, { closeAlias: original[Symbol.asyncDispose] });
  const stream = observe({ name: "alias" }, () => source)();
  const alias = stream.closeAlias;
  expect(stream.closeAlias).toBe(alias);
  await stream.next();
  await stream[Symbol.asyncDispose]();
  await flush();
  expect(exporter.getFinishedSpans()).toHaveLength(1);
});
