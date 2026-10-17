import { SpanStatusCode } from "@opentelemetry/api";
import { InMemorySpanExporter } from "@opentelemetry/sdk-trace-base";
import { afterEach, expect, it } from "vitest";

import { flush, initialize, shutdown, wrapClaudeAgentQuery } from "./index.js";

afterEach(async () => { await shutdown(); });

function queryResource(close: () => Promise<IteratorResult<unknown> | void> = async () => {}) {
  let nextCalls = 0;
  let returnCalls = 0;
  const original = {
    [Symbol.asyncIterator]() { return this; },
    async next(): Promise<IteratorResult<string>> {
      nextCalls++;
      return { value: "message", done: false };
    },
    async return(value?: unknown): Promise<IteratorResult<unknown>> {
      returnCalls++;
      return await close() ?? { value, done: true };
    },
    async throw(error: unknown): Promise<IteratorResult<unknown>> { throw error; },
  };
  const exporter = new InMemorySpanExporter();
  initialize({ spanExporter: exporter, disableDefaultExporter: true, disableBatch: true });
  const stream = wrapClaudeAgentQuery(() => original)();
  return { stream, exporter, counts: () => ({ nextCalls, returnCalls }) };
}

it("closes an unstarted query resource and exports its span exactly once", async () => {
  const { stream, exporter, counts } = queryResource();
  expect(exporter.getFinishedSpans()).toHaveLength(0);
  expect(await stream.return(Promise.resolve("cancelled"))).toEqual({ value: "cancelled", done: true });
  expect(await stream.return("again")).toEqual({ value: "again", done: true });
  expect(await stream.next()).toEqual({ value: undefined, done: true });
  await flush();
  expect(counts()).toEqual({ nextCalls: 0, returnCalls: 1 });
  expect(exporter.getFinishedSpans()).toHaveLength(1);
  expect(exporter.getFinishedSpans()[0]?.status.code).toBe(SpanStatusCode.UNSET);
});

it("awaits unstarted resource cleanup before return and queued next complete", async () => {
  let release!: () => void;
  const cleanup = new Promise<void>(resolve => { release = resolve; });
  const { stream, exporter, counts } = queryResource(() => cleanup);
  let returned = false;
  const returning = stream.return().then(value => { returned = true; return value; });
  const returningAgain = stream.return("again");
  const next = stream.next();
  await Promise.resolve();
  expect(returned).toBe(false);
  expect(exporter.getFinishedSpans()).toHaveLength(0);
  release();
  expect(await returning).toEqual({ value: undefined, done: true });
  expect(await returningAgain).toEqual({ value: "again", done: true });
  expect(await next).toEqual({ value: undefined, done: true });
  expect(counts()).toEqual({ nextCalls: 0, returnCalls: 1 });
  expect(exporter.getFinishedSpans()).toHaveLength(1);
});

it("cleans up and records a consumer throw before the first next", async () => {
  const { stream, exporter, counts } = queryResource();
  const failure = new Error("consumer cancelled before reading");
  await expect(stream.throw(failure)).rejects.toBe(failure);
  expect(await stream.return()).toEqual({ value: undefined, done: true });
  expect(counts()).toEqual({ nextCalls: 0, returnCalls: 1 });
  expect(exporter.getFinishedSpans()).toHaveLength(1);
  expect(exporter.getFinishedSpans()[0]?.status.code).toBe(SpanStatusCode.ERROR);
});

it("propagates cleanup failure and still ends the unstarted span once", async () => {
  const failure = new Error("query close failed");
  const { stream, exporter, counts } = queryResource(async () => { throw failure; });
  await expect(stream.return()).rejects.toBe(failure);
  await stream.return();
  expect(counts()).toEqual({ nextCalls: 0, returnCalls: 1 });
  expect(exporter.getFinishedSpans()).toHaveLength(1);
  expect(exporter.getFinishedSpans()[0]?.status.code).toBe(SpanStatusCode.ERROR);
});

it.each([false, true])("preserves the consumer throw when cleanup rejects (started=%s)", async started => {
  const closeFailure = new Error("query close failed");
  const consumerFailure = new Error("consumer failed");
  const { stream, exporter, counts } = queryResource(async () => { throw closeFailure; });
  if (started) await stream.next();
  await expect(stream.throw(consumerFailure)).rejects.toBe(consumerFailure);
  expect(counts()).toEqual({ nextCalls: started ? 1 : 0, returnCalls: 1 });
  expect(exporter.getFinishedSpans()).toHaveLength(1);
  expect(exporter.getFinishedSpans()[0]?.status).toEqual({ code: SpanStatusCode.ERROR, message: consumerFailure.message });
});

it.each([false, true])("preserves a rejected return value when cleanup rejects (started=%s)", async started => {
  const closeFailure = new Error("query close failed");
  const valueFailure = new Error("return value failed");
  const { stream, exporter, counts } = queryResource(async () => { throw closeFailure; });
  if (started) await stream.next();
  await expect(stream.return(Promise.reject(valueFailure))).rejects.toBe(valueFailure);
  expect(counts()).toEqual({ nextCalls: started ? 1 : 0, returnCalls: 1 });
  expect(exporter.getFinishedSpans()).toHaveLength(1);
  expect(exporter.getFinishedSpans()[0]?.status).toEqual({ code: SpanStatusCode.ERROR, message: valueFailure.message });
});

it.each([false, true])("uses existing close semantics when source return yields (started=%s)", async started => {
  const { stream, exporter, counts } = queryResource(async () => ({ value: "cleanup yield", done: false }));
  if (started) await stream.next();
  expect(await stream.return("cancelled")).toEqual({ value: "cancelled", done: true });
  expect(await stream.next()).toEqual({ value: undefined, done: true });
  expect(counts()).toEqual({ nextCalls: started ? 1 : 0, returnCalls: 1 });
  expect(exporter.getFinishedSpans()).toHaveLength(1);
});

it("does not start a bare iterable factory solely to cancel", async () => {
  let created = 0;
  const original = {
    [Symbol.asyncIterator]() {
      created++;
      return (async function* () { yield "message"; })();
    },
  };
  const exporter = new InMemorySpanExporter();
  initialize({ spanExporter: exporter, disableDefaultExporter: true, disableBatch: true });
  const stream = wrapClaudeAgentQuery(() => original)() as unknown as AsyncGenerator<string>;
  await stream.return(undefined);
  expect(created).toBe(0);
  expect(exporter.getFinishedSpans()).toHaveLength(1);
});

it("queues synchronous cleanup reentry behind the pending close", async () => {
  let release!: () => void;
  const cleanup = new Promise<void>(resolve => { release = resolve; });
  let entered!: () => void;
  const closeStarted = new Promise<void>(resolve => { entered = resolve; });
  let next: Promise<IteratorResult<unknown>> | undefined;
  let nextFinished = false;
  const { stream, exporter, counts } = queryResource(async () => {
    next = stream.next().then(value => { nextFinished = true; return value; });
    entered();
    await cleanup;
  });
  const returning = stream.return();
  await closeStarted;
  // Let the reentrant next settle if it incorrectly bypasses the cleanup gate.
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
  expect(next).toBeDefined();
  expect(nextFinished).toBe(false);
  expect(exporter.getFinishedSpans()).toHaveLength(0);
  release();
  await returning;
  expect(await next).toEqual({ value: undefined, done: true });
  expect(counts()).toEqual({ nextCalls: 0, returnCalls: 1 });
  expect(exporter.getFinishedSpans()).toHaveLength(1);
});

it("queues return-value thenable getter reentry behind pending cleanup", async () => {
  let release!: () => void;
  const cleanup = new Promise<void>(resolve => { release = resolve; });
  let entered!: () => void;
  const closeStarted = new Promise<void>(resolve => { entered = resolve; });
  let next: Promise<IteratorResult<unknown>> | undefined;
  let nextFinished = false;
  const { stream, exporter, counts } = queryResource(async () => {
    entered();
    await cleanup;
  });
  const value = {
    get then() {
      next = stream.next().then(value => { nextFinished = true; return value; });
      return (resolve: (value: string) => void) => { resolve("cancelled"); };
    },
  };
  const returning = stream.return(value);
  await closeStarted;
  try {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    expect(next).toBeDefined();
    expect(nextFinished).toBe(false);
    expect(exporter.getFinishedSpans()).toHaveLength(0);
  } finally {
    release();
  }
  expect(await returning).toEqual({ value: "cancelled", done: true });
  expect(await next).toEqual({ value: undefined, done: true });
  expect(counts()).toEqual({ nextCalls: 0, returnCalls: 1 });
  expect(exporter.getFinishedSpans()).toHaveLength(1);
});
