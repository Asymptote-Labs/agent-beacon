# CLAUDE.md

Guidance for Claude Code and other coding agents working in this repository.

## Project Scope

Beacon Endpoint Agent is a local-only endpoint telemetry agent for AI runtimes. The shipping code paths are:

- `cli/beacon`: public `beacon` CLI and endpoint runtime.
- `cli/beacon-hooks`: hook adapter invoked by Cursor and other supported runtimes.
- `collector-builder`: OpenTelemetry Collector distribution and Beacon JSONL exporter.
- `packages/asymptote-sdk-js`: TypeScript SDK for cloud agent telemetry that exports Beacon-compatible OpenTelemetry spans.
- `packaging`: macOS packaging and deployment assets.

Do not recreate or depend on removed `asymptote` mirror trees. Keep new work focused on the Beacon paths above.

## Product Posture

- Beacon is visibility-first endpoint telemetry for local AI agent runtimes, not a hosted policy service or general endpoint protection product.
- Preserve the local-only product posture. The public Beacon build should not require a hosted account, remote policy fetch, hosted dashboard, or external network dependency during normal hook execution.
- Do not add dependency vulnerability scanning, OSV/GHSA lookups, package remediation, or other vulnerability-enforcement flows to the public hook path.
- Do not add broad runtime enforcement unless explicitly requested. Current control behavior is limited to hook-native approvals/denials exposed by supported agent runtimes.
- Keep direct destination support scoped to local JSONL/Wazuh unless explicitly requested. Elastic support is a file-tailing pack over local JSONL; Beacon itself must not store Elastic credentials or require a hosted Elastic dependency.
- Beacon writes retained prompt text, command output, raw tool inputs, raw OTLP attributes, and raw diffs to local or customer-controlled logs, subject to local redaction and size limits where supported.
- The Asymptote Observe TypeScript SDK may default to the hosted `/v1/observe` endpoint as an opt-in cloud SDK contract. Beacon endpoint execution stays local-only/no-network.

## Telemetry Scope

Supported runtime surfaces today:

- Claude Code and Codex CLI telemetry configuration through local OpenTelemetry settings.
- Cursor hook telemetry for sessions, prompt submission, tool use, command execution, MCP-like tool activity, approval decisions, and file edits where hook payloads expose those fields.
- Claude Cowork admin-configured OpenTelemetry setup guidance and local validation.
- `beaconjson` OpenTelemetry Collector exporter that converts OTLP logs, traces, metrics, and resource attributes into Beacon endpoint JSONL.
- Asymptote Observe TypeScript SDK instrumentation for cloud applications, starting from OpenTelemetry/OpenLLMetry patterns and `observe()` wrappers.
- Elasticsearch/Filebeat content pack generation for forwarding local Beacon JSONL into customer-managed Elastic deployments or the bundled loopback-only development stack.
- A local-only dashboard served by `beacon endpoint dashboard`, bound to loopback by default and backed by the runtime JSONL log.
- Token usage and runtime-reported cost capture across spans, logs, and metric datapoints, normalized into `gen_ai.usage`, with attribution rollups served by the dashboard token view (`/api/tokens`) and the `beacon token-usage` report command for local and CI logs.
- Local threat detection via `beacon scan`, which runs the open Threat Rules format (`spec/threat-rules`; CEL match conditions over the endpoint event schema, with embedded conformance fixtures) over the runtime JSONL. The engine (`pkg/asymptoteobserve/threatrules`) ships in the binary; the rule corpus is external data loaded from a local store (`~/.beacon/endpoint/rules`) managed by `beacon rules`, so the corpus can grow without enlarging the binary. `scan` is read-only and offline; only the explicit, user-initiated `beacon rules pull <url>` reaches the network. A small frozen baseline is embedded so `scan` works before any rules are installed.
- An optional, off-by-default policy seam in the hook path (`cli/beacon-hooks/internal/policy`) governed by the stable contract in `pkg/asymptoteobserve/policycontract`. When `BEACON_POLICY_PROVIDER` names an executable, the `pre-tool` and `permission-request` hooks send it a JSON request describing the imminent tool call and honor an allow/deny response, returning the runtime's native deny shape and `policy.enforcement=enforce` telemetry on a deny. The seam is inert when unset and fail-open on any error (timeout, non-zero exit, malformed output), so the open build ships no enforcement of its own; all audit/enforce decision logic lives in the external provider. `rules/agent-control/agent-permission-bypass-spawn` is the open detect twin that flags the same pattern in `beacon scan`.

Current non-goals unless explicitly requested:

- Kernel/process monitoring, EDR replacement, shell history scraping, cloud audit ingestion, browser/SaaS telemetry, credential-use attribution, and MCP configuration inventory.
- Direct hosted integrations for Datadog, Snowflake, Chronicle, Panther, or other SIEM destinations beyond explicitly supported local/customer-managed forwarding patterns.
- Dependency vulnerability scanning or package security remediation.

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

Run the macOS endpoint smoke test:

```bash
sh packaging/macos/smoke-endpoint.sh
```

Build the CLI:

```bash
cd cli/beacon
make build
```

Run Collector exporter tests:

```bash
cd collector-builder/exporter/beaconjsonexporter
go test ./...
```

Run the observe SDK and threat-rules conformance tests:

```bash
cd pkg/asymptoteobserve
go test ./...
```

Run TypeScript SDK checks:

```bash
cd packages/asymptote-sdk-js
npm test
npm run check
npm run build
npm run pack:dry-run
```

Run the local dashboard during manual testing:

```bash
cd cli/beacon
go run . endpoint dashboard
```

Run local threat detection and manage rules during manual testing:

```bash
cd cli/beacon
go run . scan                  # run active rules over the runtime log
go run . rules list            # show active rules (baseline or store)
go run . rules lint ../../rules  # validate + run the repo rule pack's fixtures
```

When adding event fields a rule can match on, regenerate the field reference:
`beacon rules fields --markdown > spec/threat-rules/FIELDS.md` (a conformance test fails if
it drifts).

## Release Deployments

Homebrew releases are published by GoReleaser from `cli/beacon/.goreleaser.yaml`.
Prefer a CI-based release workflow triggered by an annotated version tag. Use a
local GoReleaser publish only as a fallback when CI release automation is not
available or the maintainer explicitly asks for a local release.

A pushed `v*` tag runs the full release end-to-end via
`.github/workflows/release.yml` (no local publish steps required), with two jobs
in the protected `release` GitHub Environment:

1. `goreleaser` (ubuntu) builds the collector dists, publishes the GitHub release
   + changelog and tarballs, and updates the Homebrew tap.
2. `package` (macOS, `needs: goreleaser`) builds the Apple Silicon
   `beacon`/`beacon-hooks` package payload plus arch-matched collector and
   Vector binaries for `darwin_arm64`, then signs, notarizes, staples, and
   uploads the `.pkg`, `.sha256`, and `update-manifest.json`. GoReleaser still
   publishes multi-arch CLI tarballs; the signed endpoint `.pkg` is arm64-only
   so it can carry a current Vector release.

Required `release`-environment secrets: `HOMEBREW_TAP_TOKEN`,
`DEVELOPER_ID_APP_CERT_P12`, `DEVELOPER_ID_APP_CERT_PASSWORD`,
`DEVELOPER_ID_INSTALLER_CERT_P12`, `DEVELOPER_ID_INSTALLER_CERT_PASSWORD`,
`NOTARY_API_KEY_P8`, `NOTARY_API_KEY_ID`, `NOTARY_API_ISSUER`; plus Variables
`DEVELOPER_ID_APP_IDENTITY`, `DEVELOPER_ID_INSTALLER_IDENTITY`, `APPLE_TEAM_ID`.
Keep one-time Apple-side onboarding steps out of the repo and share them with
the release maintainer directly. Never use `pull_request_target` or build
untrusted PR code in the signing job.

Use the next semver tag requested by the maintainer, usually the next `v0.0.x`
tag unless they explicitly decide Beacon is ready for `v1.0.0`.

Before tagging:

```bash
git fetch --tags origin
git status -sb --untracked-files=all
git tag --sort=-v:refname | sed -n '1,8p'
git log --oneline <previous-tag>..HEAD
gh release view <new-tag> --json tagName,url,isDraft,isPrerelease 2>/dev/null || true
git ls-remote --tags origin "refs/tags/<new-tag>"
```

Run the release gates before publishing:

```bash
cd cli/beacon && go test ./...
cd ../beacon-hooks && go test ./...
cd ../../collector-builder/exporter/beaconjsonexporter && go test ./...
cd ../../../pkg/asymptoteobserve && go test ./...
cd ../../packages/asymptote-sdk-js && npm test && npm run check && npm run build && npm run pack:dry-run
cd ../..
sh packaging/macos/test-endpoint-scripts.sh
```

When building macOS `.pkg` artifacts, always include the Vector binary by
setting `BEACON_VECTOR_BIN` (or `VECTOR_BIN`) to the release Vector executable
before running `packaging/macos/build-pkg.sh` or
`packaging/macos/build-signed-notarized-pkg.sh`. The package should install
`/opt/beacon/bin/vector` so packaged Jamf/Fleet forwarder helpers work without a
separate Vector install.

### Preferred CI Release

CI release automation should:

- Trigger only on pushed tags matching `v*`.
- Check out the tagged commit with full history (`fetch-depth: 0`) so GoReleaser
  can compute changelogs from the previous tag.
- Build or restore the collector binaries expected by `.goreleaser.yaml` under
  `collector-builder/dist/beacon-otelcol/<goos>_<goarch>/beacon-otelcol`.
- Run `goreleaser check` before publishing.
- Run `goreleaser release --clean --parallelism 1` from `cli/beacon` because
  the release pre-hook writes target-specific `beacon-hooks` binaries to a
  shared embedded path.
- Provide `GITHUB_TOKEN` for the GitHub release and `HOMEBREW_TAP_TOKEN` with
  write access to `asymptote-labs/homebrew-tap`.

The normal deployment flow is:

```bash
git fetch --tags origin
git status -sb --untracked-files=all
git tag -a <tag> -m "<tag>"
git push origin <tag>
gh run list --workflow release.yml --limit 5
```

After the workflow succeeds, verify the GitHub release assets and the Homebrew
tap:

```bash
gh release view <tag> --json url,tagName,assets --jq '.tagName + " " + .url + " assets=" + (.assets | map(.name) | join(","))'
gh api repos/Asymptote-Labs/homebrew-tap/contents/Formula/beacon.rb --jq '.content' | base64 --decode | sed -n '1,70p'
gh api repos/Asymptote-Labs/homebrew-tap/commits/main --jq '.sha + " " + .commit.message'
```

If the workflow fails after the tag is pushed, do not create a second tag until
the failure is understood. Fix the release workflow or source issue, then rerun
the failed workflow for the same tag when possible. Delete and recreate a pushed
tag only with maintainer approval.

### Local Fallback Release

Do not publish locally from a dirty checkout unless the maintainer explicitly
wants those uncommitted changes in the release archive. If unrelated local
changes are present, create a temporary clean worktree at `HEAD` and copy the
prebuilt collector binaries into it before running GoReleaser:

```bash
rm -rf .tmp/release-<tag>
git worktree add .tmp/release-<tag> HEAD
mkdir -p .tmp/release-<tag>/collector-builder/dist/beacon-otelcol/{darwin_amd64,darwin_arm64,linux_amd64,linux_arm64}
cp collector-builder/dist/beacon-otelcol/darwin_amd64/beacon-otelcol .tmp/release-<tag>/collector-builder/dist/beacon-otelcol/darwin_amd64/beacon-otelcol
cp collector-builder/dist/beacon-otelcol/darwin_arm64/beacon-otelcol .tmp/release-<tag>/collector-builder/dist/beacon-otelcol/darwin_arm64/beacon-otelcol
cp collector-builder/dist/beacon-otelcol/linux_amd64/beacon-otelcol .tmp/release-<tag>/collector-builder/dist/beacon-otelcol/linux_amd64/beacon-otelcol
cp collector-builder/dist/beacon-otelcol/linux_arm64/beacon-otelcol .tmp/release-<tag>/collector-builder/dist/beacon-otelcol/linux_arm64/beacon-otelcol
```

Tag and publish from the clean release checkout. Prefer explicitly exported
tokens; use `gh auth token` only as a local fallback:

```bash
git -C .tmp/release-<tag> tag -a <tag> -m "<tag>"
git -C .tmp/release-<tag> push origin <tag>
cd .tmp/release-<tag>/cli/beacon
goreleaser check
GITHUB_TOKEN="${GITHUB_TOKEN:-$(gh auth token)}" HOMEBREW_TAP_TOKEN="${HOMEBREW_TAP_TOKEN:-$(gh auth token)}" goreleaser release --clean --parallelism 1
```

After GoReleaser succeeds, verify both the GitHub release and the Homebrew tap:

```bash
gh release view <tag> --json url,tagName,assets --jq '.tagName + " " + .url + " assets=" + (.assets | length | tostring)'
gh api repos/Asymptote-Labs/homebrew-tap/contents/Formula/beacon.rb --jq '.content' | base64 --decode | sed -n '1,70p'
gh api repos/Asymptote-Labs/homebrew-tap/commits/main --jq '.sha + " " + .commit.message'
```

Clean up the temporary worktree after verification:

```bash
git worktree remove --force .tmp/release-<tag>
```

### TypeScript SDK Release

Publish `@asymptote/sdk` from GitHub Actions instead of a local shell when
possible so npm provenance remains enabled.

Before tagging:

```bash
cd packages/asymptote-sdk-js
npm test
npm run check
npm run pack:dry-run
```

Publish with an annotated SDK tag that matches `package.json` exactly:

```bash
git tag -a sdk-js-v<version> -m "sdk-js-v<version>"
git push origin sdk-js-v<version>
gh run list --workflow npm-publish-sdk.yml --limit 5
```

The `.github/workflows/npm-publish-sdk.yml` workflow validates that the pushed
`sdk-js-v<version>` tag matches `packages/asymptote-sdk-js/package.json`, reruns
the SDK checks, and publishes to npm with provenance.

Trusted publishing must be configured in npm for this repository/workflow before
the first CI publish. If trusted publishing is unavailable and the maintainer
explicitly requests a local fallback, disable provenance for that one publish
instead of changing the package defaults.

Use these npm trusted publisher values:

- Package: `@asymptote/sdk`
- Publisher: GitHub Actions
- Organization or owner: `asymptote-labs`
- Repository: `agent-beacon`
- Workflow filename: `npm-publish-sdk.yml`
- Environment: leave unset unless the workflow is updated to declare one

## Implementation Notes

- Prefer deterministic tests that use `t.TempDir()`, `t.Setenv("HOME", ...)`, fake binaries, and free local ports.
- Avoid tests that require root, real `launchctl` service changes, Wazuh, a live collector, or external network access.
- For macOS-only behavior, gate tests with `runtime.GOOS == "darwin"` or assert the non-Darwin contract explicitly.
- Keep endpoint event schema fields stable: `vendor`, `product`, `schema_version`, required event fields, and Wazuh-compatible JSONL output are release contracts.
- Preserve optional event fields for agent-native metadata (`session`, `trace`, `tool`, `file`, `command`, `mcp`, `approval`, `content`, `model`, `repository`, and `branch`) without changing existing required field semantics.
- Keep `gen_ai.usage` as the single canonical token-usage representation: normalize all runtime token telemetry (span attributes, metric datapoints, legacy `llm.usage.*` aliases) into it, mirror OTel GenAI semconv JSON names exactly, and never add parallel or per-harness token fields. `gen_ai.usage.cost_usd` carries runtime-reported cost only; do not derive cost from local pricing tables.
- When adding a new signal, include stable identifiers/counts/hashes alongside any retained raw content, and route raw fields through redaction, sanitization, truncation, and event-size controls.
- Keep the dashboard read-only. It should inspect local status and JSONL events but must not mutate endpoint configuration or telemetry.
- Keep the release readiness guidance in `README.md` up to date when install, packaging, collector, or dashboard behavior changes.

## CI Expectations

CI runs:

- `go test ./...` in `cli/beacon`.
- `go test -race ./internal/endpoint/...` in `cli/beacon`.
- `go test ./...` in `cli/beacon-hooks`.
- `go test ./...` in `collector-builder/exporter/beaconjsonexporter`.
- `go test ./...` in `pkg/asymptoteobserve` (includes threat-rules pack conformance).
- CLI help smoke checks for the public command tree.
- macOS packaging script validation via `packaging/macos/test-endpoint-scripts.sh`.
- macOS endpoint smoke validation via `packaging/macos/smoke-endpoint.sh`.
