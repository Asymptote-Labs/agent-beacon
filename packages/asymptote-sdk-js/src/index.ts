import {
  SpanKind,
  SpanStatusCode,
  context,
  trace,
  type Attributes,
  type Span,
  type SpanOptions,
  type Tracer,
} from "@opentelemetry/api";
import {
  registerInstrumentations,
  type Instrumentation,
} from "@opentelemetry/instrumentation";
import { resourceFromAttributes } from "@opentelemetry/resources";
import {
  BatchSpanProcessor,
  SimpleSpanProcessor,
  type SpanExporter,
  type SpanProcessor,
} from "@opentelemetry/sdk-trace-base";
import { NodeTracerProvider } from "@opentelemetry/sdk-trace-node";
import { OTLPTraceExporter } from "@opentelemetry/exporter-trace-otlp-http";
import { ATTR_SERVICE_NAME } from "@opentelemetry/semantic-conventions";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { isDeepStrictEqual } from "node:util";
import {
  AnthropicInstrumentation,
  type AnthropicInstrumentationConfig,
} from "@traceloop/instrumentation-anthropic";
import {
  OpenAIInstrumentation,
  type OpenAIInstrumentationConfig,
} from "@traceloop/instrumentation-openai";

import {
  ATTR_ASYMPTOTE_SDK_MODE,
  ATTR_BEACON_EVENT_ACTION,
  ATTR_BEACON_EVENT_CATEGORY,
  ATTR_BEACON_HARNESS_NAME,
  ATTR_BEACON_ORIGIN,
  ATTR_BEACON_PROMPT_TEXT,
  ATTR_BEACON_SESSION_ID,
  ATTR_BEACON_SESSION_WORKING_DIRECTORY,
} from "./constants.js";

export * from "./constants.js";

const DEFAULT_HOSTED_BASE_URL = "https://api.asymptotelabs.ai";
const DEFAULT_OBSERVE_PATH = "/v1/observe";
const DEFAULT_TRACER_NAME = "asymptote-sdk";
const DEFAULT_SERVICE_NAME = "asymptote-app";
const SDK_VERSION = readSDKVersion();
const CLAUDE_AGENT_QUERY_WRAPPED = Symbol.for("@asymptote/sdk.claude-agent-query-wrapped");

export type ExportMode = "hosted" | "otlp" | "custom";

export interface InstrumentModules {
  Anthropic?: unknown;
  OpenAI?: unknown;
  anthropic?: unknown;
  claudeAgentSDK?: { query?: unknown };
  claudeAgentSdk?: { query?: unknown };
  openAI?: unknown;
  openai?: unknown;
  [name: string]: unknown;
}

export interface InitializeOptions {
  apiKey?: string;
  baseUrl?: string;
  otlpEndpoint?: string;
  headers?: Record<string, string>;
  serviceName?: string;
  resourceAttributes?: Attributes;
  instrumentModules?: InstrumentModules;
  disableInstrumentations?: boolean;
  instrumentationOptions?: AsymptoteInstrumentationOptions;
  spanProcessor?: SpanProcessor;
  spanProcessors?: SpanProcessor[];
  spanExporter?: SpanExporter;
  disableDefaultExporter?: boolean;
  disableBatch?: boolean;
  traceExportTimeoutMillis?: number;
  maxExportBatchSize?: number;
}

export interface ResolvedExporterConfig {
  observeUrl?: string;
  headers: Record<string, string>;
  mode: ExportMode;
}

export interface ObserveOptions {
  name: string;
  spanKind?: SpanKind;
  attributes?: Attributes;
  ignoreInput?: boolean;
  ignoreOutput?: boolean;
}

export interface AsymptoteInstrumentationOptions {
  anthropic?: boolean | AnthropicInstrumentationConfig;
  instrumentModules?: InstrumentModules;
  openAI?: boolean | OpenAIInstrumentationConfig;
  traceContent?: boolean;
}

interface SDKState {
  instrumentations: Instrumentation[];
  instrumentationsDisabled: boolean;
  provider: NodeTracerProvider;
  mode: ExportMode;
  observeUrl?: string;
  /** The options the active configuration was built from, to tell a repeat from a conflict. */
  options: InitializeOptions;
}

type ClaudeAgentQueryFunction<T extends (...args: any[]) => any = (...args: any[]) => any> = T & {
  [CLAUDE_AGENT_QUERY_WRAPPED]?: true;
};

let state: SDKState | undefined;

export class Observe {
  static initialize(options: InitializeOptions = {}): void {
    initialize(options);
  }

  static getTracer(name?: string, version?: string): Tracer {
    return getTracer(name, version);
  }

  static observe<T extends (...args: any[]) => any>(options: ObserveOptions, fn: T): T {
    return observe(options, fn);
  }

  static patch(modules: InstrumentModules): void {
    patch(modules);
  }

  static sessionAttributes(sessionId: string, workingDirectory?: string): Attributes {
    return sessionAttributes(sessionId, workingDirectory);
  }

  static instrumentations(options: AsymptoteInstrumentationOptions = {}): Instrumentation[] {
    return initializeAsymptoteInstrumentations(options);
  }

  static wrapClaudeAgentQuery<T extends (...args: any[]) => any>(originalQuery: T): T {
    return wrapClaudeAgentQuery(originalQuery);
  }

  static async flush(): Promise<void> {
    await flush();
  }

  static async shutdown(): Promise<void> {
    await shutdown();
  }
}

export function initialize(options: InitializeOptions = {}): void {
  if (state) {
    if (options.instrumentModules) {
      const ignored = changedOptions(state.options, options);
      if (ignored.length > 0) {
        console.warn(
          `Asymptote Observe is already initialized; ignoring changed options: ${ignored.join(", ")}. ` +
            "Call shutdown() before reinitializing with new options.",
        );
      }
      patch(options.instrumentModules);
      return;
    }
    throw new Error("Asymptote Observe is already initialized; call shutdown() before reinitializing with new options");
  }

  const resolved = resolveExporterConfig(options);
  const instrumentations = options.disableInstrumentations
    ? []
    : initializeAsymptoteInstrumentations(options.instrumentationOptions ?? {});
  const processors = [
    ...(options.spanProcessors ?? []),
    ...(options.spanProcessor ? [options.spanProcessor] : []),
  ];
  if (options.spanExporter) {
    processors.push(makeSpanProcessor(options.spanExporter, options));
  }
  if (!options.disableDefaultExporter && !options.spanExporter) {
    if (!resolved.observeUrl) {
      throw new Error("Asymptote Observe default exporter requires an Observe endpoint");
    }
    processors.push(
      makeSpanProcessor(
        new OTLPTraceExporter({
          url: resolved.observeUrl,
          headers: resolved.headers,
        }),
        options,
      ),
    );
  }

  const provider = new NodeTracerProvider({
    resource: resourceFromAttributes({
      [ATTR_SERVICE_NAME]: options.serviceName ?? process.env.OTEL_SERVICE_NAME ?? DEFAULT_SERVICE_NAME,
      "telemetry.sdk.name": "asymptote-sdk-js",
      "telemetry.sdk.version": SDK_VERSION,
      [ATTR_BEACON_ORIGIN]: "cloud",
      [ATTR_BEACON_HARNESS_NAME]: "asymptote_observe",
      [ATTR_ASYMPTOTE_SDK_MODE]: resolved.mode,
      ...(options.resourceAttributes ?? {}),
    }),
    spanProcessors: processors,
  });
  provider.register();
  if (instrumentations.length > 0) {
    registerInstrumentations({ instrumentations });
  }
  state = { instrumentations, instrumentationsDisabled: !!options.disableInstrumentations, provider, mode: resolved.mode, observeUrl: resolved.observeUrl, options };

  if (options.instrumentModules) {
    try {
      patch(options.instrumentModules);
    } catch (error) {
      instrumentations.forEach(i => i.disable());
      provider.shutdown().catch(() => {});
      state = undefined;
      throw error;
    }
  }
}

// changedOptions names the options in next, other than instrumentModules, that differ from the
// active configuration's. Repeating the same options, as the integration guides do in each module
// they set up, is not a change. Plain objects (headers, resource attributes) compare by value;
// exporters, processors and arrays of them compare by identity, since two distinct exporter
// instances can look alike field by field.
function changedOptions(active: InitializeOptions, next: InitializeOptions): string[] {
  return (Object.keys(next) as (keyof InitializeOptions)[]).filter(name => {
    if (name === "instrumentModules" || next[name] === undefined) return false;
    const a = active[name];
    const b = next[name];
    return isPlainObject(a) && isPlainObject(b) ? !isDeepStrictEqual(a, b) : a !== b;
  });
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && Object.getPrototypeOf(value) === Object.prototype;
}

export function getTracer(name = DEFAULT_TRACER_NAME, version = SDK_VERSION): Tracer {
  if (state) {
    return state.provider.getTracer(name, version);
  }
  return trace.getTracer(name, version);
}

export function observe<T extends (...args: any[]) => any>(options: ObserveOptions, fn: T): T {
  return function observed(this: unknown, ...args: Parameters<T>): ReturnType<T> {
    const tracer = getTracer();
    const span = tracer.startSpan(options.name, {
      kind: options.spanKind ?? SpanKind.INTERNAL,
      attributes: {
        [ATTR_BEACON_HARNESS_NAME]: "asymptote_observe",
        ...(options.attributes ?? {}),
      },
    });
    if (!options.ignoreInput) {
      span.setAttribute("asymptote.observe.input.count", args.length);
    }
    return context.with(trace.setSpan(context.active(), span), () => {
      try {
        const result = fn.apply(this, args);
        return finishResult(result, span, options) as ReturnType<T>;
      } catch (error) {
        endSpanWithError(span, error);
        throw error;
      }
    });
  } as T;
}

/**
 * Builds the span or resource attributes that attribute telemetry to one
 * agent session for Beacon token usage rollups. Pass the result to
 * `observe({ attributes: ... })`, `startActiveSpan`, or
 * `initialize({ resourceAttributes: ... })` so every span in the session
 * carries the same session identity.
 */
export function sessionAttributes(sessionId: string, workingDirectory?: string): Attributes {
  const attributes: Attributes = {
    [ATTR_BEACON_SESSION_ID]: sessionId,
    "gen_ai.conversation.id": sessionId,
  };
  if (workingDirectory) {
    attributes[ATTR_BEACON_SESSION_WORKING_DIRECTORY] = workingDirectory;
  }
  return attributes;
}

export function patch(modules: InstrumentModules): void {
  if (!modules || Object.keys(modules).length === 0) {
    throw new Error("Observe.patch requires at least one module");
  }
  patchOpenLLMetryModules(modules);
  patchClaudeAgentSDK(modules.claudeAgentSDK ?? modules.claudeAgentSdk);
}

export function initializeAsymptoteInstrumentations(options: AsymptoteInstrumentationOptions = {}): Instrumentation[] {
  const instrumentations: Instrumentation[] = [];
  const traceContent = options.traceContent;
  if (options.openAI !== false) {
    const config = typeof options.openAI === "object" ? options.openAI : {};
    instrumentations.push(
      new OpenAIInstrumentation({
        traceContent,
        ...config,
      }) as unknown as Instrumentation,
    );
  }
  if (options.anthropic !== false) {
    const config = typeof options.anthropic === "object" ? options.anthropic : {};
    instrumentations.push(
      new AnthropicInstrumentation({
        traceContent,
        ...config,
      }) as unknown as Instrumentation,
    );
  }
  if (options.instrumentModules) {
    manuallyInstrumentModules(instrumentations, options.instrumentModules);
  }
  return instrumentations;
}

export function wrapClaudeAgentQuery<T extends (...args: any[]) => any>(originalQuery: T): T {
  if ((originalQuery as ClaudeAgentQueryFunction<T>)[CLAUDE_AGENT_QUERY_WRAPPED]) {
    return originalQuery;
  }
  const wrapped = function asymptoteClaudeAgentQuery(this: unknown, ...args: Parameters<T>): ReturnType<T> {
    const tracer = getTracer();
    const span = tracer.startSpan("claude_agent_sdk.query", {
      kind: SpanKind.CLIENT,
      attributes: {
        [ATTR_BEACON_ORIGIN]: "cloud",
        [ATTR_BEACON_HARNESS_NAME]: "claude_agent_sdk",
        [ATTR_BEACON_EVENT_ACTION]: "prompt.submitted",
        [ATTR_BEACON_EVENT_CATEGORY]: "prompt",
        [ATTR_BEACON_PROMPT_TEXT]: firstStringArg(args),
      },
    });
    return context.with(trace.setSpan(context.active(), span), () => {
      try {
        const result = originalQuery.apply(this, args);
        return finishResult(result, span) as ReturnType<T>;
      } catch (error) {
        endSpanWithError(span, error);
        throw error;
      }
    });
  } as ClaudeAgentQueryFunction<T>;
  Object.defineProperty(wrapped, CLAUDE_AGENT_QUERY_WRAPPED, {
    value: true,
  });
  return wrapped;
}

export function startActiveSpan<T>(name: string, fn: (span: Span) => T, options: SpanOptions = {}): T {
  const tracer = getTracer();
  return tracer.startActiveSpan(name, options, span => {
    try {
      const result = fn(span);
      return finishResult(result, span) as T;
    } catch (error) {
      endSpanWithError(span, error);
      throw error;
    }
  });
}

export async function flush(): Promise<void> {
  await state?.provider.forceFlush();
}

export async function shutdown(): Promise<void> {
  const current = state;
  if (!current) return;
  current.instrumentations.forEach(instrumentation => instrumentation.disable());
  try {
    await current.provider.shutdown();
  } finally {
    // A stopped provider cannot resume exporting, even when its exporter rejects.
    if (state === current) state = undefined;
  }
}

export function isInitialized(): boolean {
  return Boolean(state);
}

export function resolveExporterConfig(options: InitializeOptions = {}): ResolvedExporterConfig {
  const explicitEndpoint = options.otlpEndpoint ?? process.env.OTEL_EXPORTER_OTLP_ENDPOINT;
  const apiKey = options.apiKey ?? process.env.ASYMPTOTE_API_KEY;
  const baseUrl = options.baseUrl ?? process.env.ASYMPTOTE_BASE_URL ?? DEFAULT_HOSTED_BASE_URL;
  const skipDefault = !!(options.disableDefaultExporter || options.spanExporter);
  const mode: ExportMode = skipDefault ? "custom" : explicitEndpoint ? "otlp" : apiKey ? "hosted" : "custom";

  if (mode === "custom" && !skipDefault && !explicitEndpoint) {
    throw new Error("Asymptote SDK requires ASYMPTOTE_API_KEY for hosted ingest or OTEL_EXPORTER_OTLP_ENDPOINT for explicit OTLP export");
  }

  const observeUrl = skipDefault ? undefined : explicitEndpoint ? observeURL(explicitEndpoint) : observeURL(baseUrl);
  const headers: Record<string, string> = { ...(options.headers ?? {}) };
  const callerSetAuthorization = Object.keys(headers).some(name => name.toLowerCase() === "authorization");
  if (apiKey && observeUrl && !callerSetAuthorization && exportsToAsymptote(mode, observeUrl, baseUrl)) {
    requireSecureKeyURL(observeUrl);
    headers.authorization = `Bearer ${apiKey}`;
  }
  return { observeUrl, headers, mode };
}

// The Asymptote API key is a credential for Asymptote's own ingest, so it is
// sent only there: in hosted mode, or when an explicit OTLP endpoint has the
// same origin as the configured Asymptote base URL. OTEL_EXPORTER_OTLP_ENDPOINT
// is an ambient, vendor-neutral variable that usually names a third-party or
// customer collector, which must never receive the key.
function exportsToAsymptote(mode: ExportMode, observeUrl: string, baseUrl: string): boolean {
  if (mode === "hosted") {
    return true;
  }
  if (mode !== "otlp") {
    return false;
  }
  const endpointOrigin = urlOrigin(observeUrl);
  return endpointOrigin !== undefined && endpointOrigin === urlOrigin(baseUrl);
}

function urlOrigin(value: string): string | undefined {
  try {
    const origin = new URL(value).origin;
    return origin === "null" ? undefined : origin;
  } catch {
    return undefined;
  }
}

const LOOPBACK_HOSTNAMES = new Set(["localhost", "127.0.0.1", "[::1]"]);

// A URL that carries the API key must be https, except loopback over http so
// local stubs and collectors keep working.
function requireSecureKeyURL(value: string): void {
  let url: URL;
  try {
    url = new URL(value);
  } catch {
    throw new Error(`Asymptote SDK refuses to send ASYMPTOTE_API_KEY to an invalid URL: ${JSON.stringify(value)}`);
  }
  if (url.protocol === "https:") {
    return;
  }
  if (url.protocol === "http:" && LOOPBACK_HOSTNAMES.has(url.hostname.toLowerCase())) {
    return;
  }
  throw new Error(
    `Asymptote SDK refuses to send ASYMPTOTE_API_KEY over ${url.protocol.replace(/:$/, "")} to ${url.host}; ` +
      "use an https:// base URL (plain http is allowed only for localhost, 127.0.0.1, and [::1])",
  );
}

function makeSpanProcessor(exporter: SpanExporter, options: InitializeOptions): SpanProcessor {
  return options.disableBatch
    ? new SimpleSpanProcessor(exporter)
    : new BatchSpanProcessor(exporter, {
        exportTimeoutMillis: options.traceExportTimeoutMillis,
        maxExportBatchSize: options.maxExportBatchSize,
      });
}

function observeURL(endpoint: string): string {
  let end = endpoint.length;
  while (end > 0 && endpoint.charCodeAt(end - 1) === 47) {
    end -= 1;
  }
  const trimmed = endpoint.slice(0, end);
  if (trimmed.endsWith(DEFAULT_OBSERVE_PATH)) {
    return trimmed;
  }
  return `${trimmed}${DEFAULT_OBSERVE_PATH}`;
}

function readSDKVersion(): string {
  try {
    const packageJSONPath = join(dirname(fileURLToPath(import.meta.url)), "..", "package.json");
    const packageJSON = JSON.parse(readFileSync(packageJSONPath, "utf8")) as { version?: unknown };
    if (typeof packageJSON.version === "string" && packageJSON.version.trim() !== "") {
      return packageJSON.version;
    }
  } catch {
    // Keep tracing usable in unusual bundler/test environments where package.json is unavailable.
  }
  return "0.1.0";
}

function patchOpenLLMetryModules(modules: InstrumentModules): void {
  if (state?.instrumentationsDisabled) {
    return;
  }
  const instrumentations = state ? state.instrumentations : initializeAsymptoteInstrumentations();
  manuallyInstrumentModules(instrumentations, modules);
}

function manuallyInstrumentModules(instrumentations: Instrumentation[], modules: InstrumentModules): void {
  const openAIModule = modules.OpenAI ?? modules.openAI ?? modules.openai;
  const anthropicModule = modules.Anthropic ?? modules.anthropic;
  for (const instrumentation of instrumentations) {
    const maybeManual = instrumentation as Instrumentation & {
      manuallyInstrument?: (moduleValue: unknown) => void;
    };
    const instrumentationName = instrumentation.instrumentationName.toLowerCase();
    if (openAIModule && instrumentationName.includes("openai")) {
      maybeManual.manuallyInstrument?.(openAIModule);
    }
    if (anthropicModule && instrumentationName.includes("anthropic")) {
      maybeManual.manuallyInstrument?.(anthropicModule);
    }
  }
}

function patchClaudeAgentSDK(moduleValue: unknown): void {
  if (!moduleValue || typeof moduleValue !== "object") {
    return;
  }
  const target = moduleValue as { query?: unknown };
  if (typeof target.query !== "function") {
    return;
  }
  try {
    target.query = wrapClaudeAgentQuery(target.query as (...args: any[]) => any);
  } catch {
    // ESM namespace exports may be read-only; callers can wrap explicitly.
  }
}

function finishResult<T>(result: T, span: Span, options?: ObserveOptions): unknown {
  if (isPromiseLike(result)) {
    return result.then(
      value => {
        if (!options?.ignoreOutput) {
          span.setAttribute("asymptote.observe.output.type", typeof value);
        }
        span.setStatus({ code: SpanStatusCode.OK });
        span.end();
        return value;
      },
      error => {
        endSpanWithError(span, error);
        throw error;
      },
    );
  }
  if (isAsyncIterable(result)) {
    // A stream such as the Claude Agent SDK's Query carries controls (interrupt(), setModel(), ...)
    // beside its iterator. Iteration goes through the traced iterator; every other read goes to
    // the original stream, with functions bound to it so private-field methods keep working, and
    // cached so repeated reads return the same function. The proxy targets the wrapper rather
    // than the original, whose read-only properties would make a binding get trap invalid.
    const iterator = finishAsyncIterable(result, span);
    const methods = {
      next: iterator.next.bind(iterator),
      return: iterator.return.bind(iterator),
      throw: iterator.throw.bind(iterator),
    };
    const controls = new Map<Function, Function>();
    const stream: AsyncGenerator<unknown> = new Proxy(iterator, {
      get(_target, key) {
        if (key === Symbol.asyncIterator) return iteratorForStream;
        if (key === "next" || key === "return" || key === "throw") return methods[key];
        const value = Reflect.get(result, key, result);
        if (typeof value !== "function" || key === "constructor") return value;
        let control = controls.get(value);
        if (!control) { control = value.bind(result); controls.set(value, control!); }
        return control;
      },
    });
    function iteratorForStream() { return stream; }
    return stream;
  }
  if (!options?.ignoreOutput) {
    span.setAttribute("asymptote.observe.output.type", typeof result);
  }
  span.setStatus({ code: SpanStatusCode.OK });
  span.end();
  return result;
}

async function* finishAsyncIterable(iterable: AsyncIterable<unknown>, span: Span): AsyncGenerator<unknown> {
  let endedWithError = false;
  try {
    for await (const item of iterable) {
      yield item;
    }
    span.setStatus({ code: SpanStatusCode.OK });
  } catch (error) {
    endedWithError = true;
    endSpanWithError(span, error);
    throw error;
  } finally {
    if (!endedWithError) span.end();
  }
}

function endSpanWithError(span: Span, error: unknown): void {
  if (error instanceof Error) {
    span.recordException(error);
    span.setStatus({ code: SpanStatusCode.ERROR, message: error.message });
  } else {
    span.setStatus({ code: SpanStatusCode.ERROR, message: String(error) });
  }
  span.end();
}

function isPromiseLike(value: unknown): value is Promise<unknown> {
  return Boolean(value && typeof (value as Promise<unknown>).then === "function");
}

function isAsyncIterable(value: unknown): value is AsyncIterable<unknown> {
  return Boolean(value && typeof (value as AsyncIterable<unknown>)[Symbol.asyncIterator] === "function");
}

function firstStringArg(args: unknown[]): string {
  for (const arg of args) {
    if (typeof arg === "string" && arg.trim() !== "") {
      return arg;
    }
    if (arg && typeof arg === "object") {
      const maybePrompt = (arg as { prompt?: unknown; input?: unknown; query?: unknown }).prompt ?? (arg as { input?: unknown }).input ?? (arg as { query?: unknown }).query;
      if (typeof maybePrompt === "string" && maybePrompt.trim() !== "") {
        return maybePrompt;
      }
    }
  }
  return "";
}
