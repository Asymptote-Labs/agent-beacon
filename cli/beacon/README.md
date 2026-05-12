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
./beacon endpoint uninstall --user --keep-logs
```

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
