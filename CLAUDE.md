# CLAUDE.md

Guidance for Claude Code and other coding agents working in this repository.

## Project Scope

Beacon Endpoint Agent is a local-only endpoint telemetry agent for AI runtimes. The shipping code paths are:

- `cli/beacon`: public `beacon` CLI and endpoint runtime.
- `cli/beacon-hooks`: hook adapter invoked by Cursor and other supported runtimes.
- `collector-builder`: OpenTelemetry Collector distribution and Beacon JSONL exporter.
- `packages/asymptote-sdk-js`: TypeScript SDK for cloud agent telemetry that exports Beacon-compatible OpenTelemetry spans.
- `browser-extension`: optional MV3 collector (Chrome, plus a beta Firefox build target) that relays Claude.ai and ChatGPT chat telemetry into the local pipeline as OTLP.
- `pkg/asymptoteobserve`: shared Go library — event schema, harness-name normalization, provenance markers, token usage, and the threat-rules engine. Imported by the CLI, the hook adapter, and the collector exporter, so a change here reaches all three.
- `plugins`: TypeScript sources for the managed runtime plugins/extensions (`opencode-beacon`, `cline-beacon`, `pi-beacon`, `omp-beacon`, `openclaw-beacon`, `prime-beacon`, `omo-beacon`), each mirrored into an embedded copy under `cli/beacon/internal/endpoint/hooks/assets/`.
- `rules` and `spec/threat-rules`: the open rule corpus and the Threat Rules format it conforms to.
- `beacon-sandbox`: harness that runs a real Claude Code session in a disposable sandbox and checks what Beacon captured. See `beacon-sandbox/AGENTS.md`.
- `packaging`: macOS, Linux, and Windows packaging and deployment assets.
- `docs`: the published documentation site (docs.asymptotelabs.ai).

Do not recreate or depend on removed `asymptote` mirror trees. Keep new work focused on the Beacon paths above.

## Product Posture

- Beacon is visibility-first endpoint telemetry for local AI agent runtimes, not a hosted policy service or general endpoint protection product.
- Preserve the local-only default. The public Beacon build must not require a hosted account, remote policy fetch, hosted dashboard, or external network dependency during normal hook execution, and hooks themselves never touch the network.
- `beacon login` is the account-identity channel for interactive users and is required by the interactive `beacon endpoint install` wizard. It uses browser PKCE against beacon.sh and stores the scoped CLI token only in `~/.beacon/auth/session.json` (0600 under a 0700 directory). Signing in alone never starts forwarding. User-mode `beacon endpoint connect` may send that token once to `POST /api/cli/enroll/account` to authorize minting a separate device ingest key; never copy the account token into endpoint config, Vector, hook environments, logs, or command output. System/root/package/MDM/CI/piped-stdin/dry-run installs stay noninteractive and account-free.
- Beacon Managed forwarding (`cli/beacon/internal/endpoint/asymptote`, `beacon endpoint connect`, `beacon endpoint asymptote`) is Vector-shaped: Beacon writes local JSONL, Vector ships runtime lines only from the connection point forward and reads inventory from the beginning to establish the endpoint's current baseline, and the `bcn_device_*` key stays only in `<BaseDir>/asymptote/vector-secrets.json` via Vector's secret backend. The onboarding wizard preselects Managed, and confirming it connects this endpoint in the same command once the local install has succeeded; an endpoint that is already enrolled is never re-enrolled, because enrollment rotates the device key. Standard mode ships locally sanitized retained content; metadata-only uses local Vector transforms to remove prompts, responses, reasoning, tool arguments/results, command output, raw fields, diffs, inventory content, and MCP definitions before buffering/upload. Keep the ingest URL `https://`, the wire contract aligned with `beacon-ingest` (`/v1/ingest/{runtime,inventory}` and `/v1/ingest/health`), and do not add another endpoint network channel. The cloud-sandbox shuttle is the bounded exception: only with `BEACON_ORIGIN=cloud`, it can upload new runtime lines to GCS, S3, or the same Beacon ingest contract because no Vector/service manager/browser/persistent disk exists there.
- `beacon endpoint install` has a full-screen onboarding wizard on interactive user TTYs. It signs in, preselects Beacon Managed, requires an explicit choice to opt out to Local, and asks Managed users to choose Standard or Metadata-only privacy. Signing in alone still moves no telemetry. Confirming Managed does: the confirm screen states that it installs Beacon and starts forwarding, and names what the selected privacy mode sends, so that screen is the consent point. The managed destination is recorded only after enrollment succeeds, so a failed enrollment leaves a working local install, prints `beacon endpoint connect` as the retry, and re-offers the question on the next install. The onboarding record is written only after `lifecycle.Install` returns, because a profile written first left a failed install looking onboarded and silently skipped the wizard on the retry. Explicit `install --connect` is unchanged. The wizard is skipped for system/root/CI/piped-stdin/dry-run or `BEACON_ONBOARDING=0`; those unattended paths must never gain an account or browser dependency. Legacy `BEACON_ONBOARDING_EMAIL`/`USAGE` submissions remain only for fleet compatibility. Implementation lives in `cli/beacon/internal/onboarding` and `cli/beacon/cmd/endpoint_onboarding.go`; keep `docs/cli/endpoint-onboarding.mdx` in sync.
- Do not add dependency vulnerability scanning, OSV/GHSA lookups, package remediation, or other vulnerability-enforcement flows to the public hook path.
- Do not add broad runtime enforcement unless explicitly requested. Current control behavior is limited to hook-native approvals/denials exposed by supported agent runtimes.
- Keep destination support shaped like the existing local JSONL and customer-managed forwarding paths. Beacon may render supported packs or token-based endpoint forwarders, but should not add hosted SIEM dependencies or store third-party service credentials beyond the explicit supported configs. Elastic remains a file-tailing pack over local JSONL.
- Beacon writes retained prompt text, command output, raw tool inputs, raw OTLP attributes, and raw diffs to local or customer-controlled logs, subject to local redaction and size limits where supported.
- The Asymptote Observe TypeScript SDK may default to the hosted `/v1/observe` endpoint as an opt-in cloud SDK contract. Beacon endpoint execution stays local-only/no-network.

## Telemetry Scope

Supported runtime surfaces today:

- Claude Code and Codex CLI use local OpenTelemetry settings, and `beacon endpoint install` writes their user-level hooks alongside those settings (`otlpTargetCarriesHook` in `cli/beacon/cmd/endpoint_targets.go`), because Claude's OTLP export has no session lifecycle, file activity, or subagent events and Codex's SessionStart hook supplies the OS-user context its spans lack. `beacon endpoint codex sync` is the offline poll backfill from `~/.codex/sessions`; it never reads `~/.codex/auth.json` and cannot approve, deny, or delay tool calls.
- Cursor hook telemetry for sessions, prompt submission, tool use, command execution, MCP-like tool activity, approval decisions, file edits, and agent reasoning (`afterAgentThought` thinking text recorded as `agent.reasoning` events in the OTel GenAI `gen_ai.output.messages` reasoning-part shape) where hook payloads expose those fields.
- Cline uses a Beacon-managed plugin (`~/.cline/plugins/beacon.ts`) for live capture plus `beacon endpoint cline sync` for local history backfill. Cline's own OTel export/hosted prompt storage are not used, and approvals are not exposed or synthesized.
- Pi-family runtimes use Beacon-managed extensions and the shared `cli/beacon-hooks/cmd/pi_family.go` mapper while keeping separate harness names and install roots: Pi (`pi_cli`), Oh My Pi (`omp`), Prime Agent (`prime_agent`), and Senpi/OMO (`omo_senpi`). Pi, Prime, and Senpi do not expose operator approval decisions, so Beacon does not synthesize them. Oh My Pi exposes real approval events and the extension carries the remembered tool input onto those approvals.
- Prime Agent is the Pi-family exception with a single persistent `ipython` tool: kernel cells are recorded as `command.executed`, carry Python status/output metadata, and have no invented exit code. MCP is not separately attributed because Prime exposes integrations inside the kernel. `beacon endpoint prime sync` is the local poll backfill from `~/.prime/agent/sessions` and session artifacts.
- Senpi is the standalone OMO edition. The OpenCode and Codex editions ride existing Beacon integrations, so do not alias bare `oh-my-openagent` to this extension. The install target is `omo`; the event harness is `omo_senpi`; agent-directory overrides follow OMO's own order (`OMO_CODING_AGENT_DIR`, `SENPI_CODING_AGENT_DIR`, then `PI_CODING_AGENT_DIR`).
- OpenCode uses the managed plugin for live capture and approvals, plus `beacon endpoint opencode sync` for current SQLite and legacy file-store backfill. Poll events are marked `harness.collection_method=poll`, cannot hold or deny tools, and must not synthesize approvals from persisted tool results.
- Muse Code uses a Beacon-owned hooks file referenced by Muse `settings.json` via `managed_hooks_path`. Muse Spark is the model, not the runtime; do not normalize Spark spellings to `muse_code`. `PreLLMCall`/`PostLLMCall` stay unsubscribed because they carry full message arrays/tool schemas and chunked responses.
- OpenHands hooks are merged into the runtime's own `.openhands/hooks.json`. Project scope is the practical default because the loader takes the first hooks file and the CLI/GUI agent server reads the repository file. Approvals, token usage/cost, per-call tool ids, and assistant response text are not exposed or synthesized.
- Kimi Code registers in the `[[hooks]]` array of the runtime's own `$KIMI_CODE_HOME/config.toml` (default `~/.kimi-code`), the same file that holds `[providers.<name>].api_key`. Beacon appends its block and never re-serializes the document, and every write is parsed back and compared against the intended result before it replaces the file. Detection and removal go by the hook command rather than by Beacon's comment, because Kimi Code's legacy `kimi-cli` migration re-serializes config.toml and drops every comment. There is no project scope: Kimi Code reads one user-level config file. Thirteen of its twenty events are registered; `SessionHeartbeat` is deliberately left out because its sixty-second timer runs only when a hook is configured for it, and `Interrupt` because Beacon's closing action asserts the agent finished. Kimi Code reports real operator approval decisions over `PermissionRequest`/`PermissionResult`, so `PreToolUse` must not also synthesize one. Beacon writes nothing to stdout on this runtime, because `UserPromptSubmit` stdout is appended to the model's context; the policy seam denies with exit code 2 and is inert in the permission-request phase, which Kimi Code does not allow blocking. No token usage is exposed on any hook payload.
- Kiro uses one Beacon-owned hook file under `.kiro/hooks` or `$KIRO_HOME`/`~/.kiro/hooks`. Its hook stdout is added to agent context on `SessionStart` and `UserPromptSubmit`, so Beacon writes nothing to stdout on Kiro; blocking is exit code 2 with the reason on stderr. Approvals are not exposed or synthesized.
- goose currently has only the adapter: `cli/beacon-hooks/cmd/goose.go` can map a manually written `hooks.json` that invokes `beacon-hooks --platform goose`, but there is no `beacon endpoint hooks install --harness goose`, no `DiscoverGoose`, and no automatic OTLP configuration. Keep README/docs from implying goose is generally installable until those pieces exist. goose uses HTTP OTLP (4318), its blocking hook stdout must be `{"decision":"allow"}` or `{"decision":"block"}`, and it exposes no approval decisions or tool output.
- DeepSeek Harness (`dsh`) is installed through two user-level files: `$DSH_HOME/beacon-endpoint-hooks.json` and a row in `$DSH_HOME/cordis.patch.yml`; there is no project scope. It also installs a Beacon-owned local skill at `$DSH_HOME/skills/beacon-endpoint/SKILL.md` and supports native session backfill from `$DSH_HOME/sessions` through `beacon endpoint dsh sync`, which fills assistant text/reasoning, tool failures, and token usage that the hook bridge does not expose. It rides the Claude-shaped envelope on the hook path, but `run_code` remains `tool.invoked` because nested commands fire their own hooks.
- fx (vercel-labs/fx) telemetry read from the session store fx commits under `~/.fx/sessions/`, covering session start, prompts, tool calls, commands with exit codes and output, file reads/creates/edits with diffs, MCP tool calls, agent messages, history compaction, and per-turn token usage plus reported cost. fx exposes no third-party hook, plugin, or OTLP surface -- its lifecycle hooks are compiled into the binary -- so collection is a read of fx's own records, run by `beacon endpoint fx sync`. Every event carries `harness.collection_method=poll`; approvals are not synthesized (fx persists permission feedback text, not a per-call decision, matching the Cline and Pi posture) and no session-end event is invented, since fx sessions are resumable. Implementation lives in `cli/beacon/internal/fxsession` and `cli/beacon/cmd/endpoint_fx.go`.
- Claude Cowork admin-configured OpenTelemetry setup guidance and local validation.
- OpenClaw Gateway uses a Beacon-managed plugin directory plus OpenClaw's own diagnostics OTLP guidance (`beacon endpoint integrations openclaw`). The plugin's `before_tool_call` hook is fail-closed, so the handler dispatches synchronously and returns without awaiting. Conversation hooks, including `llm_output`, require `plugins.entries.beacon-endpoint.hooks.allowConversationAccess`; without it, token and agent-message coverage is partial. Beacon reads OpenClaw JSON/JSON5 config for advice but does not rewrite it.
- GitHub Copilot CLI uses MDM/customer-managed OTLP HTTP for live spans and approval-like activity, plus `beacon endpoint copilot sync` to backfill local session records from `~/.copilot/session-state`. Poll events carry assistant text, complete tool results, command/file details, and token usage with `harness.collection_method=poll`; approvals still come only from the live OTLP path and are not synthesized from persisted records.
- Runtimes that ride shared hook/OTLP paths without a special contract worth expanding here include Antigravity CLI, Devin CLI, Devin Desktop, Factory Droid, Gemini CLI, Grok Build, Hermes Agent, Qwen Code, and VS Code. Installed hook/endpoint support is governed by `cli/beacon/cmd/endpoint_targets.go` plus the install/status switches in `cli/beacon/cmd/endpoint_hooks.go`; discovery rows live in `cli/beacon/internal/endpoint/harness`.
- Browser chat telemetry through an optional MV3 extension (`browser-extension/`; Chrome, with a beta Firefox build) covering claude.ai and chatgpt.com. A main-world interceptor tees the streamed response, a per-site adapter parses it into a normalized turn, and the service worker POSTs OTLP GenAI logs to the local collector on `127.0.0.1:4318`; the extension never writes files and never contacts a remote endpoint. `harness.name` resolves to `claude_web` or `chatgpt_web` (already handled by `NormalizeHarnessName` in `pkg/asymptoteobserve/harness.go`, where the browser cases must stay above the generic `claude` rule) and `harness.collection_method` is `otlp`. The one build serves every Chromium browser; each event names the browser it came from in the typed `user_agent.name`/`user_agent.version` fields (OTel semconv, promoted by the exporter so Metadata-only forwarding keeps them), resolved in `browser-extension/src/shared/browser-identity.ts` from UA Client Hints brands, with a UA-string fallback that also names Firefox. Scope is deliberately narrow: only those origins' chat streams, never other tabs or general browsing. Retention defaults to `full`, so prompt and response text is retained by default; this is documented rather than defaulted down, because browser chat telemetry without content has no investigative value. Treat the adapters as experimental, since both sites' stream formats are private and undocumented. Per-browser differences live only in `browser-extension/tools/targets.mjs` (one entry per target, deriving its manifest from `src/manifest.json`; Chrome ships that file verbatim to `dist/`, Firefox gets `dist-firefox/`), and every extension API call goes through the `src/shared/browser.ts` shim (a unit test fails on direct `chrome.*` use). The Firefox Gecko ID `browser-collector@agent-beacon.asymptotelabs.ai` is pinned and must never change.
- `beaconjson` OpenTelemetry Collector exporter that converts OTLP logs, traces, metrics, and resource attributes into Beacon endpoint JSONL.
- Endpoint inventory (`inventory.heartbeat` / `inventory.snapshot` in `inventory_state.jsonl`) is written by a scheduled job, `com.beacon.endpoint.inventory` (`service.InventoryManager`, user LaunchAgent or system LaunchDaemon, `beacon-inventory.timer` on systemd), every six hours and once at load, on by default and reconciled by `lifecycle.ReconcileInventoryJob` on every install, repair and package upgrade (`beacon endpoint inventory install-daemon`). Agent hooks do not trigger inventory any more; the hook binary's `--cli` flag and `inventory-heartbeat` subcommand survive only as no-ops for hook commands written by older versions, and `beacon endpoint inventory heartbeat --trigger hook` is likewise retired. A system-mode run inventories the active console user's home and still writes the liveness heartbeat when nobody is logged in; the scheduled scan has no working directory, so project-scoped config is only in `beacon endpoint inventory`.
- Beacon Managed forwarding, the one Beacon-hosted network channel for endpoint telemetry: user-mode `beacon endpoint connect` authorizes device enrollment with the signed-in CLI account; system mode uses browser PKCE at `/cli/enroll`. Both store only the per-device key in `<BaseDir>/asymptote/vector-secrets.json` (0600, 0700 dir), render the selected privacy mode into `vector.toml`, validate it, and run Vector as `com.beacon.endpoint.asymptote-forwarder` / `beacon-asymptote-forwarder.service` through `service.ForwarderManager` (refused in supervised mode). `enrollment.json` and `config.json`'s `managed_ingest` block carry only non-secret identity and privacy mode; `beacon endpoint status` reports forwarder state and a live credential check (`GET /v1/ingest/health`, 3 s timeout); `disconnect` and `uninstall` remove the forwarder and credentials but never revoke server-side.
- Asymptote Observe TypeScript SDK instrumentation for cloud applications, starting from OpenTelemetry/OpenLLMetry patterns and `observe()` wrappers.
- Elasticsearch/Filebeat content pack generation for forwarding local Beacon JSONL into customer-managed Elastic deployments or the bundled loopback-only development stack.
- A local-only dashboard served by `beacon endpoint dashboard`, bound to loopback by default and backed by the runtime JSONL log.
- Token usage and runtime-reported cost capture across spans, logs, and metric datapoints, normalized into `gen_ai.usage`, with attribution rollups served by the dashboard token view (`/api/tokens`) and the `beacon token-usage` report command for local and CI logs.
- Local threat detection via `beacon scan`, which runs the open Threat Rules format (`spec/threat-rules`; CEL match conditions over the endpoint event schema, with embedded conformance fixtures) over the runtime JSONL. The engine (`pkg/asymptoteobserve/threatrules`) ships in the binary; the rule corpus is external data loaded from a local store (`~/.beacon/endpoint/rules`) managed by `beacon rules`, so the corpus can grow without enlarging the binary. `scan` is read-only and offline; only the explicit, user-initiated `beacon rules pull <url>` reaches the network. A small frozen baseline is embedded so `scan` works before any rules are installed.
- An optional, off-by-default policy seam in the hook path (`cli/beacon-hooks/internal/policy`) governed by the stable contract in `pkg/asymptoteobserve/policycontract`. When `BEACON_POLICY_PROVIDER` names an executable, the `pre-tool` and `permission-request` hooks send it a JSON request describing the imminent tool call and honor an allow/deny response, returning the runtime's native deny shape and `policy.enforcement=enforce` telemetry on a deny. The seam is inert when unset and fail-open on any error (timeout, non-zero exit, malformed output), so the open build ships no enforcement of its own; all audit/enforce decision logic lives in the external provider. `rules/agent-control/agent-permission-bypass-spawn.rule.yaml` is the open detect twin that flags the same pattern in `beacon scan`.

Current non-goals unless explicitly requested:

- Kernel/process monitoring, EDR replacement, shell history scraping, cloud audit ingestion, general browser or SaaS activity monitoring beyond the supported chat surfaces, credential-use attribution, and MCP configuration inventory.
- Direct hosted integrations for Datadog, Snowflake, Chronicle, Panther, or other SIEM destinations beyond explicitly supported local/customer-managed forwarding patterns and the opt-in Asymptote Managed path.
- Dependency vulnerability scanning or package security remediation.

## Common Commands

Run tests for the public CLI. Build the embedded hooks binary first — several tests
require a real one and fail against the checked-in placeholder:

```bash
cd cli/beacon
make build-hooks-current
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

Run the managed runtime plugin/extension checks (each verifies the checked-in copy matches its embedded twin, then runs its tests):

```bash
cd plugins/opencode-beacon && bun run check && bun test
cd ../cline-beacon && bun run check && bun test
cd ../pi-beacon && bun run check && bun test
cd ../omp-beacon && bun run check && bun test
cd ../openclaw-beacon && bun run check && bun test
cd ../prime-beacon && bun run check && bun test
cd ../omo-beacon && bun run check && bun test
```

After editing a plugin or extension source, run `bun run sync` in its directory to update the
embedded copy under `cli/beacon/internal/endpoint/hooks/assets/`; a Go test fails if the two drift.
`openclaw-beacon` syncs three files rather than one, because OpenClaw discovers a plugin as a
directory: the entry plus the two manifests it is declared in.

Run TypeScript SDK checks:

```bash
cd packages/asymptote-sdk-js
npm test
npm run check
npm run build
npm run pack:dry-run
```

Run browser extension checks. The e2e replays recorded SSE through the real
extension in headless Chromium, so it needs `openssl` on PATH and a one-time
Chromium download, but no login and no network:

```bash
cd browser-extension
npm ci
npm run check
npm run test:unit
npx playwright install chromium   # one-time
npm test                          # builds dist/, runs the replay e2e
```

Exercise the goose adapter during manual testing. There is no `--harness goose` installer yet (see
the goose entry under Telemetry Scope), so the hooks file and the OTLP endpoint are written by hand
and only the payload mapping can be run from the repository:

```bash
cd cli/beacon-hooks
go test ./... -run Goose
```

Collect fx session telemetry during manual testing:

```bash
cd cli/beacon
go run . endpoint fx status        # sessions fx has written and how much Beacon has read
go run . endpoint fx sync --print  # map records to events without writing anything
```

Connect an endpoint to Asymptote Managed during manual testing (needs Vector 0.50+ on the machine;
`/opt/beacon/bin/vector` from the signed package works, and a browser signed in to an org that has
managed ingest enabled):

```bash
cd cli/beacon
go run . endpoint connect            # browser approval → device key → forwarder service
go run . endpoint status --json | jq .managed_ingest
go run . endpoint disconnect         # stop the forwarder and remove the local key
```

Run the Asymptote Managed forwarder by hand instead (a device key from `beacon-ingest/scripts/mint_test_device.py`
or a previous connect):

```bash
cd cli/beacon
go run . endpoint asymptote install-pack --output /tmp/beacon-asymptote-pack --log-path "$HOME/.beacon/endpoint/logs/runtime.jsonl"
BEACON_ASYMPTOTE_INGEST_URL=https://<ingest> BEACON_ASYMPTOTE_SECRETS_FILE=/path/to/secrets.json BEACON_ASYMPTOTE_DATA_DIR=/tmp/beacon-asymptote-data \
  vector validate --skip-healthchecks /tmp/beacon-asymptote-pack/vector.toml
go run . endpoint asymptote validate   # writes a validation event the forwarder ships
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
`.github/workflows/release.yml` (no local publish steps required), in three jobs:

1. `goreleaser` (ubuntu, `environment: release`) builds the collector dists,
   publishes the GitHub release + changelog and tarballs, and updates the
   Homebrew tap.
2. `package` (macOS, `needs: goreleaser`, `environment: release`) builds the
   Apple Silicon `beacon`/`beacon-hooks` package payload plus arch-matched
   collector and Vector binaries for `darwin_arm64`, then signs, notarizes,
   staples, and uploads the `.pkg`, `.sha256`, and `update-manifest.json`.
   GoReleaser still publishes multi-arch CLI tarballs; the signed endpoint
   `.pkg` is arm64-only so it can carry a current Vector release.
3. `windows-package` (windows-2025, `needs: goreleaser`) builds the x64 MSI from
   the published archive, installs and uninstalls it on the runner to prove it
   works, and attaches the `.msi` and its `.sha256`. Deliberately **not** in the
   `release` environment: that environment gates signing secrets, and the MSI is
   unsigned, so this job has none to gate.

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

The Linux `.deb`, `.rpm`, and release archives carry Vector too. Run
`sh packaging/linux/fetch-vector.sh` before GoReleaser: it downloads the static
(musl) build for both arches, checks it against Vector's published checksums and
for a program interpreter, and stages it under `cli/beacon/release-vector/`. The
GoReleaser before-hook fails without it. The packages install it as
`/opt/beacon/bin/vector`; the archives carry it as `beacon-vector`, which the
Homebrew formula installs on Linux. The version is pinned in that script at
0.56.0, the same as the macOS package. Vector 0.57 and 0.58 stop expanding the
`${VAR}` references the generated packs rely on, so bump it only together with
`packaging/linux/validate-vector-packs.sh` passing against the new version.

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

Check Homebrew on Linux with a real install, since formula changes only take effect
once a release publishes them. It should print a Vector version:

```bash
docker run --rm --platform linux/amd64 homebrew/brew sh -c \
  'brew install asymptote-labs/tap/beacon && "$(brew --prefix)/bin/beacon-vector" --version'
```

The release should include the five GoReleaser CLI archives (four `.tar.gz` plus
`beacon_<version>_windows_amd64.zip`), `checksums.txt`, `threat-rules.tar.gz`,
`BeaconEndpointAgent-<version>-arm64.pkg`, its `.sha256`, `update-manifest.json`,
and `BeaconEndpointAgent-<version>-x64.msi` with its `.sha256`.

The MSI is unsigned, so `pkgutil`/`stapler`/`spctl` have no Windows counterpart to
run. Its `.sha256` is therefore the only integrity check it has, and the release
job proves the package works by installing it on a runner before attaching it —
verification by execution rather than by signature. Both change when Authenticode
signing is added. Verify the package before announcing the release:

```bash
tmpdir="$(mktemp -d)"
cd "$tmpdir"
gh release download <tag> --repo Asymptote-Labs/agent-beacon --pattern 'BeaconEndpointAgent-*-arm64.pkg*' --pattern update-manifest.json
expected="$(awk '{print $1}' BeaconEndpointAgent-*-arm64.pkg.sha256)"
actual="$(shasum -a 256 BeaconEndpointAgent-*-arm64.pkg | awk '{print $1}')"
test "$expected" = "$actual"
pkgutil --check-signature BeaconEndpointAgent-*-arm64.pkg
xcrun stapler validate BeaconEndpointAgent-*-arm64.pkg
spctl --assess --type install -vv BeaconEndpointAgent-*-arm64.pkg
jq . update-manifest.json
```

For releases that include endpoint self-update changes, validate the manual apply
path from the prior installed package on an Apple Silicon Mac:

```bash
/opt/beacon/bin/beacon version
sudo /opt/beacon/bin/beacon endpoint update --apply
/opt/beacon/bin/beacon version
/opt/beacon/bin/beacon endpoint status --system
sudo tail -n 20 /var/log/beacon-agent/system.jsonl
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
sh packaging/linux/fetch-vector.sh "$PWD/.tmp/release-<tag>/cli/beacon/release-vector"
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

### Browser Extension Release

The browser extension versions independently of the CLI. A pushed `ext-v*` tag
runs `.github/workflows/release-extension.yml`, which creates its own GitHub
release carrying the unpacked Chromium-family build as a zip plus a `.sha256`.
The zip keeps its `-chrome.zip` name: `chrome` names the Chromium extension
target family, and the same archive loads in Edge, Brave, Opera, Vivaldi and Arc.

`ext-v*` and the CLI's `v*` trigger cannot collide: GitHub tag patterns anchor at
the start, so `v*` does not match `ext-v0.1.0`.

Bump the version in **both** `browser-extension/package.json` and
`browser-extension/src/manifest.json` before tagging. The workflow requires the
tag and both files to agree and fails otherwise.

```bash
cd browser-extension
npm run check && npm run test:unit && npm run build
cd ../
git tag -a ext-v<version> -m "ext-v<version>"
git push origin ext-v<version>
gh run list --workflow release-extension.yml --limit 5
```

A second job, `firefox`, signs the Firefox build through AMO as an unlisted
add-on and attaches `agent-beacon-browser-extension-<version>-firefox.xpi` plus
its `.sha256`. It runs only when the `AMO_JWT_ISSUER` and `AMO_JWT_SECRET`
repository secrets exist, and skips when the release already has that `.xpi`,
because AMO signs each version once. Without the secrets it is a no-op and the
release still succeeds with the Chrome zip alone.

The zip is unsigned, so its `.sha256` is the only integrity check it has, the
same posture as the Windows MSI. The workflow proves the artifact works by
unzipping the published bytes and asserting they are a loadable MV3 extension.
Verify after the run:

```bash
gh release download ext-v<version> --repo Asymptote-Labs/agent-beacon \
  --pattern 'agent-beacon-browser-extension-*-chrome.zip*'
shasum -a 256 -c agent-beacon-browser-extension-<version>-chrome.zip.sha256
```

Chrome Web Store publication is not yet wired. Until it is, the zip is loaded
unpacked through Chrome's developer mode.

## Implementation Notes

- Prefer deterministic tests that use `t.TempDir()`, `testenv.SetHome(t, ...)` (`cli/beacon/internal/testenv`; a bare `t.Setenv("HOME", ...)` does not move `os.UserHomeDir` on Windows), fake binaries, and free local ports. Gate Unix permission-bit assertions with `testenv.HasPOSIXFileModes()` or `testenv.RequirePOSIXFileModes(t)`, and shell-script fixtures with `testenv.RequirePOSIXExecutableFixtures(t)`.
- Avoid tests that require root, real `launchctl` service changes, Wazuh, a live collector, or external network access.
- For macOS-only behavior, gate tests with `runtime.GOOS == "darwin"` or assert the non-Darwin contract explicitly.
- Keep endpoint event schema fields stable: `vendor`, `product`, `schema_version`, required event fields, and Wazuh-compatible JSONL output are release contracts.
- Preserve optional event fields for agent-native metadata (`session`, `trace`, `tool`, `file`, `command`, `mcp`, `approval`, `content`, `model`, `repository`, and `branch`) without changing existing required field semantics.
- Reading a runtime's own on-disk session store is a `poll` collection path, not a new method: it is a pull that sees committed records after the fact, which is what `poll` means. fx is the case that exercises it (`cli/beacon/internal/fxsession`); a future runtime with the same shape uses `poll` too rather than adding a fifth value to a release-contract field.
- Set the provenance markers on every new collection path. `harness.collection_method` (`hook`, `otlp`, `plugin`, `poll`) records the mechanism; `event.fidelity` (`observed`, `inferred`) records whether the source named the action or Beacon derived it. Both are defined in `pkg/asymptoteobserve/provenance.go` and are the only sanctioned way to express that difference — do not add per-runtime confidence fields, and do not leave a derived action unmarked. A new plugin-shaped runtime must be added to `CollectionMethodForPlatform`, whose default is `hook`. An action synthesized from an adjacent observation, such as an approval built from a pre-tool notification on a runtime with no approval hook, is `inferred`.
- Keep event identity derivable rather than asserted. `gen_ai.tool.call.id` carries the runtime's own name for one tool invocation, promoted on every capture path from the shared alias list in `pkg/asymptoteobserve.ToolCallIDKeys`; a new runtime adds its spelling there rather than in a mapper. `event.id` is a deterministic UUID derived by `EventIDForLine` from the event about to be written, so both capture paths name one action the same way. Its namespace constant and derivation are a contract: changing either renames every event ID Beacon has written, and a test pins the value.
- Keep `gen_ai.usage` as the single canonical token-usage representation: normalize all runtime token telemetry (span attributes, metric datapoints, legacy `llm.usage.*` aliases) into it, mirror OTel GenAI semconv JSON names exactly, and never add parallel or per-harness token fields. `gen_ai.usage.cost_usd` carries runtime-reported cost only; do not derive cost from local pricing tables.
- When adding a new signal, include stable identifiers/counts/hashes alongside any retained raw content, and route raw fields through redaction, sanitization, truncation, and event-size controls.
- Keep the dashboard read-only. It should inspect local status and JSONL events but must not mutate endpoint configuration or telemetry.
- The Asymptote device key has exactly one home, `<BaseDir>/asymptote/vector-secrets.json`, read by Vector's `secret` file backend. Never copy it into `config.json`, `vector.toml`, a service unit, an environment variable, a log line, or a `--json` result; `HasSecretDestinations` is unchanged because the key is not in `config.json`. `internal/auth` is flow-agnostic (PKCE, loopback callback server with an injected exchange function, browser opener, `PostJSON`); new browser flows reuse it rather than adding a second callback server. `connect` must fail before opening a browser when Vector or a service manager is missing, or when Vector rejects the rendered config, so no key is minted or rotated that cannot be used; enrollment rotates a connected device's key in place, so every local check runs ahead of it; the steps that can only fail after the exchange (storing the key, writing the config and unit, starting the service) are covered by the `asymptote/connect-pending` marker, written as soon as the key arrives and removed only when connect finishes, so status reports an incomplete connect instead of the previous connection. A failed first connect stops and removes any partial forwarder before returning, which keeps a later local-only onboarding choice local.
- Keep the release readiness guidance in `README.md` up to date when install, packaging, collector, or dashboard behavior changes.

## CI Expectations

`.github/workflows/ci.yml` runs these jobs on every pull request and on pushes to `main`:

- `go-test` (macOS): `bun run check && bun test` in all seven `plugins/*` directories, then `make build-hooks-current`, then `go test ./...` in `cli/beacon`, `go test -race ./internal/endpoint/...`, `go test ./...` in `cli/beacon-hooks`, `collector-builder/exporter/beaconjsonexporter`, and `pkg/asymptoteobserve` (includes threat-rules pack conformance), then CLI help smoke checks that also assert removed commands stay unexposed.
- `linux-test` (ubuntu) and `windows-test` (windows-2025) rerun the Go suites per platform. The Windows job tests a measured package list rather than `./...`; the excluded packages are tracked in #318, and the scope must not be widened back to `./...`.
- `typescript-sdk` (Node 20, 22, 24): `npm test`, `npm run check`, `npm run build`, `npm run pack:dry-run`, `npm run pack:smoke` in `packages/asymptote-sdk-js`.
- `browser-extension` (Node 22, matrix over `chromium` and `msedge`): `npm run check`, `npm run test:unit`, `npm run build`, a check that the build is a loadable MV3 extension, `npm run lint:firefox` (the Firefox build plus `web-ext lint --warnings-as-errors`), and the Playwright replay e2e running the real extension headless in that leg's browser (`BROWSER_CHANNEL`) against a local HTTPS replay server; the HTML report and traces upload on failure. Brave, Opera, Vivaldi and Arc have no Playwright channel; `BROWSER_EXECUTABLE` runs the same e2e against their binaries for the manual checklist in `docs/runtimes/browser-extension-chromium.mdx`.
- `beacon-sandbox` (ubuntu): `go build`, `go vet`, and `go test` for the verification harness. Hermetic — no Modal account, no API key, no paid resources.
- `cross-build` (ubuntu): builds all five release targets and asserts the Linux binaries are statically linked (no `PT_INTERP`).
- `package-smoke` (macOS): `packaging/macos/test-endpoint-scripts.sh`, a `build-pkg.sh` payload check, and `packaging/macos/smoke-endpoint.sh`.
- `linux-systemd-smoke` and `linux-package-smoke` (ubuntu): the real systemd install path, and installing the built `.deb` in a systemd container.
- `windows-package-smoke` (windows-2025): `packaging/windows/test-endpoint-scripts.ps1`, then building the MSI and installing it.

The Go jobs build the embedded hooks binary first (`make build-hooks-current`); `cli/beacon` tests
fail against the placeholder because `InstallFactory` requires a real binary.
