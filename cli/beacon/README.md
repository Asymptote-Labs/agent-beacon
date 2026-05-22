# beacon

Public CLI for Beacon Endpoint Agent.

## Build

```bash
make build
```

## Common Commands

```bash
./beacon endpoint install
./beacon endpoint status --json
./beacon endpoint discover --json
./beacon endpoint repair
./beacon endpoint dashboard
./beacon endpoint uninstall --keep-logs
```

Endpoint commands use per-user paths by default so hook and OTLP telemetry share
`~/.beacon/endpoint/logs/runtime.jsonl`. Use `--system` for root-managed
deployment paths.

Add optional Splunk HEC forwarding during install or repair:

```bash
./beacon endpoint install \
  --splunk-hec-endpoint https://splunk.example:8088/services/collector \
  --splunk-hec-token "$SPLUNK_HEC_TOKEN" \
  --splunk-index beacon
```

The local JSONL runtime log remains enabled when Splunk forwarding is
configured.

## Dashboard

```bash
./beacon endpoint dashboard
./beacon endpoint dashboard --addr 127.0.0.1:8765
./beacon endpoint dashboard --open
```

The dashboard reads the configured runtime JSONL log and serves a local,
read-only view on loopback. It has no external network dependency during normal
use.

Use the search bar to find events by action, command, file path, MCP tool,
approval decision, repository, session, or message. Quick filters surface
high-severity events, failures, approvals, MCP activity, file changes, and events
that may need review.

## Wazuh

```bash
./beacon endpoint wazuh print-config
./beacon endpoint wazuh install-pack --output ./beacon-wazuh
./beacon endpoint wazuh validate
```

## Sumo Logic

```bash
./beacon endpoint sumo print-config
./beacon endpoint sumo install-pack --output ./beacon-sumo-pack
./beacon endpoint sumo validate
```

The Sumo pack keeps Beacon as a local JSONL producer and documents forwarding
`runtime.jsonl` into a customer-managed Sumo Hosted Collector HTTP Logs &
Metrics Source. Use a tailing forwarder for production so offsets are
checkpointed and the whole file is not repeatedly uploaded.

## Optional Integrations

```bash
./beacon endpoint hooks install --harness cursor
./beacon endpoint hooks status --harness cursor

./beacon endpoint hooks install --harness opencode
./beacon endpoint hooks status --harness opencode

./beacon endpoint hooks install --harness grok
./beacon endpoint hooks status --harness grok

./beacon endpoint hooks install --harness devin --level project
./beacon endpoint hooks status --harness devin --level project

./beacon endpoint integrations claude-cowork setup --endpoint https://collector.example.com --open
./beacon endpoint integrations claude-cowork setup --ngrok --open
./beacon endpoint integrations claude-cowork validate --since 10m

./beacon endpoint integrations openclaw print-config
./beacon endpoint integrations openclaw status
./beacon endpoint integrations openclaw validate --since 10m
```

The opencode integration installs Beacon's owned local plugin at
`~/.config/opencode/plugins/beacon.ts`. The plugin is a thin adapter that sends
raw opencode hook payloads to Beacon's Go hook binary; Beacon handles
normalization, retention, redaction, and JSONL output locally. For local
troubleshooting, set `BEACON_OPENCODE_DEBUG=1` in the environment that launches
opencode to emit best-effort plugin debug logs.

The Grok Build integration writes Beacon's owned local hook file at
`~/.grok/hooks/beacon-endpoint.json` for user-level installs or `.grok/hooks/beacon-endpoint.json`
for project-level installs. Project hooks require trusting the project in Grok
with `/hooks-trust` before they execute.

The Devin integration writes Claude-compatible command hooks for Devin for
Terminal. Project-level installs use `.devin/hooks.v1.json`; user-level installs
use `~/.config/devin/config.json` under the `hooks` key. The hooks invoke
Beacon's local Go hook binary and write normalized prompt, tool, command, file,
approval, and session events to the configured runtime JSONL log.

Claude Cowork monitoring is configured in the Claude admin console at
`https://claude.ai/admin-settings/cowork`. The OTLP endpoint must be reachable
by Claude Cowork, so use a durable public HTTPS Collector endpoint for ongoing
monitoring. The `--ngrok` mode is for short-lived local testing and prints an
authenticated tunnel URL plus the matching `Authorization` header.

OpenClaw Gateway monitoring is configured in OpenClaw's local Gateway config
with the `diagnostics-otel` plugin enabled. Beacon prints a local OTLP/HTTP
configuration that points OpenClaw at the endpoint collector. OpenClaw does not
export raw prompt, response, tool, or system-prompt content unless
`diagnostics.otel.captureContent.*` is explicitly enabled.

## Test

```bash
go test ./...
go test -race ./internal/endpoint/...
```
