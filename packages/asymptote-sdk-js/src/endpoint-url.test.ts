import { createServer } from "node:http";
import { afterEach, expect, it } from "vitest";
import { initialize, observe, flush, shutdown, resolveExporterConfig } from "./index.js";

afterEach(async () => { await shutdown(); });

for (const [name, suffix, expected] of [
  ["base trailing slash", "/", "/v1/observe"],
  ["complete path", "/v1/observe/", "/v1/observe"],
  ["complete path with query", "/v1/observe?project=fixture", "/v1/observe?project=fixture"],
  ["base path with query", "/collector/?project=fixture", "/collector/v1/observe?project=fixture"],
]) {
  it(`exports to the expected loopback route for ${name}`, async () => {
    const requests: { url: string | undefined; authorization: string | undefined; status: number; body: string }[] = [];
    const server = createServer((req, res) => {
      const chunks: Buffer[] = [];
      req.on("data", chunk => chunks.push(Buffer.from(chunk)));
      req.on("end", () => {
        const status = req.url === expected ? 200 : 404;
        requests.push({ url: req.url, authorization: req.headers.authorization, status, body: Buffer.concat(chunks).toString() });
        res.writeHead(status, { "Content-Type": "application/json" });
        res.end(status === 200 ? "{}" : '{"error":"unknown route"}');
      });
    });
    await new Promise<void>(resolve => server.listen(0, "127.0.0.1", resolve));
    const address = server.address();
    if (!address || typeof address === "string") throw new Error("missing owned listener");
    const endpoint = `http://127.0.0.1:${address.port}${suffix}`;
    try {
      initialize({ otlpEndpoint: endpoint, disableBatch: true, disableInstrumentations: true });
      observe({ name: "owned-export-fixture" }, () => "fixture")();
      await flush();
      expect(requests).toHaveLength(1);
      expect(requests[0].url).toBe(expected);
      expect(requests[0].status).toBe(200);
      expect(requests[0].authorization).toBeUndefined();
      expect(JSON.parse(requests[0].body).resourceSpans).toHaveLength(1);
    } finally {
      await shutdown();
      server.closeAllConnections();
      await new Promise<void>((resolve, reject) => server.close(error => error ? reject(error) : resolve()));
    }
  });
}

it("normalizes only the pathname, preserving encoded query values and fragment", () => {
  expect(resolveExporterConfig({ otlpEndpoint: "https://collector.example/base///?project=a%2Fb&tag=x&tag=y#local" }).observeUrl)
    .toBe("https://collector.example/base/v1/observe?project=a%2Fb&tag=x&tag=y#local");
  expect(resolveExporterConfig({ otlpEndpoint: "https://collector.example/v1/observe///?project=fixture#local" }).observeUrl)
    .toBe("https://collector.example/v1/observe?project=fixture#local");
});

it("rejects invalid or relative endpoints instead of constructing an unusable exporter URL", () => {
  for (const otlpEndpoint of ["/collector", "not a URL"]) {
    expect(() => resolveExporterConfig({ otlpEndpoint })).toThrow(TypeError);
  }
});
