import { SpanStatusCode } from "@opentelemetry/api";
import { InMemorySpanExporter } from "@opentelemetry/sdk-trace-base";
import { readFileSync } from "node:fs";
import { createServer, type IncomingMessage } from "node:http";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import {
  ATTR_BEACON_EVENT_ACTION,
  ATTR_BEACON_EVENT_CATEGORY,
  ATTR_BEACON_HARNESS_NAME,
  ATTR_BEACON_ORIGIN,
  ATTR_BEACON_PROMPT_TEXT,
  Observe,
  flush,
  getTracer,
  initialize,
  initializeAsymptoteInstrumentations,
  isInitialized,
  observe,
  resolveExporterConfig,
  sessionAttributes,
  shutdown,
  wrapClaudeAgentQuery,
} from "./index.js";

const envKeys = [
  "ASYMPTOTE_API_KEY",
  "ASYMPTOTE_BASE_URL",
  "OTEL_EXPORTER_OTLP_ENDPOINT",
];

describe("resolveExporterConfig", () => {
  beforeEach(() => {
    clearEnv();
  });

  afterEach(() => {
    clearEnv();
  });

  it("resolves hosted config from api key and default base URL", () => {
    const config = resolveExporterConfig({ apiKey: "test-key" });

    expect(config.mode).toBe("hosted");
    expect(config.observeUrl).toBe("https://api.asymptotelabs.ai/v1/observe");
    expect(config.headers.authorization).toBe("Bearer test-key");
  });

  it("resolves hosted config from env", () => {
    process.env.ASYMPTOTE_API_KEY = "env-key";
    process.env.ASYMPTOTE_BASE_URL = "https://observe.example";

    const config = resolveExporterConfig();

    expect(config.mode).toBe("hosted");
    expect(config.observeUrl).toBe("https://observe.example/v1/observe");
    expect(config.headers.authorization).toBe("Bearer env-key");
  });

  it("prefers explicit api key and base URL over env", () => {
    process.env.ASYMPTOTE_API_KEY = "env-key";
    process.env.ASYMPTOTE_BASE_URL = "https://observe.example";

    const config = resolveExporterConfig({ apiKey: "option-key", baseUrl: "https://option.example" });

    expect(config.mode).toBe("hosted");
    expect(config.observeUrl).toBe("https://option.example/v1/observe");
    expect(config.headers.authorization).toBe("Bearer option-key");
  });

  it("resolves explicit Observe endpoint config without api key", () => {
    const config = resolveExporterConfig({ otlpEndpoint: "http://127.0.0.1:4318" });

    expect(config.mode).toBe("otlp");
    expect(config.observeUrl).toBe("http://127.0.0.1:4318/v1/observe");
    expect(config.headers.authorization).toBeUndefined();
  });

  it("keeps explicit Observe endpoints unchanged after trimming trailing slashes", () => {
    process.env.OTEL_EXPORTER_OTLP_ENDPOINT = "http://127.0.0.1:4318/v1/observe/";

    const config = resolveExporterConfig();

    expect(config.mode).toBe("otlp");
    expect(config.observeUrl).toBe("http://127.0.0.1:4318/v1/observe");
  });

  it("prefers explicit OTLP endpoint option over generic OTLP endpoint env", () => {
    process.env.OTEL_EXPORTER_OTLP_ENDPOINT = "http://127.0.0.1:4318";

    const config = resolveExporterConfig({ otlpEndpoint: "http://127.0.0.1:9999/v1/observe" });

    expect(config.mode).toBe("otlp");
    expect(config.observeUrl).toBe("http://127.0.0.1:9999/v1/observe");
  });

  it("does not send the api key to a foreign OTLP endpoint from env", () => {
    process.env.ASYMPTOTE_API_KEY = "secret-key";
    process.env.OTEL_EXPORTER_OTLP_ENDPOINT = "http://collector.example:4318";

    const config = resolveExporterConfig();

    expect(config.mode).toBe("otlp");
    expect(config.observeUrl).toBe("http://collector.example:4318/v1/observe");
    expect(config.headers.authorization).toBeUndefined();
    expect(JSON.stringify(config.headers)).not.toContain("secret-key");
  });

  it("does not send the api key to a foreign OTLP endpoint option", () => {
    const config = resolveExporterConfig({ apiKey: "secret-key", otlpEndpoint: "https://otel.vendor.example" });

    expect(config.mode).toBe("otlp");
    expect(config.headers.authorization).toBeUndefined();
  });

  it("does not treat a different port or scheme as the Asymptote origin", () => {
    expect(
      resolveExporterConfig({ apiKey: "k", otlpEndpoint: "https://api.asymptotelabs.ai:8443" }).headers.authorization,
    ).toBeUndefined();
    expect(
      resolveExporterConfig({ apiKey: "k", otlpEndpoint: "http://api.asymptotelabs.ai" }).headers.authorization,
    ).toBeUndefined();
  });

  it("sends the api key to an OTLP endpoint on the Asymptote origin", () => {
    const config = resolveExporterConfig({ apiKey: "test-key", otlpEndpoint: "https://api.asymptotelabs.ai:443/v1/observe" });

    expect(config.mode).toBe("otlp");
    expect(config.headers.authorization).toBe("Bearer test-key");
  });

  it("sends the api key to an OTLP endpoint on a configured base URL origin", () => {
    process.env.ASYMPTOTE_BASE_URL = "https://observe.example";
    process.env.OTEL_EXPORTER_OTLP_ENDPOINT = "https://observe.example/";

    const config = resolveExporterConfig({ apiKey: "test-key" });

    expect(config.mode).toBe("otlp");
    expect(config.headers.authorization).toBe("Bearer test-key");
  });

  it("refuses to send the api key over http to a non-loopback base URL", () => {
    expect(() => resolveExporterConfig({ apiKey: "test-key", baseUrl: "http://observe.example" })).toThrow(
      /refuses to send ASYMPTOTE_API_KEY over http/,
    );
  });

  it("refuses an insecure base URL from env when the key would be sent", () => {
    process.env.ASYMPTOTE_API_KEY = "env-key";
    process.env.ASYMPTOTE_BASE_URL = "http://observe.example";

    expect(() => resolveExporterConfig()).toThrow(/https/);
  });

  it("ignores an insecure base URL when the key goes nowhere", () => {
    process.env.ASYMPTOTE_BASE_URL = "http://observe.example";

    const config = resolveExporterConfig({ apiKey: "k", otlpEndpoint: "http://127.0.0.1:4318" });

    expect(config.headers.authorization).toBeUndefined();
  });

  it("allows the api key over http to loopback base URLs", () => {
    for (const baseUrl of ["http://127.0.0.1:4318", "http://localhost:4318", "http://[::1]:4318"]) {
      const config = resolveExporterConfig({ apiKey: "test-key", baseUrl });

      expect(config.mode).toBe("hosted");
      expect(config.headers.authorization).toBe("Bearer test-key");
    }
  });

  it("preserves a caller-supplied authorization header", () => {
    const hosted = resolveExporterConfig({ apiKey: "test-key", headers: { Authorization: "Basic custom" } });
    expect(hosted.headers).toEqual({ Authorization: "Basic custom" });

    const foreign = resolveExporterConfig({
      apiKey: "test-key",
      otlpEndpoint: "http://collector.example:4318",
      headers: { authorization: "Bearer collector-token" },
    });
    expect(foreign.headers).toEqual({ authorization: "Bearer collector-token" });
  });

  it("does not attach the api key in custom mode", () => {
    const config = resolveExporterConfig({ apiKey: "test-key", disableDefaultExporter: true });

    expect(config.mode).toBe("custom");
    expect(config.headers.authorization).toBeUndefined();
  });

  it("throws when neither hosted credentials nor explicit OTLP are configured", () => {
    expect(() => resolveExporterConfig()).toThrow(/ASYMPTOTE_API_KEY/);
  });

  it("allows custom mode when default exporter is disabled", () => {
    const config = resolveExporterConfig({ disableDefaultExporter: true });

    expect(config.mode).toBe("custom");
    expect(config.observeUrl).toBeUndefined();
  });
});

describe("sessionAttributes", () => {
  it("builds beacon and gen_ai session identity attributes", () => {
    expect(sessionAttributes("session-42")).toEqual({
      "beacon.session.id": "session-42",
      "gen_ai.conversation.id": "session-42",
    });
  });

  it("includes the working directory when provided", () => {
    expect(sessionAttributes("session-42", "/srv/agent")).toEqual({
      "beacon.session.id": "session-42",
      "gen_ai.conversation.id": "session-42",
      "beacon.session.working_directory": "/srv/agent",
    });
  });

  it("is exposed on the Observe facade", () => {
    expect(Observe.sessionAttributes("session-42")).toEqual(sessionAttributes("session-42"));
  });
});

describe("Asymptote Observe SDK", () => {
  beforeEach(() => {
    clearEnv();
  });

  afterEach(async () => {
    await shutdown();
    clearEnv();
  });

  it("initializes with a custom exporter and exports observe spans", async () => {
    const exporter = new InMemorySpanExporter();
    initialize({ spanExporter: exporter, disableDefaultExporter: true, disableBatch: true });

    const wrapped = observe(
      {
        name: "agent.plan",
        attributes: {
          [ATTR_BEACON_HARNESS_NAME]: "custom_agent",
          [ATTR_BEACON_EVENT_ACTION]: "tool.invoked",
        },
      },
      (value: string) => value.toUpperCase(),
    );

    expect(wrapped("ship it")).toBe("SHIP IT");
    await flush();

    const spans = exporter.getFinishedSpans();
    expect(spans).toHaveLength(1);
    expect(spans[0].name).toBe("agent.plan");
    expect(spans[0].attributes[ATTR_BEACON_HARNESS_NAME]).toBe("custom_agent");
    expect(spans[0].attributes[ATTR_BEACON_EVENT_ACTION]).toBe("tool.invoked");
    expect(spans[0].attributes["asymptote.observe.input.count"]).toBe(1);
    expect(spans[0].attributes["asymptote.observe.output.type"]).toBe("string");
    expect(spans[0].resource.attributes["telemetry.sdk.version"]).toBe(packageVersion());
  });

  it("respects observe input/output suppression", async () => {
    const exporter = new InMemorySpanExporter();
    initialize({ spanExporter: exporter, disableDefaultExporter: true, disableBatch: true });

    const wrapped = observe({ name: "agent.redacted", ignoreInput: true, ignoreOutput: true }, () => "secret");

    wrapped();
    await flush();

    const [span] = exporter.getFinishedSpans();
    expect(span.attributes["asymptote.observe.input.count"]).toBeUndefined();
    expect(span.attributes["asymptote.observe.output.type"]).toBeUndefined();
  });

  it("records errors and ends failed observe spans", async () => {
    const exporter = new InMemorySpanExporter();
    initialize({ spanExporter: exporter, disableDefaultExporter: true, disableBatch: true });

    const wrapped = observe({ name: "agent.fail" }, () => {
      throw new Error("boom");
    });

    expect(() => wrapped()).toThrow("boom");
    await flush();

    const [span] = exporter.getFinishedSpans();
    expect(span.status.code).toBe(SpanStatusCode.ERROR);
    expect(span.status.message).toBe("boom");
  });

  it("rejects conflicting reinitialization", () => {
    initialize({ disableDefaultExporter: true });

    expect(isInitialized()).toBe(true);
    expect(() => initialize({ disableDefaultExporter: true })).toThrow(/already initialized/);
  });

  it("does not wrap Claude Agent query twice when initialized again with instrumented modules", async () => {
    const exporter = new InMemorySpanExporter();
    const claudeAgentSDK = {
      query: async (prompt: string) => ({ ok: true, prompt }),
    };
    initialize({
      spanExporter: exporter,
      disableDefaultExporter: true,
      disableBatch: true,
      instrumentModules: { claudeAgentSDK },
    });

    initialize({ instrumentModules: { claudeAgentSDK } });

    await expect(claudeAgentSDK.query("explain tracing")).resolves.toEqual({ ok: true, prompt: "explain tracing" });
    await flush();

    expect(exporter.getFinishedSpans().filter(span => span.name === "claude_agent_sdk.query")).toHaveLength(1);
  });

  it("creates OpenLLMetry instrumentations", () => {
    const instrumentations = initializeAsymptoteInstrumentations();

    expect(instrumentations.map(instrumentation => instrumentation.instrumentationName)).toEqual(
      expect.arrayContaining([
        "@traceloop/instrumentation-openai",
        "@traceloop/instrumentation-anthropic",
      ]),
    );
    instrumentations.forEach(instrumentation => instrumentation.disable());
  });

  it("allows disabling individual auto-instrumentations", () => {
    const instrumentations = initializeAsymptoteInstrumentations({ anthropic: false });

    expect(instrumentations.map(instrumentation => instrumentation.instrumentationName)).toEqual([
      "@traceloop/instrumentation-openai",
    ]);
    instrumentations.forEach(instrumentation => instrumentation.disable());
  });

  it("wraps Claude Agent query functions with Beacon-compatible prompt spans", async () => {
    const exporter = new InMemorySpanExporter();
    initialize({ spanExporter: exporter, disableDefaultExporter: true, disableBatch: true });
    const query = wrapClaudeAgentQuery(async (prompt: string) => ({ ok: true, prompt }));

    await expect(query("explain memoization")).resolves.toEqual({ ok: true, prompt: "explain memoization" });
    await flush();

    const [span] = exporter.getFinishedSpans();
    expect(span.name).toBe("claude_agent_sdk.query");
    expect(span.attributes[ATTR_BEACON_ORIGIN]).toBe("cloud");
    expect(span.attributes[ATTR_BEACON_HARNESS_NAME]).toBe("claude_agent_sdk");
    expect(span.attributes[ATTR_BEACON_EVENT_ACTION]).toBe("prompt.submitted");
    expect(span.attributes[ATTR_BEACON_EVENT_CATEGORY]).toBe("prompt");
    expect(span.attributes[ATTR_BEACON_PROMPT_TEXT]).toBe("explain memoization");
  });

  it("keeps Claude async iterable spans open until consumed", async () => {
    const exporter = new InMemorySpanExporter();
    initialize({ spanExporter: exporter, disableDefaultExporter: true, disableBatch: true });
    const query = wrapClaudeAgentQuery(async function* (_prompt: string) {
      yield { type: "message", text: "one" };
      yield { type: "message", text: "two" };
    });

    const messages = [];
    for await (const message of query("stream please")) {
      messages.push(message);
    }
    await flush();

    expect(messages).toHaveLength(2);
    expect(exporter.getFinishedSpans()).toHaveLength(1);
  });

  it("preserves stream control methods and their original receiver", async () => {
    const exporter = new InMemorySpanExporter();
    initialize({ spanExporter: exporter, disableDefaultExporter: true, disableBatch: true });
    class Stream {
      #interrupted = false;
      async interrupt() { this.#interrupted = true; }
      get interrupted() { return this.#interrupted; }
      async *[Symbol.asyncIterator]() { if (!this.#interrupted) yield "message"; }
    }
    const original = new Stream();
    const query = wrapClaudeAgentQuery(() => original);
    const stream = query();
    expect(stream.interrupt).toBe(stream.interrupt);
    await stream.interrupt();
    expect(original.interrupted).toBe(true);
    expect(stream.interrupted).toBe(true);
    const messages = [];
    for await (const message of stream) messages.push(message);
    expect(messages).toEqual([]);
    await flush();
    expect(exporter.getFinishedSpans()).toHaveLength(1);
  });

  it("keeps direct iterator methods traced when a control closes the real source", async () => {
    const exporter = new InMemorySpanExporter();
    initialize({ spanExporter: exporter, disableDefaultExporter: true, disableBatch: true });
    let cleaned = false;
    const source = (async function* () {
      try { yield "first"; yield "second"; } finally { cleaned = true; }
    })();
    const interrupt = async () => { await source.return(); };
    const original = Object.assign(source, { interrupt, stop: interrupt });
    Object.defineProperty(original, "stop", { value: original.interrupt, configurable: false, writable: false });
    const stream = wrapClaudeAgentQuery(() => original)();
    expect(stream[Symbol.asyncIterator]()).toBe(stream);
    expect(await stream.next()).toEqual({ value: "first", done: false });
    await stream.stop();
    expect(cleaned).toBe(true);
    expect(await stream.next()).toEqual({ value: undefined, done: true });
    await flush();
    expect(exporter.getFinishedSpans()).toHaveLength(1);
  });

  it("traces explicit iterator throw and preserves source cleanup", async () => {
    const exporter = new InMemorySpanExporter();
    initialize({ spanExporter: exporter, disableDefaultExporter: true, disableBatch: true });
    let cleaned = false;
    const original = (async function* () {
      try { yield "first"; } finally { cleaned = true; }
    })();
    const stream = wrapClaudeAgentQuery(() => original)();
    await stream.next();
    const failure = new Error("consumer failed");
    await expect(stream.throw(failure)).rejects.toBe(failure);
    expect(cleaned).toBe(true);
    await flush();
    expect(exporter.getFinishedSpans()).toHaveLength(1);
    expect(exporter.getFinishedSpans()[0]?.status.code).toBe(SpanStatusCode.ERROR);
  });

  it("ends async iterable spans when the caller stops consumption early", async () => {
    const exporter = new InMemorySpanExporter();
    initialize({ spanExporter: exporter, disableDefaultExporter: true, disableBatch: true });
    let cleaned = false;
    const query = wrapClaudeAgentQuery(async function* () {
      try { yield { type: "first" }; yield { type: "second" }; }
      finally { cleaned = true; }
    });
    for await (const _message of query()) { break; }
    await flush();
    expect(cleaned).toBe(true);
    expect(exporter.getFinishedSpans()).toHaveLength(1);
  });
  it("provides a tracer compatible with Vercel-style telemetry hooks", async () => {
    const exporter = new InMemorySpanExporter();
    initialize({ spanExporter: exporter, disableDefaultExporter: true, disableBatch: true });

    await fakeGenerateText({
      experimental_telemetry: {
        isEnabled: true,
        tracer: Observe.getTracer(),
      },
    });
    await flush();

    const [span] = exporter.getFinishedSpans();
    expect(span.name).toBe("ai.generateText");
    expect(span.attributes[ATTR_BEACON_HARNESS_NAME]).toBe("vercel_ai_sdk");
    expect(span.attributes[ATTR_BEACON_EVENT_ACTION]).toBe("prompt.submitted");
  });

  it("exports OTLP over HTTP with hosted auth headers", async () => {
    const requests: CapturedRequest[] = [];
    const server = createServer(async (req, res) => {
      requests.push({
        method: req.method ?? "",
        url: req.url ?? "",
        authorization: req.headers.authorization,
        body: await readRequestBody(req),
      });
      res.statusCode = 200;
      res.end();
    });
    await listen(server);
    try {
      const address = server.address();
      if (!address || typeof address === "string") {
        throw new Error("unexpected server address");
      }
      initialize({
        apiKey: "hosted-key",
        baseUrl: `http://127.0.0.1:${address.port}`,
        disableBatch: true,
      });

      const traced = observe({ name: "agent.http" }, () => "ok");
      traced();
      await flush();

      expect(requests).toHaveLength(1);
      expect(requests[0].method).toBe("POST");
      expect(requests[0].url).toBe("/v1/observe");
      expect(requests[0].authorization).toBe("Bearer hosted-key");
      expect(requests[0].body.length).toBeGreaterThan(0);
    } finally {
      await close(server);
    }
  });
});

interface CapturedRequest {
  method: string;
  url: string;
  authorization: string | undefined;
  body: Buffer;
}

function clearEnv(): void {
  for (const key of envKeys) {
    delete process.env[key];
  }
}

function packageVersion(): string {
  const packageJSON = JSON.parse(readFileSync(new URL("../package.json", import.meta.url), "utf8")) as { version: string };
  return packageJSON.version;
}

async function fakeGenerateText(options: { experimental_telemetry?: { isEnabled?: boolean; tracer?: ReturnType<typeof getTracer> } }): Promise<void> {
  if (!options.experimental_telemetry?.isEnabled || !options.experimental_telemetry.tracer) {
    return;
  }
  options.experimental_telemetry.tracer.startActiveSpan(
    "ai.generateText",
    {
      attributes: {
        [ATTR_BEACON_HARNESS_NAME]: "vercel_ai_sdk",
        [ATTR_BEACON_EVENT_ACTION]: "prompt.submitted",
      },
    },
    span => {
      span.end();
    },
  );
}

function readRequestBody(req: IncomingMessage): Promise<Buffer> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = [];
    req.on("data", chunk => chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk)));
    req.on("end", () => resolve(Buffer.concat(chunks)));
    req.on("error", reject);
  });
}

function listen(server: ReturnType<typeof createServer>): Promise<void> {
  return new Promise(resolve => {
    server.listen(0, "127.0.0.1", resolve);
  });
}

function close(server: ReturnType<typeof createServer>): Promise<void> {
  return new Promise((resolve, reject) => {
    server.close(error => (error ? reject(error) : resolve()));
  });
}
