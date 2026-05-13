# Beacon Endpoint Agent

Local endpoint telemetry for AI agent runtimes.

Beacon Endpoint Agent configures local telemetry for tools like Claude Code,
Codex CLI, Claude Cowork, and Cursor, then writes Wazuh-compatible JSONL logs.
It runs local-only and does not require a Beacon account.

Beacon is visibility-first. The current public build focuses on observing local
agent runtime activity, normalizing it into endpoint events, and leaving
forwarding to existing localfile/Wazuh or customer-managed pipelines.

## What It Captures

Beacon can currently:

- Discover supported local agent runtimes: Claude Code, Codex CLI, Cursor, and
  Claude Cowork.
- Configure Claude Code and Codex CLI to export OpenTelemetry to a localhost
  collector.
- Install Cursor hooks that emit local endpoint events for sessions, prompt
  submission, tool use, command execution, MCP-like tool activity, approval
  decisions, and file edits where Cursor exposes those hook payloads.
- Convert OTLP logs, traces, metrics, and resource attributes into Beacon
  endpoint JSONL with the `beaconjson` collector exporter.
- Write Wazuh-compatible JSONL to a local runtime log.
- Run a local-only dashboard for inspecting runtime inventory, summaries,
  timelines, filters, and event details from the JSONL log.
- Generate Wazuh localfile/rule content for the Beacon event schema.

Beacon records metadata by default. Content retention is configurable with:

- `metadata`: default; no prompt text, raw attributes, command output, or raw
  diffs.
- `redacted`: include configured content fields after local redaction and size
  limits.
- `full`: include configured content fields in local/customer-controlled logs,
  still subject to event size limits.

Beacon does not currently provide kernel/process monitoring, shell history
collection, cloud audit ingestion, browser/SaaS telemetry, credential-use
attribution, MCP configuration inventory, or direct Datadog/Splunk/Elastic/etc.
exporters.

## Quick Start

Build the CLI:

```bash
cd cli/beacon
make build
```

Install in user mode:

```bash
./beacon endpoint install --user
./beacon endpoint status
```

Install with explicit content retention:

```bash
./beacon endpoint install --user --content-retention metadata
```

Print Wazuh config and validate event output:

```bash
./beacon endpoint wazuh print-config --user
./beacon endpoint wazuh validate --user
```

Run the macOS endpoint smoke test:

```bash
sh packaging/macos/smoke-endpoint.sh
```

Optional integrations:

```bash
./beacon endpoint hooks install --harness cursor --user
./beacon endpoint integrations claude-cowork print-config --user
```

Run the local dashboard:

```bash
./beacon endpoint dashboard --user
./beacon endpoint dashboard --user --open
```

Uninstall:

```bash
./beacon endpoint uninstall --user --keep-logs
```

## Commands

- `beacon endpoint install`: configure the endpoint agent, Collector service, and Claude/Codex telemetry.
- `beacon endpoint repair`: reapply service/config files and repair telemetry drift.
- `beacon endpoint status`: show Collector, service, harness, and diagnostic status.
- `beacon endpoint discover`: list supported local AI runtimes.
- `beacon endpoint dashboard`: run a localhost-only dashboard over the runtime JSONL log.
- `beacon endpoint hooks`: install, check, or remove hook-based integrations such as Cursor.
- `beacon endpoint integrations claude-cowork`: print setup and validate admin-configured Cowork OTLP export.
- `beacon endpoint wazuh`: print/install Wazuh content and write a validation event.
- `beacon endpoint uninstall`: stop services and remove managed endpoint files.

## Repository Layout

- `cli/beacon`: public `beacon` CLI.
- `cli/beacon-hooks`: hook adapter invoked by supported agent runtimes.
- `collector-builder`: custom OpenTelemetry Collector distribution and
  `beaconjson` exporter.
- `packaging`: macOS packaging and MDM deployment assets.

## Release Readiness

Release builds should include the `beacon` CLI with a platform-matched embedded
hook adapter, the `beacon-otelcol` collector distribution, Wazuh content,
SHA-256 checksums, and concise notes covering supported runtimes,
content-retention defaults, log paths, and uninstall behavior.

For macOS, publish a signed and notarized package or Homebrew formula that
installs the CLI, collector, Wazuh content pack, and deployment scripts. The
package should apply explicit endpoint settings, for example:

```bash
beacon endpoint install --harness claude,codex --content-retention metadata
```

Before publishing a release, verify the build from a clean checkout and clean
macOS host or VM:

- `beacon version` reports the expected version, commit, and build date.
- `beacon endpoint install --user --no-start` succeeds without developer
  tooling.
- `beacon endpoint status --user` reports config, collector, service, harness,
  diagnostic, and runtime log paths.
- `beacon endpoint wazuh validate --user` writes a valid Beacon JSONL event.
- `beacon endpoint dashboard --user` starts on `127.0.0.1`, serves a read-only
  dashboard, and can search local telemetry without external network
  dependencies.
- `beacon endpoint uninstall --user` removes managed service and config files.
- macOS package signature and notarization are valid when distributing a `.pkg`.

The repository smoke test keeps this flow local and non-root:

```bash
sh packaging/macos/smoke-endpoint.sh
```

It builds a temporary Beacon binary, uses a temporary `HOME`, runs a user-mode
install with `--no-start`, validates status and Wazuh output, checks Cursor hook
install/status, uninstalls, and preserves the runtime log long enough to assert
expected events were written. The script skips automatically on non-macOS hosts.

## Test

```bash
cd cli/beacon
go test ./...
go test -race ./internal/endpoint/...

cd ../beacon-hooks
go test ./...

cd ../../collector-builder/exporter/beaconjsonexporter
go test ./...
```
