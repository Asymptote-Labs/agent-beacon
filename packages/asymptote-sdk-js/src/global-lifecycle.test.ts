import {
  DiagLogLevel,
  ProxyTracerProvider,
  ROOT_CONTEXT,
  context,
  createContextKey,
  diag,
  propagation,
  trace,
  type Context,
  type ContextManager,
  type TextMapPropagator,
} from "@opentelemetry/api";
import { InMemorySpanExporter, SimpleSpanProcessor } from "@opentelemetry/sdk-trace-base";
import { NodeTracerProvider } from "@opentelemetry/sdk-trace-node";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

let sdk: typeof import("./index.js");
const hostProviders: NodeTracerProvider[] = [];
const diagnosticError = vi.fn();
const hostKey = createContextKey("host-context-control");

beforeEach(async () => {
  trace.disable();
  context.disable();
  propagation.disable();
  vi.resetModules();
  sdk = await import("./index.js");
  diagnosticError.mockClear();
  diag.setLogger({ error: diagnosticError, warn() {}, info() {}, debug() {}, verbose() {} }, DiagLogLevel.ERROR);
});

afterEach(async () => {
  await sdk.shutdown().catch(() => {});
  await Promise.all(hostProviders.splice(0).map(provider => provider.shutdown()));
  trace.disable();
  context.disable();
  propagation.disable();
  diag.disable();
  vi.restoreAllMocks();
});

function initialize(serviceName: string, exporter = new InMemorySpanExporter()): InMemorySpanExporter {
  sdk.initialize({ serviceName, spanExporter: exporter, disableBatch: true, disableInstrumentations: true });
  return exporter;
}

function emitGlobalAndLocal(): void {
  trace.getTracer("global-control").startSpan("global-control").end();
  sdk.getTracer().startSpan("local-control").end();
}

function expectGeneration(exporter: InMemorySpanExporter, serviceName: string): void {
  const spans = exporter.getFinishedSpans();
  expect(spans.map(span => span.name)).toEqual(["global-control", "local-control"]);
  expect(spans.every(span => span.resource.attributes["service.name"] === serviceName)).toBe(true);
}

function registerHost(): { provider: NodeTracerProvider; exporter: InMemorySpanExporter } {
  const exporter = new InMemorySpanExporter();
  const provider = new NodeTracerProvider({ spanProcessors: [new SimpleSpanProcessor(exporter)] });
  hostProviders.push(provider);
  provider.register({ contextManager: new HostContextManager(), propagator: hostPropagator });
  return { provider, exporter };
}

function expectHostGlobals(): void {
  context.with(ROOT_CONTEXT.setValue(hostKey, "host-value"), () => {
    expect(context.active().getValue(hostKey)).toBe("host-value");
    const carrier: Record<string, string> = {};
    propagation.inject(context.active(), carrier);
    expect(carrier).toEqual({ "host-propagator": "host-value" });
  });
}

class HostContextManager implements ContextManager {
  private current = ROOT_CONTEXT;

  active(): Context { return this.current; }
  with<A extends unknown[], F extends (...args: A) => ReturnType<F>>(ctx: Context, fn: F, thisArg?: ThisParameterType<F>, ...args: A): ReturnType<F> {
    const previous = this.current;
    this.current = ctx;
    try { return fn.apply(thisArg, args); } finally { this.current = previous; }
  }
  bind<T>(_: Context, target: T): T { return target; }
  enable(): this { return this; }
  disable(): this { return this; }
}

const hostPropagator: TextMapPropagator = {
  inject(ctx, carrier, setter) { setter.set(carrier, "host-propagator", String(ctx.getValue(hostKey))); },
  extract(ctx) { return ctx; },
  fields() { return ["host-propagator"]; },
};

it("routes fresh global and local tracers to each owned generation", async () => {
  for (const serviceName of ["first-generation", "second-generation"]) {
    const exporter = initialize(serviceName);
    emitGlobalAndLocal();
    await sdk.flush();
    expectGeneration(exporter, serviceName);
    await sdk.shutdown();
  }
});

it("reuses process defaults across restarts without duplicate registrations", async () => {
  initialize("first-generation");
  const parent = sdk.getTracer().startSpan("context-survives-shutdown");
  await context.with(trace.setSpan(context.active(), parent), async () => {
    await sdk.shutdown();
    await Promise.resolve();
    expect(trace.getActiveSpan()).toBe(parent);
    initialize("second-generation");
    await Promise.resolve();
    expect(trace.getActiveSpan()).toBe(parent);
    const carrier: Record<string, string> = {};
    propagation.inject(context.active(), carrier);
    expect(carrier.traceparent).toContain(parent.spanContext().traceId);
  });
  parent.end();
  expect(diagnosticError).not.toHaveBeenCalled();
});

it("preserves a pre-existing host provider, context manager and propagator", async () => {
  const host = registerHost();
  const registerContext = vi.spyOn(context, "setGlobalContextManager");
  const registerPropagation = vi.spyOn(propagation, "setGlobalPropagator");
  const exporter = initialize("sdk-local-generation");
  emitGlobalAndLocal();
  await sdk.flush();
  expect(exporter.getFinishedSpans().map(span => span.name)).toEqual(["local-control"]);
  expect(host.exporter.getFinishedSpans().map(span => span.name)).toEqual(["global-control"]);
  await sdk.shutdown();
  initialize("next-sdk-local-generation");
  await sdk.shutdown();
  expectHostGlobals();
  trace.getTracer("host-after-sdk").startSpan("host-after-sdk").end();
  await host.provider.forceFlush();
  expect(host.exporter.getFinishedSpans().map(span => span.name)).toEqual(["global-control", "host-after-sdk"]);
  expect(registerContext).not.toHaveBeenCalled();
  expect(registerPropagation).not.toHaveBeenCalled();
  expect(diagnosticError).not.toHaveBeenCalled();
});

it("distinguishes the public virgin NOOP delegate from a registered host provider", async () => {
  const virgin = trace.getTracerProvider();
  expect(virgin).toBeInstanceOf(ProxyTracerProvider);
  expect((virgin as ProxyTracerProvider).getDelegate()).toBe(new ProxyTracerProvider().getDelegate());
  const exporter = initialize("first-owned-generation");
  expect((trace.getTracerProvider() as ProxyTracerProvider).getDelegate()).not.toBe(new ProxyTracerProvider().getDelegate());
  emitGlobalAndLocal();
  await sdk.flush();
  expectGeneration(exporter, "first-owned-generation");
  expect(diagnosticError).not.toHaveBeenCalled();
});

it("preserves a host replacement installed while the SDK is active", async () => {
  initialize("sdk-generation");
  trace.disable();
  context.disable();
  propagation.disable();
  const host = registerHost();
  await sdk.shutdown();
  initialize("next-sdk-generation");
  await sdk.shutdown();
  expectHostGlobals();
  trace.getTracer("host-replacement").startSpan("host-replacement").end();
  await host.provider.forceFlush();
  expect(host.exporter.getFinishedSpans().map(span => span.name)).toEqual(["host-replacement"]);
});

it.each(["context", "propagation", "both"])("preserves pre-existing %s registrations through owned tracer restarts", async resource => {
  if (resource !== "propagation") context.setGlobalContextManager(new HostContextManager());
  if (resource !== "context") propagation.setGlobalPropagator(hostPropagator);
  initialize("first-generation");
  const rejected = resource === "both" ? ["context", "propagation"] : [resource];
  expect(diagnosticError).toHaveBeenCalledTimes(rejected.length);
  rejected.forEach((name, index) => {
    expect(String(diagnosticError.mock.calls[index][0])).toContain(`registration of API: ${name}`);
  });
  await sdk.shutdown();
  const replacement = initialize("second-generation");
  emitGlobalAndLocal();
  await sdk.flush();
  expectGeneration(replacement, "second-generation");
  context.with(ROOT_CONTEXT.setValue(hostKey, "host-value"), () => {
    expect(context.active().getValue(hostKey)).toBe("host-value");
    if (resource !== "context") {
      const carrier: Record<string, string> = {};
      propagation.inject(context.active(), carrier);
      expect(carrier).toEqual({ "host-propagator": "host-value" });
    }
  });
  expect(diagnosticError).toHaveBeenCalledTimes(rejected.length);
});

it("recovers global routing after a module patch fails during initialization", async () => {
  expect(() => sdk.initialize({
    spanExporter: new InMemorySpanExporter(), disableBatch: true, disableInstrumentations: true, instrumentModules: {},
  })).toThrow("Observe.patch requires at least one module");
  expect(sdk.isInitialized()).toBe(false);
  const replacement = initialize("recovered-generation");
  emitGlobalAndLocal();
  await sdk.flush();
  expectGeneration(replacement, "recovered-generation");
});

it("preserves a host replacement when initialization fails partway through patching", async () => {
  let host: ReturnType<typeof registerHost> | undefined;
  const modules = {
    get claudeAgentSDK(): never {
      trace.disable();
      context.disable();
      propagation.disable();
      host = registerHost();
      throw new Error("synthetic module patch failure");
    },
  };
  expect(() => sdk.initialize({
    spanExporter: new InMemorySpanExporter(), disableBatch: true, disableInstrumentations: true, instrumentModules: modules,
  })).toThrow("synthetic module patch failure");
  expect(sdk.isInitialized()).toBe(false);
  expectHostGlobals();
  trace.getTracer("host-after-failure").startSpan("host-after-failure").end();
  await host!.provider.forceFlush();
  expect(host!.exporter.getFinishedSpans().map(span => span.name)).toEqual(["host-after-failure"]);
});

it("cleans up a partial bootstrap failure without retrying process defaults", async () => {
  const registerPropagation = vi.spyOn(propagation, "setGlobalPropagator").mockImplementationOnce(() => {
    throw new Error("synthetic propagation registration failure");
  });
  expect(() => initialize("failed-generation")).toThrow("synthetic propagation registration failure");
  expect(sdk.isInitialized()).toBe(false);
  const replacement = initialize("recovered-generation");
  emitGlobalAndLocal();
  await sdk.flush();
  expectGeneration(replacement, "recovered-generation");
  expect(registerPropagation).toHaveBeenCalledTimes(1);
  const parent = sdk.getTracer().startSpan("preserved-bootstrap-context");
  await context.with(trace.setSpan(context.active(), parent), async () => {
    await Promise.resolve();
    expect(trace.getActiveSpan()).toBe(parent);
  });
  parent.end();
  expect(diagnosticError).not.toHaveBeenCalled();
});

it("recovers global routing after an exporter rejects shutdown", async () => {
  class RejectingExporter extends InMemorySpanExporter {
    override async shutdown(): Promise<void> { throw new Error("synthetic shutdown failure"); }
  }
  initialize("failing-generation", new RejectingExporter());
  await expect(sdk.shutdown()).rejects.toThrow("synthetic shutdown failure");
  expect(sdk.isInitialized()).toBe(false);
  const replacement = initialize("recovered-generation");
  emitGlobalAndLocal();
  await sdk.flush();
  expectGeneration(replacement, "recovered-generation");
});

it("does not let concurrent old shutdown completions detach a new generation", async () => {
  let release!: () => void;
  let started!: () => void;
  const shutdownStarted = new Promise<void>(resolve => { started = resolve; });
  class DeferredExporter extends InMemorySpanExporter {
    override async shutdown(): Promise<void> {
      started();
      await new Promise<void>(resolve => { release = resolve; });
      await super.shutdown();
    }
  }
  initialize("first-generation", new DeferredExporter());
  const firstShutdown = sdk.shutdown();
  const concurrentShutdown = sdk.shutdown();
  await shutdownStarted;
  expect(() => initialize("too-early-generation")).toThrow("already initialized");
  release();
  await firstShutdown;
  const replacement = initialize("second-generation");
  await concurrentShutdown;
  emitGlobalAndLocal();
  await sdk.flush();
  expectGeneration(replacement, "second-generation");
});

it("preserves a host replacement installed while exporter shutdown is pending", async () => {
  let release!: () => void;
  let started!: () => void;
  const shutdownStarted = new Promise<void>(resolve => { started = resolve; });
  class DeferredExporter extends InMemorySpanExporter {
    override async shutdown(): Promise<void> {
      started();
      await new Promise<void>(resolve => { release = resolve; });
      await super.shutdown();
    }
  }
  initialize("sdk-generation", new DeferredExporter());
  const pendingShutdown = sdk.shutdown();
  await shutdownStarted;
  trace.disable();
  context.disable();
  propagation.disable();
  const host = registerHost();
  release();
  await pendingShutdown;
  expectHostGlobals();
  trace.getTracer("host-during-shutdown").startSpan("host-during-shutdown").end();
  await host.provider.forceFlush();
  expect(host.exporter.getFinishedSpans().map(span => span.name)).toEqual(["host-during-shutdown"]);
  expect(diagnosticError).not.toHaveBeenCalled();
});

it("requires new tracers after shutdown instead of promising cached tracer rebinding", async () => {
  initialize("first-generation");
  const cachedGlobalTracer = trace.getTracer("cached-global");
  const cachedLocalTracer = sdk.getTracer("cached-local");
  await sdk.shutdown();
  const replacement = initialize("second-generation");
  cachedGlobalTracer.startSpan("cached-old-global").end();
  cachedLocalTracer.startSpan("cached-old-local").end();
  emitGlobalAndLocal();
  await sdk.flush();
  expectGeneration(replacement, "second-generation");
});
