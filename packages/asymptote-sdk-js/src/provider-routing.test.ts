import * as Anthropic from "@anthropic-ai/sdk";
import { InMemorySpanExporter } from "@opentelemetry/sdk-trace-base";
import { afterEach, expect, it } from "vitest";
import { initialize, observe, flush, shutdown } from "./index.js";

const resources = [Anthropic.Anthropic.Completions, Anthropic.Anthropic.Messages, Anthropic.Anthropic.Beta.Messages];
const originals = resources.map(resource => Object.getOwnPropertyDescriptor(resource.prototype, "create")!);

afterEach(async () => {
  await shutdown();
  resources.forEach((resource, index) => { Object.defineProperty(resource.prototype, "create", originals[index]); });
});

it("routes real provider instrumentation to the new exporter after reinitialization", async () => {
  let fetches = 0;
  const client = new Anthropic.Anthropic({
    apiKey: "synthetic-fixture-only", baseURL: "http://127.0.0.1:1",
    fetch: async () => {
      fetches++;
      return new Response(JSON.stringify({ id: "msg_fixture", type: "message", role: "assistant", model: "synthetic-model", content: [{ type: "text", text: "fixture" }], stop_reason: "end_turn", stop_sequence: null, usage: { input_tokens: 1, output_tokens: 2 } }), { status: 200, headers: { "content-type": "application/json" } });
    },
  });
  const invoke = () => client.messages.create({ model: "synthetic-model", max_tokens: 8, messages: [{ role: "user", content: "offline fixture" }] });
  for (const serviceName of ["first-provider", "second-provider"]) {
    const exporter = new InMemorySpanExporter();
    initialize({ serviceName, spanExporter: exporter, disableDefaultExporter: true, disableBatch: true, instrumentationOptions: { openAI: false }, instrumentModules: { Anthropic } });
    observe({ name: "manual-control" }, () => "unchanged")();
    expect((await invoke()).content).toEqual([{ type: "text", text: "fixture" }]);
    await flush();
    const spans = exporter.getFinishedSpans();
    expect(spans.filter(span => span.name === "manual-control")).toHaveLength(1);
    expect(spans.filter(span => span.name !== "manual-control"), serviceName).toHaveLength(1);
    expect(spans.every(span => span.resource.attributes["service.name"] === serviceName)).toBe(true);
    await shutdown();
  }
  expect(fetches).toBe(2);
});
