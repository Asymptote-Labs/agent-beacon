import { context, trace } from "@opentelemetry/api";
import { InMemorySpanExporter } from "@opentelemetry/sdk-trace-base";
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

it.each(["complete", "return", "throw", "source-error"])("keeps deferred stream work under its span (%s)", async mode => {
  const failure = new Error("fixture stream failure");
  const step = observe({ name: "stream.parent" }, async function* () {
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
  });
  const consumer = observe({ name: "consumer" }, async () => {
    const consumerId = activeId();
    const stream = step();
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
  expect(spans.find(span => span.name === "outside")?.parentSpanContext).toBeUndefined();
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
