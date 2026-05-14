# Beacon Endpoint Agent

Local endpoint telemetry for AI agent runtimes.

Beacon Endpoint Agent configures local telemetry for tools like Claude Code,
Codex CLI, Claude Cowork, and Cursor, then writes Wazuh-compatible JSONL logs.
It runs local-only and does not require a Beacon account.

Read the [Beacon CLI documentation](https://docs.asymptotelabs.ai/cli) for setup
guides and command details.

Beacon is visibility-first. The current public build focuses on observing local
agent runtime activity, normalizing it into endpoint events, and leaving
forwarding to existing localfile/Wazuh or customer-managed pipelines.

## Product Vision

Beacon is built around a simple thesis: as AI agents move from answering
questions to taking actions on laptops and workstations, the endpoint becomes
the place where intent, context, permissions, tools, credentials, and changes
come together.

The long-term vision is to make local agent activity observable,
understandable, and eventually governable across the enterprise. Beacon starts
with the visibility layer: a common, local-first record of what agents were
asked to do, what context they accessed, which tools and commands they used,
what files they touched, what approvals were granted, and what changed on the
endpoint.

Read more in
[Introducing Beacon: Endpoint Telemetry for AI Agents](https://justindsouza.substack.com/p/introducing-beacon-endpoint-telemetry).

## Table Of Contents

- [Product Vision](#product-vision)
- [What Beacon Does](#what-beacon-does)
- [Privacy And Retention](#privacy-and-retention)
- [What Beacon Does Not Do](#what-beacon-does-not-do)
- [Documentation](#documentation)
- [Quick Start](#quick-start)
- [Install With Homebrew](#install-with-homebrew)
- [Optional Integrations](#optional-integrations)
- [Claude Cowork Durable Collector](#claude-cowork-durable-collector)
- [Dashboard](#dashboard)
- [Command Reference](#command-reference)
- [Repository Layout](#repository-layout)
- [Release Readiness](#release-readiness)
- [Testing](#testing)

## What Beacon Does

Beacon can currently:

- **Runtime discovery:** discover supported local agent runtimes: Claude Code,
  Codex CLI, Cursor, and Claude Cowork.
- **Local OTLP setup:** configure Claude Code and Codex CLI to export
  OpenTelemetry to a localhost collector.
- **Cursor hooks:** install Cursor hooks that emit local endpoint events for
  sessions, prompt submission, tool use, command execution, MCP-like tool
  activity, approval decisions, and file edits where Cursor exposes those hook
  payloads.
- **Collector export:** convert OTLP logs, traces, metrics, and resource
  attributes into Beacon endpoint JSONL with the `beaconjson` collector exporter.
- **Local JSONL output:** write Wazuh-compatible JSONL to a local runtime log.
- **Local dashboard:** run a local-only dashboard for inspecting runtime
  inventory, summaries, timelines, filters, and event details from the JSONL
  log.
- **Wazuh content:** generate Wazuh localfile/rule content for the Beacon event
  schema.

## Privacy And Retention

Beacon records metadata by default. Content retention is configurable with:

- `metadata`: default; no prompt text, raw attributes, command output, or raw
  diffs.
- `redacted`: include configured content fields after local redaction and size
  limits.
- `full`: include configured content fields in local/customer-controlled logs,
  still subject to event size limits.

## What Beacon Does Not Do

Beacon does not currently provide kernel/process monitoring, shell history
collection, cloud audit ingestion, browser/SaaS telemetry, credential-use
attribution, MCP configuration inventory, or direct Datadog/Splunk/Elastic/etc.
exporters.

## Documentation

See the [Beacon CLI documentation](https://docs.asymptotelabs.ai/cli) for setup
guides and command details.

## Quick Start

### Install With Homebrew

Install the released Beacon CLI from the public Homebrew tap:

```bash
brew tap asymptote-labs/tap
brew install beacon
beacon version
```

### Build From Source

```bash
cd cli/beacon
make build
```

### Install In User Mode

```bash
beacon endpoint install --user
beacon endpoint status --user
```

### Set Content Retention

```bash
beacon endpoint install --user --content-retention metadata
```

### Configure Wazuh Output

```bash
beacon endpoint wazuh print-config --user
beacon endpoint wazuh validate --user
```

### Run The macOS Smoke Test

```bash
sh packaging/macos/smoke-endpoint.sh
```

## Optional Integrations

### Cursor Hooks

```bash
beacon endpoint hooks install --harness cursor --user
```

### Claude Cowork

Claude Cowork OpenTelemetry export is configured in the Claude admin console and
requires a Team/Enterprise admin.

```bash
beacon endpoint integrations claude-cowork setup --endpoint https://collector.example.com --user --open
beacon endpoint integrations claude-cowork validate --user --since 10m
```

For local testing only, Beacon can create a temporary authenticated ngrok tunnel
to the local OTLP HTTP receiver:

```bash
beacon endpoint integrations claude-cowork setup --ngrok --user --open
```

## Claude Cowork Durable Collector

Claude Cowork exports telemetry from Anthropic's service, so the OTLP endpoint
must be reachable from the public internet. Do not use `127.0.0.1`, a laptop, or
an ngrok URL for production monitoring.

For ongoing use, run a customer-managed HTTPS OpenTelemetry Collector endpoint:

```text
https://otel.example.com
```

Configure Claude Cowork with:

- `OTLP endpoint`: `https://otel.example.com`
- `OTLP protocol`: `HTTP/protobuf`
- `OTLP headers`: `Authorization=Bearer <customer-generated-token>`
- `Resource attributes`: `deployment.environment=prod,service.name=claude-cowork`

Recommended production shape:

- Use a real DNS name with TLS, such as `https://otel.company.com`.
- Require authentication at the public edge, commonly with an
  `Authorization=Bearer ...` header.
- Terminate TLS at a hardened reverse proxy or load balancer, then forward OTLP
  HTTP paths such as `/v1/logs`, `/v1/metrics`, and `/v1/traces` to the
  Collector's local `4318` receiver.
- Treat Cowork telemetry as sensitive. Prompt text, tool parameters, file paths,
  user email addresses, model usage, and errors may be present before Beacon
  redaction/export.
- Use `--ngrok` only for demos, validation, or local development.

## Dashboard

Run the local dashboard:

```bash
beacon endpoint dashboard --user
beacon endpoint dashboard --user --open
```

The dashboard binds to loopback by default and reads the local runtime JSONL log.
It is intended for local inspection, not remote administration.

## Command Reference

Common commands:

```bash
beacon version
beacon endpoint install --user
beacon endpoint status --user
beacon endpoint discover
beacon endpoint dashboard --user --open
beacon endpoint wazuh print-config --user
beacon endpoint wazuh validate --user
beacon endpoint hooks install --harness cursor --user
beacon endpoint integrations claude-cowork setup --endpoint https://collector.example.com --user --open
beacon endpoint integrations claude-cowork validate --user --since 10m
beacon endpoint uninstall --user --keep-logs
```

- `beacon endpoint install`: configure the endpoint agent, Collector service, and Claude/Codex telemetry.
- `beacon endpoint repair`: reapply service/config files and repair telemetry drift.
- `beacon endpoint status`: show Collector, service, harness, and diagnostic status.
- `beacon endpoint discover`: list supported local AI runtimes.
- `beacon endpoint dashboard`: run a localhost-only dashboard over the runtime JSONL log.
- `beacon endpoint hooks`: install, check, or remove hook-based integrations such as Cursor.
- `beacon endpoint integrations claude-cowork`: set up and validate admin-configured Cowork OTLP export.
- `beacon endpoint wazuh`: print/install Wazuh content and write a validation event.
- `beacon endpoint uninstall`: stop services and remove managed endpoint files.

Uninstall while keeping logs:

```bash
beacon endpoint uninstall --user --keep-logs
```

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

## Testing

```bash
cd cli/beacon
go test ./...
go test -race ./internal/endpoint/...

cd ../beacon-hooks
go test ./...

cd ../../collector-builder/exporter/beaconjsonexporter
go test ./...
```
