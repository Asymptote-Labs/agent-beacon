import { InMemorySpanExporter } from "@opentelemetry/sdk-trace-base";
import { expect, it } from "vitest";
import { flush, initialize, isInitialized, observe, shutdown } from "./index.js";

class RejectingShutdownExporter extends InMemorySpanExporter {
  override async shutdown(): Promise<void> {
    throw new Error("synthetic exporter shutdown failure");
  }
}

it("clears a stopped provider after reporting exporter shutdown failure", async () => {
  const first = new RejectingShutdownExporter();
  initialize({ serviceName: "first-service", spanExporter: first, disableBatch: true, disableInstrumentations: true });
  observe({ name: "first-control" }, () => "first")();
  await flush();
  expect(first.getFinishedSpans()).toHaveLength(1);
  await expect(shutdown()).rejects.toThrow("synthetic exporter shutdown failure");
  expect(isInitialized()).toBe(false);

  const replacement = new InMemorySpanExporter();
  initialize({ serviceName: "replacement-service", spanExporter: replacement, disableBatch: true, disableInstrumentations: true });
  try {
    observe({ name: "replacement-control" }, () => "replacement")();
    await flush();
    expect(replacement.getFinishedSpans()).toHaveLength(1);
    expect(replacement.getFinishedSpans()[0].resource.attributes["service.name"]).toBe("replacement-service");
  } finally {
    await shutdown();
  }
});
