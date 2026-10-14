import * as Anthropic from "@anthropic-ai/sdk";
import { InMemorySpanExporter } from "@opentelemetry/sdk-trace-base";
import { afterEach, beforeEach, expect, it, vi, type MockInstance } from "vitest";
import { flush, initialize, observe, shutdown } from "./index.js";

let warn: MockInstance<typeof console.warn>;
beforeEach(() => { warn = vi.spyOn(console, "warn").mockImplementation(() => {}); });
afterEach(async () => { warn.mockRestore(); await shutdown(); });

// Each integration guide calls initialize({ apiKey, instrumentModules }); an app following two of
// them repeats the same options and must neither fail nor warn.
it("accepts a repeat of the active options with additional modules", () => {
  const headers = { "x-team": "core" };
  initialize({ apiKey: "key-1", headers, disableDefaultExporter: true, disableInstrumentations: true });
  expect(() => initialize({ apiKey: "key-1", headers: { "x-team": "core" }, instrumentModules: { Anthropic } })).not.toThrow();
  expect(warn).not.toHaveBeenCalled();
});

it("keeps the active configuration and names the options it ignores", async () => {
  const original = new InMemorySpanExporter();
  const replacement = new InMemorySpanExporter();
  initialize({ apiKey: "key-1", serviceName: "original-service", spanExporter: original, disableBatch: true, disableInstrumentations: true });
  initialize({ apiKey: "key-2", serviceName: "original-service", spanExporter: replacement, instrumentModules: { Anthropic } });
  expect(warn).toHaveBeenCalledTimes(1);
  const message = String(warn.mock.calls[0][0]);
  expect(message).toContain("apiKey, spanExporter");
  expect(message).not.toContain("serviceName");
  expect(message).not.toContain("key-2");
  observe({ name: "original-provider" }, () => 1)();
  await flush();
  expect(original.getFinishedSpans()).toHaveLength(1);
  expect(replacement.getFinishedSpans()).toHaveLength(0);
});

it("does not treat an explicitly undefined option as a change", () => {
  initialize({ disableDefaultExporter: true, disableInstrumentations: true });
  initialize({ serviceName: undefined, instrumentModules: { Anthropic } });
  expect(warn).not.toHaveBeenCalled();
});
