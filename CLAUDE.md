# CLAUDE.md

Guidance for Claude Code and other coding agents working in this repository.

## Project Scope

Beacon Endpoint Agent is a local-only endpoint telemetry agent for AI runtimes. The shipping code paths are:

- `cli/beacon`: public `beacon` CLI and endpoint runtime.
- `cli/beacon-hooks`: hook adapter invoked by Cursor and other supported runtimes.
- `collector-builder`: OpenTelemetry Collector distribution and Beacon JSONL exporter.
- `packaging`: macOS packaging and deployment assets.

Do not recreate or depend on removed `asymptote` mirror trees. Keep new work focused on the Beacon paths above.

## Product Posture

- Beacon is visibility-first endpoint telemetry for local AI agent runtimes, not a hosted policy service or general endpoint protection product.
- Preserve the local-only product posture. The public Beacon build should not require a hosted account, remote policy fetch, hosted dashboard, or external network dependency during normal hook execution.
- Do not add dependency vulnerability scanning, OSV/GHSA lookups, package remediation, or other vulnerability-enforcement flows to the public hook path.
- Do not add broad runtime enforcement unless explicitly requested. Current control behavior is limited to hook-native approvals/denials exposed by supported agent runtimes.
- Keep direct destination support scoped to local JSONL/Wazuh unless explicitly requested. Other SIEM/observability systems should consume the local output through customer-managed forwarding.
- Default content retention is `full`: configured prompt text, command output, raw tool inputs, raw OTLP attributes, and raw diffs may be written to local or customer-controlled logs, still subject to local redaction and size limits where supported. Keep `metadata` and `redacted` modes available for stricter deployments.

## Telemetry Scope

Supported runtime surfaces today:

- Claude Code and Codex CLI telemetry configuration through local OpenTelemetry settings.
- Cursor hook telemetry for sessions, prompt submission, tool use, command execution, MCP-like tool activity, approval decisions, and file edits where hook payloads expose those fields.
- Claude Cowork admin-configured OpenTelemetry setup guidance and local validation.
- `beaconjson` OpenTelemetry Collector exporter that converts OTLP logs, traces, metrics, and resource attributes into Beacon endpoint JSONL.
- A local-only dashboard served by `beacon endpoint dashboard`, bound to loopback by default and backed by the runtime JSONL log.

Current non-goals unless explicitly requested:

- Kernel/process monitoring, EDR replacement, shell history scraping, cloud audit ingestion, browser/SaaS telemetry, credential-use attribution, and MCP configuration inventory.
- Direct hosted integrations for Datadog, Splunk, Elastic, Snowflake, Chronicle, Panther, or other SIEM destinations.
- Dependency vulnerability scanning or package security remediation.

## Common Commands

Run tests for the public CLI:

```bash
cd cli/beacon
go test ./...
go test -race ./internal/endpoint/...
```

Run hook adapter tests:

```bash
cd cli/beacon-hooks
go test ./...
```

Run packaging wrapper checks:

```bash
sh packaging/macos/test-endpoint-scripts.sh
```

Run the macOS endpoint smoke test:

```bash
sh packaging/macos/smoke-endpoint.sh
```

Build the CLI:

```bash
cd cli/beacon
make build
```

Run Collector exporter tests:

```bash
cd collector-builder/exporter/beaconjsonexporter
go test ./...
```

Run the local dashboard during manual testing:

```bash
cd cli/beacon
go run . endpoint dashboard
```

## Implementation Notes

- Prefer deterministic tests that use `t.TempDir()`, `t.Setenv("HOME", ...)`, fake binaries, and free local ports.
- Avoid tests that require root, real `launchctl` service changes, Wazuh, a live collector, or external network access.
- For macOS-only behavior, gate tests with `runtime.GOOS == "darwin"` or assert the non-Darwin contract explicitly.
- Keep endpoint event schema fields stable: `vendor`, `product`, `schema_version`, required event fields, and Wazuh-compatible JSONL output are release contracts.
- Preserve optional event fields for agent-native metadata (`session`, `tool`, `file`, `command`, `mcp`, `approval`, `content`, `model`, `repository`, and `branch`) without changing existing required field semantics.
- When adding a new signal, include stable identifiers/counts/hashes alongside any retained raw content, and route raw fields through the configured content retention mode.
- Keep the dashboard read-only. It should inspect local status and JSONL events but must not mutate endpoint configuration or telemetry.
- Keep the release readiness guidance in `README.md` up to date when install, packaging, collector, or dashboard behavior changes.

## CI Expectations

CI runs:

- `go test ./...` in `cli/beacon`.
- `go test -race ./internal/endpoint/...` in `cli/beacon`.
- `go test ./...` in `cli/beacon-hooks`.
- `go test ./...` in `collector-builder/exporter/beaconjsonexporter`.
- CLI help smoke checks for the public command tree.
- macOS packaging script validation via `packaging/macos/test-endpoint-scripts.sh`.
- macOS endpoint smoke validation via `packaging/macos/smoke-endpoint.sh`.
