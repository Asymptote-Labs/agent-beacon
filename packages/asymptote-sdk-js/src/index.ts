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
import {
  ATTR_SERVICE_NAME,
  ATTR_TELEMETRY_SDK_NAME,
  ATTR_TELEMETRY_SDK_VERSION,
} from "@opentelemetry/semantic-conventions";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import {
  AnthropicInstrumentation,
  type AnthropicInstrumentationConfig,
} from "@traceloop/instrumentation-anthropic";
import {
  OpenAIInstrumentation,
  type OpenAIInstrumentationConfig,
} from "@traceloop/instrumentation-openai";

import {
  ATTR_ASYMPTOTE_INPUT_COUNT,
  ATTR_ASYMPTOTE_OUTPUT_TYPE,
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
      [ATTR_TELEMETRY_SDK_NAME]: "asymptote-sdk-js",
      [ATTR_TELEMETRY_SDK_VERSION]: SDK_VERSION,
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
  state = { instrumentations, instrumentationsDisabled: !!options.disableInstrumentations, provider, mode: resolved.mode, observeUrl: resolved.observeUrl };

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
      span.setAttribute(ATTR_ASYMPTOTE_INPUT_COUNT, args.length);
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
  state?.instrumentations.forEach(instrumentation => instrumentation.disable());
  await state?.provider.shutdown();
  state = undefined;
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
  if (apiKey && !headers.authorization && !headers.Authorization) {
    headers.authorization = `Bearer ${apiKey}`;
  }
  return { observeUrl, headers, mode };
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
  let instrumentations: Instrumentation[];
  if (state?.instrumentations.length) {
    instrumentations = state.instrumentations;
  } else {
    instrumentations = initializeAsymptoteInstrumentations();
    if (state) {
      state.instrumentations = instrumentations;
    }
  }
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
          span.setAttribute(ATTR_ASYMPTOTE_OUTPUT_TYPE, typeof value);
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
    return finishAsyncIterable(result, span);
  }
  if (!options?.ignoreOutput) {
    span.setAttribute(ATTR_ASYMPTOTE_OUTPUT_TYPE, typeof result);
  }
  span.setStatus({ code: SpanStatusCode.OK });
  span.end();
  return result;
}

async function* finishAsyncIterable(iterable: AsyncIterable<unknown>, span: Span): AsyncGenerator<unknown> {
  try {
    for await (const item of iterable) {
      yield item;
    }
    span.setStatus({ code: SpanStatusCode.OK });
    span.end();
  } catch (error) {
    endSpanWithError(span, error);
    throw error;
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
