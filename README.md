# Beacon Endpoint Agent

Local endpoint telemetry for AI agent runtimes.

Beacon Endpoint Agent configures local telemetry for tools like Claude Code,
Codex CLI, Claude Cowork, and Cursor, then writes Wazuh-compatible JSONL logs.
It runs local-only and does not require a Beacon account.

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

Print Wazuh config and validate event output:

```bash
./beacon endpoint wazuh print-config --user
./beacon endpoint wazuh validate --user
```

Optional integrations:

```bash
./beacon endpoint hooks install --harness cursor --user
./beacon endpoint integrations claude-cowork print-config --user
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
- `beacon endpoint hooks`: install, check, or remove hook-based integrations such as Cursor.
- `beacon endpoint integrations claude-cowork`: print setup and validate admin-configured Cowork OTLP export.
- `beacon endpoint wazuh`: print/install Wazuh content and write a validation event.
- `beacon endpoint uninstall`: stop services and remove managed endpoint files.

## Repository Layout

- `cli/beacon`: public `beacon` CLI.
- `cli/beacon-hooks`: hook adapter invoked by supported agent runtimes.
- `collector-builder`: custom OpenTelemetry Collector distribution scaffold.
- `packaging`: macOS packaging and MDM deployment assets.

## Test

```bash
cd cli/beacon
go test ./...
go test -race ./internal/endpoint/...

cd ../beacon-hooks
go test ./...
```
