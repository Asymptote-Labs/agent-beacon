<p align="center">
  <img src="images/beacon-hero.png" alt="Beacon" width="860">
</p>

<h1 align="center">Asymptote Lab's Beacon</h1>

<p align="center">
  <strong>Unified endpoint telemetry for AI agents.</strong>
</p>

<p align="center">
  <a href="https://docs.asymptotelabs.ai/cli">Docs</a>
  ·
  <a href="https://docs.asymptotelabs.ai/cli/installation">Install</a>
  ·
  <a href="#for-security-and-it-teams">For Security and IT Teams</a>
  ·
  <a href="https://docs.asymptotelabs.ai/cli/mdm-deployment">MDM Deployment</a>
  ·
  <a href="#command-reference">Commands</a>
</p>

AI agents now take action directly on developer endpoints, but security teams
often have little unified visibility into prompts, tools, approvals, commands,
and file changes across local agent harnesses.

Beacon runs locally on the endpoint, captures supported activity from Claude
Code, Codex CLI, Claude Cowork, and Cursor, and normalizes it into
Wazuh-compatible JSONL for existing localfile/Wazuh or customer-managed
pipelines. Splunk HEC forwarding is available as an optional collector
destination for teams that want direct SIEM ingestion.

<p align="center">
  <img src="images/beacon-architecture.png" alt="Beacon endpoint architecture" width="860">
</p>

### Why Teams Use Beacon

- **Local-only by default:** no hosted account, remote policy fetch, or external
  network dependency during normal endpoint collection.
- **Built for AI runtimes:** captures supported Claude Code, Codex CLI, Cursor,
  and Claude Cowork activity where those runtimes expose telemetry.
- **Security-team friendly:** ships macOS package assets for Jamf Pro and Fleet
  deployment, validation, repair, and inventory.
- **SIEM-ready output:** writes Wazuh-compatible JSONL for local or
  customer-managed forwarding, with optional Splunk HEC export through the
  bundled collector.

## Table Of Contents

- [For Security and IT Teams](#for-security-and-it-teams)
- [Product Vision](#product-vision)
- [What Beacon Does](#what-beacon-does)
- [Privacy And Retention](#privacy-and-retention)
- [What Beacon Does Not Do](#what-beacon-does-not-do)
- [Documentation](#documentation)
- [Quick Start](#quick-start)
- [Optional Integrations](#optional-integrations)
- [Claude Cowork Durable Collector](#claude-cowork-durable-collector)
- [Dashboard](#dashboard)
- [Command Reference](#command-reference)
- [Repository Layout](#repository-layout)
- [Release Readiness](#release-readiness)
- [Testing](#testing)

## For Security and IT Teams

Beacon can be deployed with macOS MDM using a signed and notarized `.pkg`. The
package installs Beacon under `/opt/beacon`, creates system endpoint
configuration, loads the local collector LaunchDaemon, and writes runtime JSONL
to `/var/log/beacon-agent/runtime.jsonl`.

Deployment guidance:

- [For Security & IT Teams](https://docs.asymptotelabs.ai/cli/security-it-teams)
  explains the operational model and security workflow.
- [MDM Deployment](https://docs.asymptotelabs.ai/cli/mdm-deployment) covers the
  managed macOS package layout, rollout model, environment variables, and
  rollback behavior.
- [Jamf](https://docs.asymptotelabs.ai/cli/jamf) and
  [Fleet](https://docs.asymptotelabs.ai/cli/fleet) cover MDM-specific inventory,
  repair, validation, and script parameters.
- [`packaging/macos/README.md`](packaging/macos/README.md) documents the
  repository-local packaging assets used to build and test the `.pkg`.

Validate a managed Mac with:

```bash
sudo /opt/beacon/bin/beacon endpoint status --json
sudo /opt/beacon/bin/beacon endpoint wazuh validate
sudo launchctl print system/com.beacon.endpoint.collector
```

Cursor telemetry is deployed separately in the logged-in user's context; do not
configure Cursor hooks from the base system package install.

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
  attributes into Beacon endpoint JSONL with the `beaconjson` collector exporter,
  and optionally forward the same OTLP signals to Splunk HEC.
- **Local JSONL output:** write Wazuh-compatible JSONL to a local runtime log.
- **Local dashboard:** run a local-only dashboard for inspecting runtime
  inventory, summaries, timelines, filters, and event details from the JSONL
  log.
- **Wazuh content:** generate Wazuh localfile/rule content for the Beacon event
  schema.
- **Splunk HEC forwarding:** send logs, traces, and metrics to a
  customer-managed Splunk HTTP Event Collector while preserving the local JSONL
  audit log.

See the
[architecture](https://docs.asymptotelabs.ai/cli/architecture),
[supported surfaces](https://docs.asymptotelabs.ai/cli/supported-surfaces), and
[endpoint event schema](https://docs.asymptotelabs.ai/cli/event-schema) docs for
the detailed collection model and normalized JSONL fields.

## Privacy And Retention

Beacon records configured content fields by default. Content retention is configurable with:

- `metadata`: no prompt text, raw attributes, command output, or raw diffs.
- `redacted`: include configured content fields after local redaction and size
  limits.
- `full`: default; include configured content fields in local/customer-controlled
  logs, still subject to event size limits.

The [endpoint agent docs](https://docs.asymptotelabs.ai/cli/endpoint) describe
how retention settings are applied during install and repair.

## What Beacon Does Not Do

Beacon does not currently provide kernel/process monitoring, shell history
collection, cloud audit ingestion, browser/SaaS telemetry, credential-use
attribution, MCP configuration inventory, or direct Datadog/Elastic/etc.
exporters.

For current support boundaries, see
[Supported Surfaces](https://docs.asymptotelabs.ai/cli/supported-surfaces).

## Documentation

Start with the [Beacon CLI documentation](https://docs.asymptotelabs.ai/cli).
The most common next pages are:

- [Installation](https://docs.asymptotelabs.ai/cli/installation) for local CLI
  setup.
- [Endpoint Agent](https://docs.asymptotelabs.ai/cli/endpoint) for install,
  status, repair, and uninstall workflows.
- [MDM Deployment](https://docs.asymptotelabs.ai/cli/mdm-deployment),
  [Jamf](https://docs.asymptotelabs.ai/cli/jamf), and
  [Fleet](https://docs.asymptotelabs.ai/cli/fleet) for managed macOS rollout.
- [Wazuh](https://docs.asymptotelabs.ai/cli/wazuh) and
  [SIEM Forwarding](https://docs.asymptotelabs.ai/cli/siem-forwarding) for
  forwarding local JSONL events.
- [Dashboard](https://docs.asymptotelabs.ai/cli/dashboard),
  [Cursor Hooks](https://docs.asymptotelabs.ai/cli/hooks), and
  [Claude Cowork](https://docs.asymptotelabs.ai/cli/claude-cowork) for optional
  runtime workflows.

## Quick Start

For the full local setup guide, see
[Installation](https://docs.asymptotelabs.ai/cli/installation).

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

### Install Locally

```bash
beacon endpoint install
beacon endpoint status
```

The normal CLI flow uses per-user endpoint paths by default. Cursor hooks,
Claude Code OTLP, and Codex OTLP all write to the same user runtime log:
`~/.beacon/endpoint/logs/runtime.jsonl`. Use `--system` only for root-managed
package or MDM deployments.

Command details: [`beacon endpoint install`](https://docs.asymptotelabs.ai/cli/endpoint-install)
and [`beacon endpoint status`](https://docs.asymptotelabs.ai/cli/endpoint-status).

### Set Content Retention

```bash
beacon endpoint install --content-retention metadata
```

Omit the flag to use the default `full` retention mode.

### Configure Wazuh Output

```bash
beacon endpoint wazuh print-config
beacon endpoint wazuh validate
```

See [Wazuh](https://docs.asymptotelabs.ai/cli/wazuh) and
[SIEM Forwarding](https://docs.asymptotelabs.ai/cli/siem-forwarding) for
production forwarding guidance.

### Configure Splunk HEC Forwarding

```bash
beacon endpoint install \
  --splunk-hec-endpoint https://splunk.example:8088/services/collector \
  --splunk-hec-token "$SPLUNK_HEC_TOKEN" \
  --splunk-index beacon
```

Splunk forwarding is additive: Beacon still writes the local runtime JSONL log,
and the bundled collector also sends logs, traces, and metrics to the configured
HEC endpoint.

### Run The macOS Smoke Test

```bash
sh packaging/macos/smoke-endpoint.sh
```

## Optional Integrations

### Cursor Hooks

```bash
beacon endpoint hooks install --harness cursor
```

See [Cursor Hooks](https://docs.asymptotelabs.ai/cli/hooks) for install, status,
and uninstall guidance.

### Claude Cowork

Claude Cowork OpenTelemetry export is configured in the Claude admin console and
requires a Team/Enterprise admin.

```bash
beacon endpoint integrations claude-cowork setup --endpoint https://collector.example.com --open
beacon endpoint integrations claude-cowork validate --since 10m
```

For local testing only, Beacon can create a temporary authenticated ngrok tunnel
to the local OTLP HTTP receiver:

```bash
beacon endpoint integrations claude-cowork setup --ngrok --open
```

See [Claude Cowork](https://docs.asymptotelabs.ai/cli/claude-cowork) for
production setup and validation details.

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

The
[`claude-cowork setup`](https://docs.asymptotelabs.ai/cli/claude-cowork-setup)
and
[`claude-cowork validate`](https://docs.asymptotelabs.ai/cli/claude-cowork-validate)
command docs include the current CLI flags.

## Dashboard

Run the local dashboard:

```bash
beacon endpoint dashboard
beacon endpoint dashboard --open
```

The dashboard binds to loopback by default and reads the local runtime JSONL log.
It is intended for local inspection, not remote administration. In the default
CLI setup it reads the same user log used by hook and OTLP telemetry.

See [Dashboard](https://docs.asymptotelabs.ai/cli/dashboard) for usage and
[`beacon endpoint dashboard`](https://docs.asymptotelabs.ai/cli/endpoint-dashboard)
for command flags.

## Command Reference

Common commands:

```bash
beacon version
beacon endpoint install
beacon endpoint status
beacon endpoint discover
beacon endpoint dashboard --open
beacon endpoint wazuh print-config
beacon endpoint wazuh validate
beacon endpoint hooks install --harness cursor
beacon endpoint integrations claude-cowork setup --endpoint https://collector.example.com --open
beacon endpoint integrations claude-cowork validate --since 10m
beacon endpoint uninstall --keep-logs
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

Full command docs are available under
[Endpoint Agent](https://docs.asymptotelabs.ai/cli/endpoint), including
[install](https://docs.asymptotelabs.ai/cli/endpoint-install),
[repair](https://docs.asymptotelabs.ai/cli/endpoint-repair),
[status](https://docs.asymptotelabs.ai/cli/endpoint-status),
[discover](https://docs.asymptotelabs.ai/cli/endpoint-discover),
[dashboard](https://docs.asymptotelabs.ai/cli/endpoint-dashboard),
[hooks](https://docs.asymptotelabs.ai/cli/hooks), and
[uninstall](https://docs.asymptotelabs.ai/cli/endpoint-uninstall).

Uninstall while keeping logs:

```bash
beacon endpoint uninstall --keep-logs
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
package should apply explicit system endpoint settings, for example:

```bash
beacon endpoint install --system --harness claude,codex --content-retention full
```

See [MDM Deployment](https://docs.asymptotelabs.ai/cli/mdm-deployment) for the
managed package layout and rollout model.

Before publishing a release, verify the build from a clean checkout and clean
macOS host or VM:

- `beacon version` reports the expected version, commit, and build date.
- `beacon endpoint install --no-start` succeeds without developer
  tooling.
- `beacon endpoint status` reports config, collector, service, harness,
  diagnostic, and runtime log paths.
- `beacon endpoint wazuh validate` writes a valid Beacon JSONL event.
- `beacon endpoint dashboard` starts on `127.0.0.1`, serves a read-only
  dashboard, and can search local telemetry without external network
  dependencies.
- `beacon endpoint uninstall` removes managed service and config files.
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
