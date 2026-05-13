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

The dashboard is local-only, binds to loopback, and reads the configured
runtime JSONL log.

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
