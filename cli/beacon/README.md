# beacon

Public CLI for Beacon Endpoint Agent.

## Build

```bash
make build
```

## Common Commands

```bash
./beacon endpoint install --user
./beacon endpoint status --json
./beacon endpoint discover --json
./beacon endpoint repair --user
./beacon endpoint dashboard --user
./beacon endpoint uninstall --user --keep-logs
```

## Dashboard

```bash
./beacon endpoint dashboard --user
./beacon endpoint dashboard --user --addr 127.0.0.1:8765
./beacon endpoint dashboard --user --open
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
./beacon endpoint wazuh print-config --user
./beacon endpoint wazuh install-pack --output ./beacon-wazuh --user
./beacon endpoint wazuh validate --user
```

## Optional Integrations

```bash
./beacon endpoint hooks install --harness cursor --user
./beacon endpoint hooks status --harness cursor --user

./beacon endpoint integrations claude-cowork print-config --user
./beacon endpoint integrations claude-cowork validate --user
```

## Test

```bash
go test ./...
go test -race ./internal/endpoint/...
```
