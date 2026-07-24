# AGENTS.md

This repository also has detailed contributor and release guidance in `CLAUDE.md`,
`README.md`, and `CONTRIBUTING.md`. Prefer those for command reference; this file
only adds environment/runtime context that is not obvious from them.

## Cursor Cloud specific instructions

Beacon is a Go + TypeScript monorepo. There is **no root `go.mod`**; each Go
component is its own module wired to `pkg/asymptoteobserve` via local `replace`
directives. The shipping surfaces are `cli/beacon`, `cli/beacon-hooks`,
`pkg/asymptoteobserve`, `collector-builder/exporter/beaconjsonexporter`, and the
TypeScript SDK in `packages/asymptote-sdk-js`.

### Toolchain / versions (non-obvious)

- Go modules require **`go 1.24`** (`cli/beacon`, `cli/beacon-hooks`,
  `pkg/asymptoteobserve`) and **`go 1.25.0`** for the collector exporter
  (`collector-builder/exporter/beaconjsonexporter/go.mod`).
- `GOTOOLCHAIN=auto` (the default here) lets Go auto-download the required
  toolchain even if the base image ships an older `go`, so `go build`/`go test`
  work without a manual upgrade — the first invocation just pays a one-time
  toolchain download. A Go `>=1.24` toolchain is installed at `/usr/local/go`.
- Node `>=20` is required for the SDK; CI tests on Node 20/22/24.

### Build / run / test (see `CLAUDE.md` for the full list)

- Build the CLI: `cd cli/beacon && make build`. This first builds
  `cli/beacon-hooks` and copies it to `internal/embedded/hooks.bin` (a
  **gitignored, build-time artifact**). A placeholder is created for fresh
  clones, but a real binary is required for the full `beacon` build and for
  `InstallFactory`-dependent tests, so run `make build-hooks-current` (or
  `make build`) before `go test ./...` in `cli/beacon`.
- Run the local dashboard (a read-only loopback web app, default
  `127.0.0.1:8765`): `cd cli/beacon && go run . endpoint dashboard`. Point it at
  a specific log with `--log-path <file>`; it serves the UI plus `/api/events`
  and `/api/tokens` from that JSONL.
- Write a synthetic event / offline threat scan for a quick end-to-end check:
  `go run . endpoint test-event --log-path <file>` then
  `go run . scan --log-path <file>`.
- SDK: `cd packages/asymptote-sdk-js && npm ci` then `npm test`, `npm run check`,
  `npm run build`. `npm ci` is **not** run by `.cursor/install.sh`, so install
  SDK deps yourself before working on the SDK.
- Lint (`golangci-lint`, via `make lint`) is a local convenience target and is
  **not** a CI gate; the repo ships no `.golangci.*` config, so it reports
  default-linter findings (several pre-existing). `go vet ./...` is clean.

### Known Linux limitations (non-obvious)

- CI runs the Go test job on **macOS** (`.github/workflows/ci.yml` `go-test` uses
  `macos-latest`). Two `cli/beacon` tests are hardcoded for macOS paths/behavior
  and FAIL on Linux even on a clean checkout:
  `TestConfigureVSCodePreservesSettingsAndDisablesContentCaptureByDefault`
  (`internal/endpoint/harness`, expects `~/Library/Application Support/...`) and
  `TestRollbackReportsCollectorRestartFailure` (`internal/endpoint/selfupdate`).
  All other Go tests (including `go test -race ./internal/endpoint/...`) pass on
  Linux. Treat these two as expected environment failures when developing on
  Linux, not regressions.
- macOS packaging scripts under `packaging/macos/` are macOS-only and do not run
  meaningfully on Linux.
