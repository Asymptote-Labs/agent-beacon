## Finding: User-mode collector service does not stay running

- Area: endpoint
- Severity: blocker
- Environment: macOS 15.5 arm64, Beacon 0.0.12, Homebrew install from `asymptote-labs/tap`
- Expected: After `beacon endpoint install`, `beacon endpoint status` should report the collector service running and OTLP ports listening on `127.0.0.1:4317` and `127.0.0.1:4318`.
- Actual: `beacon endpoint install` exits successfully and writes config/log files, but repeated status checks report `Collector: grpc=false http=false (Collector ports are not listening)` and JSON status reports `service.loaded=true` with `service.running=false`.
- Reproduction: `brew tap asymptote-labs/tap && brew install beacon && beacon endpoint install && beacon endpoint status && sleep 5 && beacon endpoint status --json`
- Evidence: `/Users/shukan/beacon-e2e-20260518-184846/endpoint-status-recheck.txt`, `/Users/shukan/beacon-e2e-20260518-184846/status-after-install-recheck.json`, and `/tmp/com.beacon.endpoint.collector.user.err`
- Suspected cause: Beacon CLI 0.0.12 writes `include_runtime_metrics: false` into the `beaconjson` exporter config by default, but the bundled Homebrew `beacon-otelcol` rejects that key during config decode. This indicates a CLI/collector release artifact mismatch or an insufficient release smoke gate.
- Proposed fix: Omit the `include_runtime_metrics` exporter key unless the user explicitly opts in, and add release validation that runs the generated endpoint collector config through the bundled collector before publishing.

## Finding: Homebrew arm64 release embeds amd64 hook adapter

- Area: packaging
- Severity: blocker
- Environment: macOS 15.5 arm64, Beacon 0.0.12, Homebrew install from `asymptote-labs/tap`, Cursor 3.4.20
- Expected: `beacon endpoint hooks install --harness cursor` should install a usable `beacon-hooks` adapter under `~/.beacon/endpoint/hooks/` on Apple Silicon.
- Actual: Hook installation fails with `embedded hooks binary is not usable on this host: architecture mismatch: embedded hooks binary is darwin/amd64 but this CLI is darwin/arm64`.
- Reproduction: `beacon endpoint install && beacon endpoint hooks install --harness cursor`
- Evidence: `/Users/shukan/beacon-e2e-20260518-184846/cursor-hooks.txt`
- Suspected cause: GoReleaser target builds write platform-specific hook binaries to the same `cli/beacon/internal/embedded/hooks.bin` path. Parallel target builds can race, allowing the darwin/arm64 CLI archive to embed a darwin/amd64 hook adapter.
- Proposed fix: Serialize GoReleaser builds that mutate the shared embedded hook path, keep the embedded architecture test failing hard for real mismatches, and validate hook installation from each release candidate archive before publishing.

## Finding: Elastic docs query uses pre-ingest field names

- Area: elastic
- Severity: minor
- Environment: macOS 15.5 arm64, Beacon 0.0.12 with local fixes, local Elastic stack from `beacon endpoint elastic up`
- Expected: The documented validation query `logs-beacon.endpoint-*/_search?q=product:endpoint-agent` should find ingested Beacon events.
- Actual: The query returns zero hits because the ingest pipeline stores Beacon release-contract fields under `beacon.*`; `product` becomes `beacon.product`, `harness.name` becomes `beacon.harness.name`, and prompt text becomes `beacon.prompt.text`.
- Reproduction: `curl "http://localhost:9200/logs-beacon.endpoint-*/_search?q=product:endpoint-agent"` after local Filebeat ingestion starts.
- Evidence: `/Users/shukan/beacon-e2e-20260518-184846/elastic-search-product-retry.json`, `/Users/shukan/beacon-e2e-20260518-184846/elastic-one-marker-full.json`, and `/Users/shukan/beacon-e2e-20260518-184846/elastic-field-caps.json`
- Suspected cause: The docs and runbook validate against raw JSONL field names, while the Elastic ingest pipeline intentionally maps Beacon fields into `beacon.*` ECS-adjacent fields.
- Proposed fix: Update Elastic validation docs and runbook examples to query `beacon.product:endpoint-agent`, `beacon.harness.name:<runtime>`, and `beacon.prompt.text:"Beacon E2E"`.

## Finding: Kibana data view name differs from docs

- Area: elastic
- Severity: polish
- Environment: macOS 15.5 arm64, Beacon 0.0.12 with local fixes, local Elastic stack from `beacon endpoint elastic up`
- Expected: Docs say to select the `Beacon Endpoint Events` data view in Kibana Discover.
- Actual: The saved object installed by the local pack is named `Beacon Endpoint` with title `logs-beacon.endpoint-*`.
- Reproduction: Start the local Elastic stack and query Kibana saved objects with `curl -H 'kbn-xsrf: beacon-e2e' 'http://localhost:5601/api/saved_objects/_find?type=index-pattern&search=Beacon%20Endpoint%20Events&per_page=10'`.
- Evidence: `/Users/shukan/beacon-e2e-20260518-184846/kibana-data-view-search.json`
- Suspected cause: Docs and Kibana saved object naming drifted.
- Proposed fix: Either rename the data view saved object to `Beacon Endpoint Events` or update docs to tell users to select `Beacon Endpoint`.
