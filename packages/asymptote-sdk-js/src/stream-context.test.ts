import { context, trace, SpanStatusCode } from "@opentelemetry/api";
import { InMemorySpanExporter } from "@opentelemetry/sdk-trace-base";
import { Readable, Writable } from "node:stream";
import { pipeline } from "node:stream/promises";
import { afterEach, beforeEach, expect, it } from "vitest";

import { flush, initialize, observe, shutdown } from "./index.js";

let exporter: InMemorySpanExporter;
beforeEach(() => {
  exporter = new InMemorySpanExporter();
  initialize({ spanExporter: exporter, disableDefaultExporter: true, disableBatch: true, disableInstrumentations: true });
});
afterEach(async () => { await shutdown(); });

const activeId = () => trace.getSpan(context.active())?.spanContext().spanId;
const child = (name: string) => observe({ name }, () => "fixture")();

it.each(
  ["direct", "promised"].flatMap(delivery =>
    ["complete", "return", "throw", "source-error"].map(mode => ({ delivery, mode }))),
)("keeps deferred stream work under its span ($delivery / $mode)", async ({ delivery, mode }) => {
  const failure = new Error("fixture stream failure");
  const source = async function* () {
    try {
      await Promise.resolve();
      child("stream.body");
      yield "first";
      await Promise.resolve();
      if (mode === "source-error") throw failure;
      child("stream.resumed");
      yield "second";
    } finally {
      await Promise.resolve();
      child("stream.cleanup");
    }
  };
  const step = observe({ name: "stream.parent" }, () =>
    delivery === "promised" ? Promise.resolve(source()) : source());
  const consumer = observe({ name: "consumer" }, async () => {
    const consumerId = activeId();
    const stream = await step();
    expect(exporter.getFinishedSpans().filter(span => span.name === "stream.parent")).toHaveLength(0);
    expect(activeId()).toBe(consumerId);
    expect(await stream.next()).toEqual({ value: "first", done: false });
    expect(activeId()).toBe(consumerId);
    if (mode === "return") await stream.return(undefined);
    else if (mode === "throw") await expect(stream.throw(failure)).rejects.toBe(failure);
    else if (mode === "source-error") await expect(stream.next()).rejects.toBe(failure);
    else {
      expect(await stream.next()).toEqual({ value: "second", done: false });
      expect(activeId()).toBe(consumerId);
      expect(await stream.next()).toEqual({ value: undefined, done: true });
    }
    expect(activeId()).toBe(consumerId);
  });
  await consumer();
  expect(activeId()).toBeUndefined();
  child("outside");
  await flush();
  const spans = exporter.getFinishedSpans();
  const parent = spans.find(span => span.name === "stream.parent")!;
  const owner = spans.find(span => span.name === "consumer")!;
  expect(parent.parentSpanContext?.spanId).toBe(owner.spanContext().spanId);
  for (const name of ["stream.body", "stream.cleanup", ...(mode === "complete" ? ["stream.resumed"] : [])]) {
    const span = spans.find(span => span.name === name)!;
    expect(span.parentSpanContext?.spanId, name).toBe(parent.spanContext().spanId);
    expect(span.spanContext().traceId, name).toBe(parent.spanContext().traceId);
  }
  expect(spans.filter(span => span.name === "stream.parent")).toHaveLength(1);
  expect(parent.status.code).toBe(mode === "complete" ? SpanStatusCode.OK : mode === "return" ? SpanStatusCode.UNSET : SpanStatusCode.ERROR);
  expect(spans.find(span => span.name === "outside")?.parentSpanContext).toBeUndefined();
});

it("does not wrap promised platform streams with their own reading API", async () => {
  const original = new ReadableStream<string>({
    start(controller) {
      controller.enqueue("fixture");
      controller.close();
    },
  });
  const stream = await observe({ name: "platform-stream" }, async () => original)();
  expect(stream).toBe(original);
  expect(stream).toBeInstanceOf(ReadableStream);
  expect(exporter.getFinishedSpans().filter(span => span.name === "platform-stream")).toHaveLength(1);
  const reader = stream.getReader();
  try {
    expect(await reader.read()).toEqual({ value: "fixture", done: false });
    expect(await reader.read()).toEqual({ value: undefined, done: true });
  } finally {
    reader.releaseLock();
  }
});

it("keeps a promised Node stream's identity and non-iterator consumption", async () => {
  const original = Readable.from(["fixture"]);
  const stream = await observe({ name: "node-stream" }, async () => original)();
  expect(stream).toBe(original);
  expect(stream).toBeInstanceOf(Readable);
  expect(exporter.getFinishedSpans().filter(span => span.name === "node-stream")).toHaveLength(1);
  let text = "";
  await pipeline(stream, new Writable({
    write(chunk, _encoding, callback) {
      text += chunk.toString();
      callback();
    },
  }));
  expect(text).toBe("fixture");
});

it.each(["return", "throw"])("closes an unstarted promised iterator through %s", async operation => {
  let closed = 0;
  let nextCalls = 0;
  const failure = new Error("cancelled before consumption");
  const original = {
    [Symbol.asyncIterator]() { return this; },
    async next(): Promise<IteratorResult<string>> {
      nextCalls++;
      return { value: "fixture", done: false };
    },
    async return(): Promise<IteratorResult<string>> {
      closed++;
      return { value: undefined, done: true };
    },
    async throw(error: unknown): Promise<IteratorResult<string>> { throw error; },
  };
  const stream = await observe({ name: "unstarted" }, async () => original)();
  expect(exporter.getFinishedSpans()).toHaveLength(0);
  if (operation === "throw") await expect(stream.throw(failure)).rejects.toBe(failure);
  else expect(await stream.return()).toEqual({ value: undefined, done: true });
  expect(nextCalls).toBe(0);
  expect(closed).toBe(1);
  expect(exporter.getFinishedSpans()).toHaveLength(1);
  expect(exporter.getFinishedSpans()[0].status.code).toBe(operation === "throw" ? SpanStatusCode.ERROR : SpanStatusCode.UNSET);
});

it("keeps the existing asynchronous scalar context", async () => {
  const step = observe({ name: "scalar.parent" }, async () => {
    await Promise.resolve();
    child("scalar.child");
    return "result";
  });
  expect(await step()).toBe("result");
  expect(activeId()).toBeUndefined();
  await flush();
  const spans = exporter.getFinishedSpans();
  const parent = spans.find(span => span.name === "scalar.parent")!;
  expect(spans.find(span => span.name === "scalar.child")?.parentSpanContext?.spanId).toBe(parent.spanContext().spanId);
});
