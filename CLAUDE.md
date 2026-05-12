# CLAUDE.md

Guidance for Claude Code and other coding agents working in this repository.

## Project Scope

Beacon Endpoint Agent is a local-only endpoint telemetry agent for AI runtimes. The shipping code paths are:

- `cli/beacon`: public `beacon` CLI and endpoint runtime.
- `cli/beacon-hooks`: hook adapter invoked by Cursor and other supported runtimes.
- `collector-builder`: OpenTelemetry Collector distribution scaffold.
- `packaging`: macOS packaging and deployment assets.

Do not recreate or depend on removed `asymptote` mirror trees. Keep new work focused on the Beacon paths above.

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

Build the CLI:

```bash
cd cli/beacon
make build
```

## Implementation Notes

- Prefer deterministic tests that use `t.TempDir()`, `t.Setenv("HOME", ...)`, fake binaries, and free local ports.
- Avoid tests that require root, real `launchctl` service changes, Wazuh, a live collector, or external network access.
- For macOS-only behavior, gate tests with `runtime.GOOS == "darwin"` or assert the non-Darwin contract explicitly.
- Keep endpoint event schema fields stable: `vendor`, `product`, `schema_version`, required event fields, and Wazuh-compatible JSONL output are release contracts.
- Preserve the local-only product posture. The public Beacon build should not require a hosted account or remote policy fetch.

## CI Expectations

CI runs:

- `go test ./...` in `cli/beacon`.
- `go test -race ./internal/endpoint/...` in `cli/beacon`.
- `go test ./...` in `cli/beacon-hooks`.
- CLI help smoke checks for the public command tree.
- macOS packaging script validation via `packaging/macos/test-endpoint-scripts.sh`.
