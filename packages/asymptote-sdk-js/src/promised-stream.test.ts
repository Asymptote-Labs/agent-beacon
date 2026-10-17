import { SpanStatusCode } from "@opentelemetry/api";
import { InMemorySpanExporter } from "@opentelemetry/sdk-trace-base";
import { afterEach, beforeEach, expect, it } from "vitest";

import { flush, initialize, observe, shutdown } from "./index.js";

let exporter: InMemorySpanExporter;
beforeEach(() => {
  exporter = new InMemorySpanExporter();
  initialize({ spanExporter: exporter, disableDefaultExporter: true, disableBatch: true });
});
afterEach(async () => { await shutdown(); });

it("keeps an observed promised stream open until consumption completes", async () => {
  const step = observe({ name: "agent.promised-stream" }, async () => (async function* () { yield "token"; })());
  const stream = await step();
  await flush();
  expect(exporter.getFinishedSpans()).toHaveLength(0);
  expect(await stream.next()).toEqual({ value: "token", done: false });
  expect(exporter.getFinishedSpans()).toHaveLength(0);
  expect(await stream.next()).toEqual({ value: undefined, done: true });
  await flush();
  expect(exporter.getFinishedSpans()).toHaveLength(1);
  expect(exporter.getFinishedSpans()[0]?.status.code).toBe(SpanStatusCode.OK);
});

it("records failures raised while consuming an observed promised stream", async () => {
  const failure = new Error("fixture stream failed");
  const step = observe({ name: "agent.promised-error" }, async () => (async function* () {
    yield "token";
    throw failure;
  })());
  const stream = await step();
  await stream.next();
  await expect(stream.next()).rejects.toBe(failure);
  await flush();
  expect(exporter.getFinishedSpans()).toHaveLength(1);
  expect(exporter.getFinishedSpans()[0]?.status).toEqual({ code: SpanStatusCode.ERROR, message: failure.message });
});

it("keeps the existing directly-returned stream lifecycle", async () => {
  const step = observe({ name: "agent.direct-stream" }, async function* () { yield "token"; });
  const stream = step();
  expect(exporter.getFinishedSpans()).toHaveLength(0);
  await stream.next();
  expect(exporter.getFinishedSpans()).toHaveLength(0);
  await stream.next();
  await flush();
  expect(exporter.getFinishedSpans()).toHaveLength(1);
  expect(exporter.getFinishedSpans()[0]?.status.code).toBe(SpanStatusCode.OK);
});

it("ends an ordinary promised result on resolution", async () => {
  const step = observe({ name: "agent.promise" }, async () => "result");
  expect(await step()).toBe("result");
  await flush();
  expect(exporter.getFinishedSpans()).toHaveLength(1);
  expect(exporter.getFinishedSpans()[0]?.status.code).toBe(SpanStatusCode.OK);
});

it.each([false, true])("preserves ignoreOutput for promised values (ignoreOutput=%s)", async ignoreOutput => {
  const step = observe({ name: "agent.output", ignoreOutput }, async () => "result");
  expect(await step()).toBe("result");
  await flush();
  expect(exporter.getFinishedSpans()).toHaveLength(1);
  expect(exporter.getFinishedSpans()[0]?.attributes["asymptote.observe.output.type"])
    .toBe(ignoreOutput ? undefined : "string");
});

it("records a rejected promise exactly once", async () => {
  const failure = new Error("fixture setup failed");
  const step = observe({ name: "agent.rejected" }, async () => { throw failure; });
  await expect(step()).rejects.toBe(failure);
  await flush();
  expect(exporter.getFinishedSpans()).toHaveLength(1);
  expect(exporter.getFinishedSpans()[0]?.status).toEqual({ code: SpanStatusCode.ERROR, message: failure.message });
});

it("preserves controls and their receiver on a promised query stream", async () => {
  class Stream {
    #stopped = false;
    async interrupt() { this.#stopped = true; }
    get stopped() { return this.#stopped; }
    async *[Symbol.asyncIterator]() { if (!this.#stopped) yield "token"; }
  }
  const original = new Stream();
  const step = observe({ name: "agent.control" }, async () => original);
  const stream = await step();
  expect(stream.interrupt).toBe(stream.interrupt);
  await stream.interrupt();
  expect(stream.stopped).toBe(true);
  expect(original.stopped).toBe(true);
  const tokens = [];
  for await (const token of stream) tokens.push(token);
  expect(tokens).toEqual([]);
  await flush();
  expect(exporter.getFinishedSpans()).toHaveLength(1);
});

it("cleans up a promised stream cancelled after its first item", async () => {
  let cleaned = false;
  const step = observe({ name: "agent.cancel" }, async () => (async function* () {
    try { yield "first"; yield "second"; }
    finally { cleaned = true; }
  })());
  const stream = await step();
  expect(await stream.next()).toEqual({ value: "first", done: false });
  await stream.return(undefined);
  expect(cleaned).toBe(true);
  await flush();
  expect(exporter.getFinishedSpans()).toHaveLength(1);
  expect(exporter.getFinishedSpans()[0]?.status.code).toBe(SpanStatusCode.UNSET);
});

it.each([false, true])("ends the span if inspecting a resolved stream throws (promised=%s)", async promised => {
  const failure = new Error("fixture iterator getter failed");
  const value = { get [Symbol.asyncIterator]() { throw failure; } };
  const step = observe({ name: "agent.invalid-stream" }, () => promised ? Promise.resolve(value) : value);
  let caught: unknown;
  try { await step(); }
  catch (error) { caught = error; }
  expect(caught).toBe(failure);
  await flush();
  expect(exporter.getFinishedSpans()).toHaveLength(1);
  expect(exporter.getFinishedSpans()[0]?.status).toEqual({ code: SpanStatusCode.ERROR, message: failure.message });
});
