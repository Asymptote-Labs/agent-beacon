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

## Elastic

Generate Filebeat, standalone Elastic Agent, Elasticsearch, and Kibana content
for the configured Beacon runtime log:

```bash
./beacon endpoint elastic print-config
./beacon endpoint elastic install-pack --output ./beacon-elastic-pack
```

On macOS with Docker Desktop, start a loopback-only Elasticsearch, Kibana, and
Filebeat stack for local validation:

```bash
./beacon endpoint elastic up --pack-dir ./beacon-elastic-pack
./beacon endpoint elastic down --pack-dir ./beacon-elastic-pack
```

For Elastic Cloud or a self-managed cluster, install the JSON assets from the
pack and run Filebeat or standalone Elastic Agent with `ES_HOSTS` plus one
authentication method, such as `ES_API_KEY`.

## Optional Integrations

```bash
./beacon endpoint hooks install --harness cursor
./beacon endpoint hooks status --harness cursor

./beacon endpoint integrations claude-cowork setup --endpoint https://collector.example.com --open
./beacon endpoint integrations claude-cowork setup --ngrok --open
./beacon endpoint integrations claude-cowork validate --since 10m
```

Claude Cowork monitoring is configured in the Claude admin console at
`https://claude.ai/admin-settings/cowork`. The OTLP endpoint must be reachable
by Claude Cowork, so use a durable public HTTPS Collector endpoint for ongoing
monitoring. The `--ngrok` mode is for short-lived local testing and prints an
authenticated tunnel URL plus the matching `Authorization` header.

## Test

```bash
go test ./...
go test -race ./internal/endpoint/...
```
