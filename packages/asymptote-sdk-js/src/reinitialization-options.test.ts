import * as Anthropic from "@anthropic-ai/sdk";
import { InMemorySpanExporter } from "@opentelemetry/sdk-trace-base";
import { afterEach, expect, it } from "vitest";
import { flush, initialize, isInitialized, observe, shutdown } from "./index.js";

afterEach(async () => { await shutdown(); });

it("rejects changed configuration even when manual modules accompany it", async () => {
  const original = new InMemorySpanExporter();
  const replacement = new InMemorySpanExporter();
  initialize({ serviceName: "original-service", spanExporter: original, disableBatch: true, disableInstrumentations: true });
  expect(() => initialize({ serviceName: "replacement-service", spanExporter: replacement, instrumentModules: { Anthropic } })).toThrow(/already initialized/);
  expect(isInitialized()).toBe(true);
  observe({ name: "original-provider-control" }, () => 1)();
  await flush();
  expect(original.getFinishedSpans()).toHaveLength(1);
  expect(original.getFinishedSpans()[0].resource.attributes["service.name"]).toBe("original-service");
  expect(replacement.getFinishedSpans()).toHaveLength(0);
});

it("continues to accept the documented modules-only initialization shortcut", async () => {
  const exporter = new InMemorySpanExporter();
  initialize({ serviceName: "original-service", spanExporter: exporter, disableBatch: true, disableInstrumentations: true });
  expect(() => initialize({ instrumentModules: { Anthropic } })).not.toThrow();
  observe({ name: "modules-only-control" }, () => 1)();
  await flush();
  expect(exporter.getFinishedSpans()).toHaveLength(1);
  expect(exporter.getFinishedSpans()[0].resource.attributes["service.name"]).toBe("original-service");
});

it("does not treat an explicitly undefined option as a configuration change", () => {
  initialize({ disableDefaultExporter: true, disableInstrumentations: true });
  expect(() => initialize({ serviceName: undefined, instrumentModules: { Anthropic } })).not.toThrow();
});
