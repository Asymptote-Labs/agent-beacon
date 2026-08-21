# Agent instructions for agent-beacon

Beacon is a local-only endpoint telemetry agent for AI runtimes. Guidance for working in this
repository lives in `CONTRIBUTING.md` (layout, workflow, validation) and `CLAUDE.md` (project
scope, product posture, release process). Read those first for ordinary changes.

## Verifying a change end to end

This repository ships a sandbox that runs a **real Claude Code session** against the Beacon under
test and checks whether Beacon captured what happened. Use it when asked to verify that a change
actually works, not just that it compiles and unit-tests pass.

**Read `beacon-sandbox/AGENTS.md` before running any verification.** It is the operating manual:
prerequisites, which scenario to pick for which change, how to read a verdict, and the failure
modes that look like Beacon bugs but are not.

Start with the prerequisite check, which is free and needs no sandbox:

```bash
cd beacon-sandbox && go run ./cmd/beacon-sandbox doctor
```

Two things to know before you run anything: a scenario costs real money (roughly $0.06 of sandbox
plus a few cents of API), so tell the user before spending it; and the sandbox is Linux-only, so it
cannot verify macOS-specific behavior.

## Running the standard checks

Unit and integration tests for the shipping code, per `CONTRIBUTING.md`:

```bash
cd cli/beacon && go test ./...
cd cli/beacon && go test -race ./internal/endpoint/...
cd cli/beacon-hooks && go test ./...
cd collector-builder/exporter/beaconjsonexporter && go test ./...
cd pkg/asymptoteobserve && go test ./...
sh packaging/macos/test-endpoint-scripts.sh
sh packaging/macos/smoke-endpoint.sh
```

Each directory above is a separate Go module; run Go commands from inside the module you are
changing.
