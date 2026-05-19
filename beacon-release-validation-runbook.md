# Beacon CLI End-To-End Release Validation Runbook

## Goal
Validate a stable public Beacon release from the user-facing docs at [https://docs.asymptotelabs.ai/cli](https://docs.asymptotelabs.ai/cli), not a local build, and capture any install, docs, integration, or product bugs in `beacon-installation-notes.md` before changing code.

Use repo context only after runtime testing to decide whether each finding is docs-only, CLI behavior, hook behavior, collector/exporter behavior, packaging, or Elastic content-pack behavior.

## Test Environment
Run on macOS with:

- Homebrew available and online.
- Docker Desktop or Docker Compose available for local Elastic validation.
- A dedicated test macOS user or a workstation where changing local AI runtime config is acceptable.
- Authenticated/installable runtimes for the full matrix: Cursor, Claude Code, Codex CLI, Gemini CLI, and OpenCode.
- Repo checkout available locally. Set `BEACON_REPO` to that checkout path for writing notes and later researching fixes.
- Integration validation is user-assisted for now. The agent prepares Beacon, asks the user to submit unique prompts in each runtime, then self-verifies the resulting events in `runtime.jsonl` and Elastic. Headless/noninteractive runtime commands can be used as an optional fast path only when local auth and CLI behavior are known to be reliable.

Before running commands, record environment evidence:

```bash
sw_vers
uname -m
brew --version
docker --version || true
docker compose version || true
which beacon || true
beacon version || true
which agent || true
agent --version || true
which claude || true
claude --version || true
which codex || true
codex --version || true
which gemini || true
gemini --version || true
which opencode || true
opencode --version || true
```

## Safety And State Baseline
Create a timestamped test directory and preserve all user-owned config files Beacon may touch:

```bash
export BEACON_E2E_RUN="${HOME}/beacon-e2e-$(date +%Y%m%d-%H%M%S)"
export BEACON_REPO="${BEACON_REPO:-$(pwd)}"
mkdir -p "$BEACON_E2E_RUN/backups"

for path in \
  "$HOME/.beacon" \
  "$HOME/.claude/settings.json" \
  "$HOME/.codex/config.toml" \
  "$HOME/.gemini/settings.json" \
  "$HOME/.cursor/hooks.json" \
  "$HOME/.config/opencode/plugins/beacon.ts"; do
  if [ -e "$path" ]; then
    mkdir -p "$BEACON_E2E_RUN/backups/$(dirname "${path#$HOME/}")"
    cp -R "$path" "$BEACON_E2E_RUN/backups/${path#$HOME/}"
  fi
done
```

Do not restore backups, stop Beacon, stop Elastic, or uninstall anything automatically. The default outcome of this runbook is to preserve the tested Beacon state so the user can continue inspecting runtime logs, hook config, the local dashboard, and Kibana after validation.

## Documentation Walkthrough
Read the docs exactly like a new user, starting from:

- [https://docs.asymptotelabs.ai/cli](https://docs.asymptotelabs.ai/cli)
- [https://docs.asymptotelabs.ai/cli/quickstart-developers](https://docs.asymptotelabs.ai/cli/quickstart-developers)
- [https://docs.asymptotelabs.ai/cli/installation](https://docs.asymptotelabs.ai/cli/installation)
- [https://docs.asymptotelabs.ai/cli/endpoint](https://docs.asymptotelabs.ai/cli/endpoint)
- [https://docs.asymptotelabs.ai/cli/hooks](https://docs.asymptotelabs.ai/cli/hooks)
- [https://docs.asymptotelabs.ai/cli/supported-runtimes](https://docs.asymptotelabs.ai/cli/supported-runtimes)
- [https://docs.asymptotelabs.ai/cli/elastic](https://docs.asymptotelabs.ai/cli/elastic)
- [https://docs.asymptotelabs.ai/cli/siem-forwarding-elastic](https://docs.asymptotelabs.ai/cli/siem-forwarding-elastic)

For every confusing step, mismatch, missing prerequisite, wrong command, broken link, or unclear success criterion, append a finding to `beacon-installation-notes.md` with:

```markdown
## Finding: <short title>

- Area: docs | install | endpoint | cursor | claude | codex | opencode | elastic | packaging
- Severity: blocker | major | minor | polish
- Environment: <OS, arch, Beacon version, runtime version>
- Expected: <what a new user would expect from docs>
- Actual: <what happened>
- Reproduction: <commands or UI steps>
- Evidence: <key command output, log line, event excerpt, screenshot path if applicable>
- Suspected cause: <leave blank until repo research if unknown>
- Proposed fix: <leave blank until repo research if unknown>
```

## Homebrew Install Validation
Validate the shipped Homebrew formula and installed artifacts:

```bash
brew tap asymptote-labs/tap
brew install beacon
brew info beacon
which beacon
beacon version
beacon --help
beacon endpoint --help
```

Acceptance criteria:

- `beacon version` prints a released version, not a local dev build.
- `which beacon` resolves to the Homebrew-installed binary.
- Help output includes `endpoint`, `endpoint hooks`, and `endpoint elastic` command groups.

## Endpoint Quickstart Validation
Follow the quickstart exactly:

```bash
beacon endpoint install
beacon endpoint status
beacon endpoint status --json > "$BEACON_E2E_RUN/status-after-install.json"
beacon endpoint discover
beacon endpoint discover --json > "$BEACON_E2E_RUN/discover-after-install.json"
```

Validate local state:

```bash
test -f "$HOME/.beacon/endpoint/config.json"
test -f "$HOME/.beacon/endpoint/otelcol.yaml"
test -f "$HOME/.beacon/endpoint/logs/runtime.jsonl"
python3 -m json.tool < "$HOME/.beacon/endpoint/config.json" > "$BEACON_E2E_RUN/config.pretty.json"
```

Acceptance criteria:

- Install exits successfully without sudo in user mode.
- Status reports collector/config/log paths clearly.
- `~/.beacon/endpoint/logs/runtime.jsonl` exists.
- The runtime log contains a Beacon endpoint event such as `telemetry.enabled` or another install/status event.
- Claude and Codex configuration files are created or updated consistently with the docs.

Repo areas to research later if failures occur:

- [`cli/beacon/cmd/endpoint.go`](cli/beacon/cmd/endpoint.go)
- [`cli/beacon/internal/endpoint/lifecycle/lifecycle.go`](cli/beacon/internal/endpoint/lifecycle/lifecycle.go)
- [`cli/beacon/internal/endpoint/harness/harness.go`](cli/beacon/internal/endpoint/harness/harness.go)
- [`cli/beacon/internal/endpoint/writer/writer.go`](cli/beacon/internal/endpoint/writer/writer.go)

## Cursor Integration Test
Install Cursor hooks, then ask the user to submit a unique prompt in Cursor:

```bash
beacon endpoint hooks install --harness cursor
beacon endpoint hooks status --harness cursor
beacon endpoint hooks status --harness cursor --json > "$BEACON_E2E_RUN/cursor-hooks-status.json"
```

User runtime action:

```bash
export BEACON_E2E_CURSOR_MARKER="Beacon E2E Cursor prompt $(date +%s)"
printf 'Please submit this prompt in Cursor: %s\n' "$BEACON_E2E_CURSOR_MARKER"
```

Optional headless fast path, only when Cursor CLI auth is known to work:

```bash
agent -p --output-format text "$BEACON_E2E_CURSOR_MARKER: answer with exactly the word beacon-ok." \
  > "$BEACON_E2E_RUN/cursor-headless.out" \
  2> "$BEACON_E2E_RUN/cursor-headless.err"
```

Validation:

```bash
rg "$BEACON_E2E_CURSOR_MARKER|cursor|prompt|beacon-ok" "$HOME/.beacon/endpoint/logs/runtime.jsonl" \
  > "$BEACON_E2E_RUN/cursor-runtime-events.txt"
```

Acceptance criteria:

- Hook status reports Cursor installed.
- The user confirms the prompt was submitted, or the optional headless command exits successfully.
- Runtime log contains a Cursor prompt event with the marker when content retention is `full`.
- Event has stable Beacon fields including `vendor`, `product`, `schema_version`, `event`, timestamp, and a Cursor/session context where available.
- Cursor remains usable if hook telemetry fails; if the prompt succeeds but no event appears, record this as an integration or hook-coverage finding.

Repo areas to research later:

- [`cli/beacon/internal/endpoint/hooks/cursor.go`](cli/beacon/internal/endpoint/hooks/cursor.go)
- [`cli/beacon-hooks/cmd/prompt_submit.go`](cli/beacon-hooks/cmd/prompt_submit.go)
- [`cli/beacon-hooks/internal/logging/logging.go`](cli/beacon-hooks/internal/logging/logging.go)

## Claude Code Integration Test
Confirm Beacon configured Claude settings, then ask the user to submit a unique prompt in Claude Code:

```bash
python3 -m json.tool < "$HOME/.claude/settings.json" > "$BEACON_E2E_RUN/claude-settings.pretty.json" || true
beacon endpoint status --json > "$BEACON_E2E_RUN/status-before-claude.json"
```

Runtime action:

```bash
export BEACON_E2E_CLAUDE_MARKER="Beacon E2E Claude prompt $(date +%s)"
printf 'Please submit this prompt in Claude Code: %s\n' "$BEACON_E2E_CLAUDE_MARKER"
```

Optional headless fast path, only when Claude Code headless mode is known to work:

```bash
claude -p "$BEACON_E2E_CLAUDE_MARKER: answer with exactly the word beacon-ok." \
  --output-format json \
  > "$BEACON_E2E_RUN/claude-headless.json" \
  2> "$BEACON_E2E_RUN/claude-headless.err"
```

Validation:

```bash
rg "$BEACON_E2E_CLAUDE_MARKER|claude|beacon-ok" "$HOME/.beacon/endpoint/logs/runtime.jsonl" \
  > "$BEACON_E2E_RUN/claude-runtime-events.txt"
```

Acceptance criteria:

- The user confirms the prompt was submitted, or the optional headless command exits successfully.
- Runtime log receives Claude OTLP-derived events.
- Prompt content appears only when retention mode permits it.
- If no events appear, status output makes collector/log path issues diagnosable.

Repo areas to research later:

- [`cli/beacon/internal/endpoint/harness/harness.go`](cli/beacon/internal/endpoint/harness/harness.go)
- [`collector-builder/exporter/beaconjsonexporter/exporter.go`](collector-builder/exporter/beaconjsonexporter/exporter.go)
- [`collector-builder/exporter/beaconjsonexporter/event.go`](collector-builder/exporter/beaconjsonexporter/event.go)

## Codex Integration Test
Confirm Beacon configured Codex, then ask the user to submit a unique prompt in Codex:

```bash
cp "$HOME/.codex/config.toml" "$BEACON_E2E_RUN/codex-config.toml" || true
beacon endpoint status --json > "$BEACON_E2E_RUN/status-before-codex.json"
```

Runtime action:

```bash
export BEACON_E2E_CODEX_MARKER="Beacon E2E Codex prompt $(date +%s)"
printf 'Please submit this prompt in Codex: %s\n' "$BEACON_E2E_CODEX_MARKER"
```

Optional headless fast path, only when Codex noninteractive mode is known to work:

```bash
codex exec --json "$BEACON_E2E_CODEX_MARKER: answer with exactly the word beacon-ok." \
  > "$BEACON_E2E_RUN/codex-headless.jsonl" \
  2> "$BEACON_E2E_RUN/codex-headless.err"
```

Validation:

```bash
rg "codex|$BEACON_E2E_CODEX_MARKER|beacon-ok" "$HOME/.beacon/endpoint/logs/runtime.jsonl" \
  > "$BEACON_E2E_RUN/codex-runtime-events.txt"
```

Acceptance criteria:

- The user confirms the prompt was submitted, or the optional noninteractive command exits successfully.
- Runtime log receives Codex OTLP-derived events.
- Prompt text behavior matches docs: Beacon config disables Codex prompt logging by default, so absence of raw prompt text may be expected, but agent activity events should still appear.
- Noisy internal transport spans should not dominate the log.

Repo areas to research later:

- [`cli/beacon/internal/endpoint/harness/harness.go`](cli/beacon/internal/endpoint/harness/harness.go)
- [`collector-builder/exporter/beaconjsonexporter/exporter_test.go`](collector-builder/exporter/beaconjsonexporter/exporter_test.go)

## Gemini CLI Integration Test
Gemini CLI support is opt-in, so repair the endpoint with the Gemini harness included, then ask the user to submit a unique prompt in Gemini CLI:

```bash
beacon endpoint repair --harness claude,codex,gemini
python3 -m json.tool < "$HOME/.gemini/settings.json" > "$BEACON_E2E_RUN/gemini-settings.pretty.json" || true
beacon endpoint status --json > "$BEACON_E2E_RUN/status-before-gemini.json"
```

Runtime action:

```bash
export BEACON_E2E_GEMINI_MARKER="Beacon E2E Gemini prompt $(date +%s)"
printf 'Please submit this prompt in Gemini CLI: %s\n' "$BEACON_E2E_GEMINI_MARKER"
```

Optional headless fast path, only when Gemini CLI noninteractive mode and auth are known to work:

```bash
gemini --prompt "$BEACON_E2E_GEMINI_MARKER: answer with exactly the word beacon-ok." \
  > "$BEACON_E2E_RUN/gemini-headless.out" \
  2> "$BEACON_E2E_RUN/gemini-headless.err"
```

If the installed Gemini CLI uses a different noninteractive command or flag set, document the discrepancy and use the current recommended command from `gemini --help`.

Validation:

```bash
rg "gemini|$BEACON_E2E_GEMINI_MARKER|beacon-ok" "$HOME/.beacon/endpoint/logs/runtime.jsonl" \
  > "$BEACON_E2E_RUN/gemini-runtime-events.txt"
```

Acceptance criteria:

- `~/.gemini/settings.json` contains Beacon-managed local OTLP telemetry settings with `target` set to `local`, `otlpProtocol` set to `grpc`, `useCollector` set to `true`, and no `outfile`.
- The user confirms the prompt was submitted, or the optional noninteractive command exits successfully.
- Runtime log receives Gemini OTLP-derived events with `harness.name=gemini_cli`.
- Prompt content appears only when retention mode permits it.
- If no events appear, check for project Gemini settings or `GEMINI_TELEMETRY_*` environment variables overriding the user settings.

Repo areas to research later:

- [`cli/beacon/internal/endpoint/harness/gemini.go`](cli/beacon/internal/endpoint/harness/gemini.go)
- [`cli/beacon/internal/endpoint/lifecycle/lifecycle.go`](cli/beacon/internal/endpoint/lifecycle/lifecycle.go)
- [`collector-builder/exporter/beaconjsonexporter/exporter.go`](collector-builder/exporter/beaconjsonexporter/exporter.go)

## OpenCode Integration Test
Install OpenCode hooks, then ask the user to submit a unique prompt in OpenCode:

```bash
beacon endpoint hooks install --harness opencode
beacon endpoint hooks status --harness opencode
beacon endpoint hooks status --harness opencode --json > "$BEACON_E2E_RUN/opencode-hooks-status.json"
```

Runtime action:

```bash
export BEACON_E2E_OPENCODE_MARKER="Beacon E2E OpenCode prompt $(date +%s)"
printf 'Please submit this prompt in OpenCode: %s\n' "$BEACON_E2E_OPENCODE_MARKER"
```

Optional headless fast path, only when OpenCode noninteractive mode is known to work:

```bash
opencode run --format json "$BEACON_E2E_OPENCODE_MARKER: answer with exactly the word beacon-ok." \
  > "$BEACON_E2E_RUN/opencode-headless.json" \
  2> "$BEACON_E2E_RUN/opencode-headless.err"
```

If the installed OpenCode CLI uses a different noninteractive command or flag set, document the discrepancy and use the current recommended command from `opencode --help`.

Validation:

```bash
rg "OpenCode|opencode|$BEACON_E2E_OPENCODE_MARKER|beacon-ok" "$HOME/.beacon/endpoint/logs/runtime.jsonl" \
  > "$BEACON_E2E_RUN/opencode-runtime-events.txt"
```

Acceptance criteria:

- Hook status reports OpenCode plugin installed.
- The user confirms the prompt was submitted, or the optional noninteractive command exits successfully.
- Runtime log contains OpenCode chat/session activity with the marker when content retention is `full`.
- OpenCode remains usable if Beacon hook execution fails; if the prompt succeeds but no event appears, record this as an integration or hook-coverage finding.

Repo areas to research later:

- [`cli/beacon/internal/endpoint/hooks/opencode.go`](cli/beacon/internal/endpoint/hooks/opencode.go)
- [`cli/beacon/internal/endpoint/hooks/assets/opencode/embed.go`](cli/beacon/internal/endpoint/hooks/assets/opencode/embed.go)
- [`cli/beacon-hooks/cmd/opencode_event.go`](cli/beacon-hooks/cmd/opencode_event.go)

## Local Elastic Integration Test
Generate and run the local Elastic content pack from the Homebrew CLI:

```bash
beacon endpoint elastic install-pack --output "$BEACON_E2E_RUN/beacon-elastic-pack"
beacon endpoint elastic up --pack-dir "$BEACON_E2E_RUN/beacon-elastic-pack"
```

Wait for services, then validate Elasticsearch directly before using Kibana:

```bash
curl -fsS http://localhost:9200 >/dev/null
curl -fsS "http://localhost:9200/logs-beacon.endpoint-*/_search?q=beacon.product:endpoint-agent&size=5" > "$BEACON_E2E_RUN/elastic-search-product.json"
curl -fsS "http://localhost:9200/logs-beacon.endpoint-*/_search?q=beacon.prompt.text:%22Beacon%20E2E%22&size=20" > "$BEACON_E2E_RUN/elastic-search-marker.json"
curl -fsS "http://localhost:9200/logs-beacon.endpoint-*/_search?q=beacon.harness.name:cursor&size=5" > "$BEACON_E2E_RUN/elastic-search-cursor.json"
```

Kibana validation:

- Open [http://localhost:5601](http://localhost:5601).
- Go to Discover.
- Select `Beacon Endpoint Events`.
- Search for `beacon.prompt.text:"Beacon E2E"` and for each runtime field value: `beacon.harness.name:cursor`, `beacon.harness.name:claude_code`, `beacon.harness.name:codex_cli`, `beacon.harness.name:opencode`.
- Save screenshots or notes if the data view is missing, fields are poorly named, timestamps are wrong, or events are hard to find.

Acceptance criteria:

- `elastic up` creates or uses the content pack successfully.
- Elasticsearch, Kibana, and Filebeat start on loopback ports.
- Events from all successful runtime tests are searchable in Elasticsearch.
- Kibana has a usable Beacon data view and Discover workflow.
- Generated pack points Filebeat at the same runtime log Beacon writes.

Repo areas to research later:

- [`cli/beacon/internal/endpoint/elastic/elastic.go`](cli/beacon/internal/endpoint/elastic/elastic.go)
- [`cli/beacon/internal/endpoint/elastic/pack/README.md`](cli/beacon/internal/endpoint/elastic/pack/README.md)
- [`cli/beacon/internal/endpoint/elastic/pack/docker-compose.yml`](cli/beacon/internal/endpoint/elastic/pack/docker-compose.yml)

## Notes Review And Fix Planning
After all tests, review `beacon-installation-notes.md` and group findings:

- Docs fixes: docs command, prerequisite, expected-output, broken-link, or troubleshooting updates.
- CLI fixes: command flags, status clarity, install/repair behavior, error messages, or path mismatch.
- Hook fixes: Cursor/OpenCode install/status, hook payload parsing, runtime resilience, or content retention.
- Exporter fixes: Claude/Codex OTLP normalization, filtering, field mapping, or event schema consistency.
- Elastic fixes: generated pack, Docker Compose stack, Filebeat config, index templates, ingest pipeline, Kibana assets, or docs.

For each confirmed bug, research the smallest relevant code path and add or update focused tests before implementation. Use existing validation patterns from:

- [`cli/beacon/internal/endpoint/lifecycle/lifecycle_test.go`](cli/beacon/internal/endpoint/lifecycle/lifecycle_test.go)
- [`cli/beacon/internal/endpoint/harness/harness_test.go`](cli/beacon/internal/endpoint/harness/harness_test.go)
- [`cli/beacon/internal/endpoint/hooks/cursor_test.go`](cli/beacon/internal/endpoint/hooks/cursor_test.go)
- [`cli/beacon/internal/endpoint/hooks/opencode_test.go`](cli/beacon/internal/endpoint/hooks/opencode_test.go)
- [`cli/beacon-hooks/cmd/opencode_event_test.go`](cli/beacon-hooks/cmd/opencode_event_test.go)
- [`collector-builder/exporter/beaconjsonexporter/exporter_test.go`](collector-builder/exporter/beaconjsonexporter/exporter_test.go)
- [`cli/beacon/internal/endpoint/elastic/elastic_test.go`](cli/beacon/internal/endpoint/elastic/elastic_test.go)

## Optional Full Teardown
Only run teardown if the user explicitly asks to remove the test state. Preserve notes and evidence under `$BEACON_E2E_RUN`.

```bash
beacon endpoint elastic down --pack-dir "$BEACON_E2E_RUN/beacon-elastic-pack" || true
beacon endpoint uninstall --keep-logs || true
```

If the user also asks to roll back runtime configuration, restore the timestamped backups manually and verify each restored file before continuing. Do not restore backups by default.

## Final Release Gate
Before considering fixes ready, run the focused tests for touched areas plus the standard release gates:

```bash
cd cli/beacon && go test ./...
cd cli/beacon && go test -race ./internal/endpoint/...
cd cli/beacon-hooks && go test ./...
cd collector-builder/exporter/beaconjsonexporter && go test ./...
sh packaging/macos/test-endpoint-scripts.sh
```

For any fix affecting install, hooks, collector packaging, or Elastic assets, rerun the affected slice of this runbook with a freshly installed Homebrew release or a release-candidate archive, and append the retest result to `beacon-installation-notes.md`.
