# beacon

Public CLI for Beacon Endpoint Agent.

## Build

```bash
make build
```

## Common Commands

```bash
./beacon endpoint install
./beacon endpoint status --json
./beacon endpoint discover --json
./beacon endpoint repair
./beacon endpoint dashboard
./beacon endpoint uninstall --keep-logs
```

Endpoint commands use per-user paths by default so hook and OTLP telemetry share
`~/.beacon/endpoint/logs/runtime.jsonl`. Use `--system` for root-managed
deployment paths.

Add optional Splunk HEC forwarding during install or repair:

```bash
./beacon endpoint install \
  --splunk-hec-endpoint https://splunk.example:8088/services/collector \
  --splunk-hec-token "$SPLUNK_HEC_TOKEN" \
  --splunk-index beacon
```

The local JSONL runtime log remains enabled when Splunk forwarding is
configured.

## Cloud Agent Telemetry

Use `beacon cloud` helpers to configure provider-managed cloud agent sandboxes.
Claude Code on the web and Cursor cloud agents can forward Beacon JSONL to
customer-managed GCS or S3:

```bash
./beacon cloud gcs setup \
  --project "$GCP_PROJECT" \
  --bucket "$BEACON_CLOUD_GCS_BUCKET" \
  --prefix "$BEACON_CLOUD_GCS_PREFIX" \
  --service-account beacon-cloud-trace-uploader \
  --apply \
  --print-env
```

Copy the printed `BEACON_CLOUD_GCS_BUCKET`,
`BEACON_CLOUD_GCS_PREFIX`, and `BEACON_CLOUD_GCS_CREDENTIALS_B64`
values into the cloud agent environment.

For S3, use `beacon cloud s3 setup --apply --print-env` and copy the printed
`BEACON_CLOUD_S3_*` and AWS credential values instead.

For both destinations, also set:

```bash
BEACON_ORIGIN=cloud
BEACON_RUN_PROVIDER=claude_code_web # or cursor_cloud
BEACON_RUN_EPHEMERAL=true
BEACON_CLOUD_USER_ID_HASH=<stable-user-or-test-id>
```

For Cursor Cloud Agents, keep the repository-owned setup in `.cursor/`:

```text
.cursor/environment.json
.cursor/install.sh
.cursor/start.sh
```

The install script builds the current branch by default, or downloads a release
when `BEACON_VERSION` is set, then merges Beacon commands into
`.cursor/hooks.json` during environment setup. It defaults to safe hook coverage
while testing forwarding and skips shell/edit-specific hooks; set
`BEACON_CURSOR_CLOUD_FULL_HOOKS=1` to enable the full Cursor Cloud hook set.

For Claude Code on the web, generate the setup script for a Beacon release and
paste it into the provider's cloud setup field:

```bash
./beacon cloud claude-web print-setup --version vX.Y.Z
```

The setup script installs `beacon-hooks` into the cloud sandbox and uploads a
gzip-compressed `/tmp/beacon/runtime.jsonl` snapshot to object storage. Claude web writes
`.claude/settings.local.json` inside the sandbox clone. Cursor cloud uses
`.cursor/environment.json` to install Beacon and generate project-level
`.cursor/hooks.json` because Cursor cloud only runs repository project hooks.

```text
<prefix>/runtime/date=YYYY-MM-DD/<timestamp>-claude_code_web-<claude-session-id>.jsonl.gz
<prefix>/runtime/date=YYYY-MM-DD/<timestamp>-cursor_cloud-<generated-or-explicit-run-id>.jsonl.gz
```

Cloud network access must allow `oauth2.googleapis.com` and
`storage.googleapis.com`. Cursor cloud currently supports command hooks for tool
activity, shell execution, file reads and edits, subagents, and compaction
events; it does not currently run session start, session end, prompt submit, or
stop hooks in cloud agents. If you are testing unreleased Beacon changes, clone
and build the feature branch in the setup script instead of using
`print-setup --version`.

Add optional Falcon LogScale HEC forwarding during install or repair:

```bash
./beacon endpoint repair \
  --falcon-hec-endpoint "$LOGSCALE_URL/api/v1/ingest/hec" \
  --falcon-hec-token "$LOGSCALE_INGEST_TOKEN" \
  --falcon-source beacon-endpoint-agent \
  --falcon-sourcetype json
```

Beacon sends LogScale HEC requests with `Authorization: Bearer <ingest token>`.
The HEC `event` value is the normalized Beacon event object with an ISO
`@timestamp` nested inside it for LogScale's built-in JSON parser. The optional
`--falcon-index` flag maps to a LogScale repository and is usually only needed
with organization or system multi-repository ingest tokens; repository-scoped
ingest tokens already select the target repository.

## Dashboard

```bash
./beacon endpoint dashboard
./beacon endpoint dashboard --addr 127.0.0.1:8765
./beacon endpoint dashboard --open
```

The dashboard reads the configured runtime JSONL log and serves a local,
read-only view on loopback. It has no external network dependency during normal
use.

Use the search bar to find events by action, command, file path, MCP tool,
approval decision, repository, session, or message. Quick filters surface
high-severity events, failures, approvals, MCP activity, file changes, and events
that may need review.

## Agent Telemetry in CI

Use `beacon ci exec` to collect Claude Code or Codex OpenTelemetry for a single
CI command without installing a persistent endpoint service or changing local
developer configuration:

```bash
./beacon ci exec -- claude --print "Summarize this repository in one sentence"
```

For actions or workflows that launch an agent internally, use CI session mode:

```bash
./beacon ci start --harness claude,codex
# run one or more agent CI steps here
./beacon ci finish --min-events 1
```

Beacon starts the bundled `beacon-otelcol` in the foreground, writes a
job-scoped collector config under `$RUNNER_TEMP/beacon`, configures supported
agent harnesses to export OTLP to the local collector, validates that structured
Beacon events reached the runtime JSONL log, and writes normalized JSONL for
artifact upload or customer-managed forwarding.

`ci exec` injects telemetry only into its child process. `ci start` writes
session exports to `$GITHUB_ENV` when run through the composite action, which
lets later `uses:` actions inherit Claude Code environment variables or a
generated Codex `CODEX_HOME`.

Validate an existing CI artifact explicitly:

```bash
./beacon ci validate \
  --log-path "$RUNNER_TEMP/beacon/runtime.jsonl" \
  --min-events 1
```

Captured events carry the GitHub Actions run context (`origin`, `run.provider`,
`run.repository`, `run.branch`, `run.run_id`, `run.run_attempt`, `run.workflow`,
`run.job`, `run.event_name`, `run.commit`, `run.actor`, and `run.pr_number` on
pull-request events) so they can be correlated per-workflow and per-PR
downstream.

Upload the log from GitHub Actions for customer-controlled retention:

```yaml
- name: Run Claude with Beacon telemetry
  run: beacon ci exec -- claude --print "Summarize this repository"

- name: Upload Beacon telemetry
  if: always()
  uses: actions/upload-artifact@v4
  with:
    name: beacon-runtime-log
    path: ${{ runner.temp }}/beacon/runtime.jsonl
```

Upload only the `runtime.jsonl` file (as above), not the whole
`${{ runner.temp }}/beacon` directory. When forwarding is configured, Beacon
keeps the SIEM token in the runner environment and writes only collector
environment references into `otelcol.yaml`; excluding the collector config from
artifacts still avoids exposing forwarding endpoints or local collector details.

The JSONL artifact is the universal CI export contract. Existing destination
packs can consume the same file after it is downloaded or copied into the
customer-managed forwarder path:

| Destination | CI export path |
| --- | --- |
| Wazuh | Download the artifact and tail it with the Wazuh localfile pack. |
| Elastic | Download the artifact and ingest it with Filebeat or Elastic Agent pack assets. |
| Datadog | Download the artifact and tail it with the Datadog Agent log collection pack. |
| Sumo Logic | Download the artifact for Vector-based forwarding, or use `--upload` when object storage is the handoff point. |
| Rapid7 | Download the artifact for Vector/webhook forwarding. |
| S3 | Use direct `--upload s3` in CI, or upload/download the artifact manually. |
| CloudWatch Logs | Download the artifact and forward with the Vector CloudWatch pack. |
| GCS | Use direct `--upload gcs` in CI, or upload/download the artifact manually. |
| Sentinel | Download the artifact and ingest with the Azure Monitor Agent/DCR pack. |
| Splunk | Use direct `--forward splunk`, or ingest the JSONL artifact with an existing Splunk pipeline. |
| Falcon LogScale | Use direct `--forward falcon`, or ingest the JSONL artifact with an existing pipeline. |

### Forwarding to a customer-managed SIEM

Because CI runners are ephemeral, the local JSONL is destroyed when the runner
is torn down. In addition to (or instead of) uploading an artifact, `ci exec`
can forward events from its ephemeral collector to a customer-managed Splunk or
CrowdStrike Falcon LogScale HEC endpoint before teardown:

```bash
export BEACON_CI_SPLUNK_HEC_TOKEN="$SPLUNK_HEC_TOKEN"   # from CI secrets
beacon ci exec \
  --forward splunk \
  --forward-endpoint "https://splunk.example:8088/services/collector" \
  -- claude --print "Summarize this repository"
```

- The token is read from the environment only
  (`BEACON_CI_SPLUNK_HEC_TOKEN` / `BEACON_CI_FALCON_HEC_TOKEN`) and is never
  accepted as a flag, so it does not appear in CI process listings. The
  endpoint may be passed via `--forward-endpoint` or
  `BEACON_CI_SPLUNK_HEC_ENDPOINT` / `BEACON_CI_FALCON_HEC_ENDPOINT`.
- Forwarding is best-effort: a SIEM delivery failure does not fail the job, and
  Beacon still writes the local JSONL.
- Beacon remains a local JSONL producer; egress goes only to infrastructure the
  customer already operates.

### Uploading CI JSONL to object storage

For CI runners that should hand off telemetry through object storage, `ci exec`
can upload the completed `runtime.jsonl` after telemetry validation:

```bash
export BEACON_CI_S3_BUCKET="my-beacon-telemetry"
export BEACON_CI_S3_PREFIX="github-actions"
beacon ci exec --upload s3 -- claude --print "Summarize this repository"
```

```bash
export BEACON_CI_GCS_BUCKET="my-beacon-telemetry"
export BEACON_CI_GCS_PREFIX="github-actions"
beacon ci exec --upload gcs -- claude --print "Summarize this repository"
```

- S3 upload uses `aws s3 cp` and the standard AWS credential provider chain.
- GCS upload uses `gcloud storage cp` when available, falling back to `gsutil cp`.
- Beacon strips common AWS and Google credential environment variables from the
  child agent process, while keeping them available to the post-run uploader.
- Object keys default to
  `<prefix>/<repository>/<run_id>/<run_attempt>/runtime.jsonl` in GitHub Actions,
  with a timestamp fallback outside GitHub Actions.

By default `ci exec` fails the step when no Beacon events reach the runtime log,
which surfaces a broken telemetry pipeline. Pass `--require-telemetry=false` to
downgrade that to a warning when you do not want telemetry health to gate the
build.

### GitHub Action

A composite action wraps binary resolution, `ci exec`, session
`start`/`finish`, and artifact upload. For a direct command, use the default
`exec` mode:

```yaml
- name: Run Claude with Beacon telemetry
  uses: asymptote-labs/agent-beacon@<tag>
  with:
    command: claude --print "Summarize the changes in this pull request"
```

The action downloads a pinned Beacon release and verifies it against the
release `checksums.txt` before running. When the action is invoked from a
version tag, that tag is used as the default Beacon CLI release; set `version`
explicitly or use `binary-path` when testing from a branch or local checkout.
It uploads the runtime JSONL log as an artifact by default.

To wrap a third-party agent action, split the action into `start` and `finish`
steps:

```yaml
- name: Start Beacon telemetry
  uses: asymptote-labs/agent-beacon@<tag>
  with:
    mode: start
    harnesses: claude,codex

- name: Run agent review action
  uses: vendor/agent-review-action@main

- name: Finish Beacon telemetry
  if: always()
  uses: asymptote-labs/agent-beacon@<tag>
  with:
    mode: finish
    min-events: "1"
```

For Codex-based actions that accept a custom home directory, pass the generated
Codex home output:

```yaml
- name: Start Beacon telemetry
  id: beacon
  uses: asymptote-labs/agent-beacon@<tag>
  with:
    mode: start
    harnesses: codex

- uses: openai/codex-action@main
  with:
    openai-api-key: ${{ secrets.OPENAI_API_KEY }}
    codex-home: ${{ steps.beacon.outputs.codex-home }}
    prompt: "Review this pull request"
```

To forward to a customer-managed SIEM, set `forward`/`forward-endpoint` on
`exec` or `start` and provide the token through a job- or workflow-level
environment variable from a secret:

```yaml
jobs:
  claude-telemetry:
    runs-on: ubuntu-latest
    env:
      BEACON_CI_SPLUNK_HEC_TOKEN: ${{ secrets.SPLUNK_HEC_TOKEN }}
    steps:
      - uses: asymptote-labs/agent-beacon@<tag>
        with:
          command: claude --print "Summarize this repository"
          forward: splunk
          forward-endpoint: https://splunk.example:8088/services/collector
```

To upload the completed JSONL artifact to object storage, configure cloud
credentials at the job level and set `upload`:

```yaml
- uses: aws-actions/configure-aws-credentials@v4
  with:
    role-to-assume: ${{ secrets.BEACON_TELEMETRY_ROLE_ARN }}
    aws-region: us-east-1

- name: Run Claude with Beacon telemetry
  uses: asymptote-labs/agent-beacon@<tag>
  env:
    BEACON_CI_S3_BUCKET: my-beacon-telemetry
    BEACON_CI_S3_PREFIX: github-actions
  with:
    command: claude --print "Summarize this repository"
    upload: s3
```

See [`examples/github-actions/claude-code-telemetry.yml`](../../examples/github-actions/claude-code-telemetry.yml),
[`examples/github-actions/claude-security-review-session.yml`](../../examples/github-actions/claude-security-review-session.yml),
[`examples/github-actions/codex-action-session.yml`](../../examples/github-actions/codex-action-session.yml),
[`examples/github-actions/s3-upload-telemetry.yml`](../../examples/github-actions/s3-upload-telemetry.yml),
[`examples/github-actions/gcs-upload-telemetry.yml`](../../examples/github-actions/gcs-upload-telemetry.yml),
[`examples/github-actions/splunk-forward-telemetry.yml`](../../examples/github-actions/splunk-forward-telemetry.yml),
and [`examples/github-actions/falcon-forward-telemetry.yml`](../../examples/github-actions/falcon-forward-telemetry.yml)
for complete reference workflows.

## Wazuh

```bash
./beacon endpoint wazuh print-config
./beacon endpoint wazuh install-pack --output ./beacon-wazuh
./beacon endpoint wazuh validate
```

## Sumo Logic

```bash
./beacon endpoint sumo print-config
./beacon endpoint sumo install-pack --output ./beacon-sumo-pack
./beacon endpoint sumo validate
```

The Sumo pack keeps Beacon as a local JSONL producer and documents forwarding
`runtime.jsonl` into a customer-managed Sumo Hosted Collector HTTP Logs &
Metrics Source. Use a tailing forwarder for production so offsets are
checkpointed and the whole file is not repeatedly uploaded.

## Rapid7 InsightIDR

```bash
./beacon endpoint rapid7 print-config
./beacon endpoint rapid7 install-pack --output ./beacon-rapid7-pack
./beacon endpoint rapid7 validate
```

The Rapid7 pack keeps Beacon as a local JSONL producer and documents forwarding
`runtime.jsonl` into a Rapid7 InsightIDR Custom Logs webhook as NDJSON. Store the
webhook URL in your customer-managed shipper or deployment tooling, not in
Beacon endpoint configuration.

## AWS S3

```bash
./beacon endpoint s3 print-config
./beacon endpoint s3 install-pack --output ./beacon-s3-pack
./beacon endpoint s3 validate
```

The S3 pack keeps Beacon as a local JSONL producer and documents forwarding
`runtime.jsonl` and the sibling `inventory_state.jsonl` into separate
`runtime/` and `inventory/` S3 prefixes with a customer-managed Vector host
agent. Store AWS credentials, profiles, IAM roles, bucket policies, lifecycle
rules, and encryption settings in AWS, Vector, or deployment tooling, not in
Beacon endpoint configuration.

## AWS CloudWatch Logs

```bash
./beacon endpoint cloudwatch print-config
./beacon endpoint cloudwatch install-pack --output ./beacon-cloudwatch-pack
./beacon endpoint cloudwatch validate
```

The AWS CloudWatch Logs pack keeps Beacon as a local JSONL producer and
documents forwarding `runtime.jsonl` into a CloudWatch Logs log group with a
customer-managed Vector host agent. Store AWS credentials, profiles, IAM roles,
log group retention, stream naming, and encryption settings in AWS, Vector, or
deployment tooling, not in Beacon endpoint configuration.

## Google Cloud Storage

```bash
./beacon endpoint gcs print-config
./beacon endpoint gcs install-pack --output ./beacon-gcs-pack
./beacon endpoint gcs validate
```

The GCS pack keeps Beacon as a local JSONL producer and documents forwarding
`runtime.jsonl` and the sibling `inventory_state.jsonl` into separate
`runtime/` and `inventory/` folders below one root GCS prefix.

The macOS package includes
`/opt/beacon/jamf/claude/gcs/{install-forwarder.sh,run-forwarder.sh,repair-hooks-and-forwarder.sh}`
and runs bundled Vector as `com.beacon.endpoint.gcs-forwarder`. Vector `0.56`
supports service-account JSON and GCE metadata credentials; on a normal Mac,
deliver the JSON through MDM or secret tooling to a root-owned path outside
Beacon-managed directories and reference it with
`GOOGLE_APPLICATION_CREDENTIALS`. Do not rely on interactive `gcloud` ADC or
Workload Identity Federation for the launchd service. An endpoint writer with
`roles/storage.objectCreator` cannot list or read objects, so use a separate
reader identity for validation.

## Microsoft Sentinel

```bash
./beacon endpoint sentinel print-config
./beacon endpoint sentinel install-pack --output ./beacon-sentinel-pack
./beacon endpoint sentinel validate
```

The Sentinel pack keeps Beacon as a local JSONL producer and documents
forwarding `runtime.jsonl` through Azure Monitor Agent, a Data Collection Rule,
and a `BeaconRuntime_CL` custom Log Analytics table. Store Azure workspace,
DCR, endpoint, and credential details in Azure or customer-managed deployment
tooling, not in Beacon endpoint configuration.

## Optional Integrations

```bash
./beacon endpoint hooks install --harness cursor
./beacon endpoint hooks status --harness cursor

./beacon endpoint hooks install --harness claude --level user
./beacon endpoint hooks status --harness claude

./beacon endpoint hooks install --harness opencode
./beacon endpoint hooks status --harness opencode

./beacon endpoint hooks install --harness grok
./beacon endpoint hooks status --harness grok

./beacon endpoint hooks install --harness hermes
./beacon endpoint hooks status --harness hermes

./beacon endpoint hooks install --harness devin-cli --level project
./beacon endpoint hooks status --harness devin-cli --level project
./beacon endpoint hooks install --harness devin-desktop --level user
./beacon endpoint hooks status --harness devin-desktop --level user

./beacon endpoint install --harness claude,codex,devin-cli,devin-desktop

./beacon endpoint integrations claude-cowork setup --endpoint https://collector.example.com --open
./beacon endpoint integrations claude-cowork setup --ngrok --open
./beacon endpoint integrations claude-cowork validate --since 10m

./beacon endpoint integrations openclaw print-config
./beacon endpoint integrations openclaw status
./beacon endpoint integrations openclaw validate --since 10m

./beacon endpoint integrations vscode setup
./beacon endpoint integrations vscode status
./beacon endpoint integrations vscode validate --since 10m
./beacon endpoint hooks install --harness vscode --level project
```

The opencode integration installs Beacon's owned local plugin at
`~/.config/opencode/plugins/beacon.ts`. The plugin is a thin adapter that sends
raw opencode hook payloads to Beacon's Go hook binary; Beacon handles
normalization, retention, redaction, and JSONL output locally. For local
troubleshooting, set `BEACON_OPENCODE_DEBUG=1` in the environment that launches
opencode to emit best-effort plugin debug logs.

Claude Code supports two Beacon setup paths. `beacon endpoint install --harness claude`
configures Claude Code's local OpenTelemetry export to Beacon's collector.
`beacon endpoint hooks install --harness claude` writes command hooks into
`~/.claude/settings.json` or `.claude/settings.json` and sends normalized events
directly to the local runtime JSONL log. The hook path is useful when an
Anthropic organization policy blocks third-party telemetry export. Claude Code
hooks are intentionally not included in `beacon endpoint hooks install --all` in
this release; install them explicitly with `--harness claude`.

The Grok Build integration writes Beacon's owned local hook file at
`~/.grok/hooks/beacon-endpoint.json` for user-level installs or `.grok/hooks/beacon-endpoint.json`
for project-level installs. Project hooks require trusting the project in Grok
with `/hooks-trust` before they execute.

The Hermes Agent integration writes shell-hook entries into
`~/.hermes/config.yaml`. Hermes prompts for first-use consent for each
`(event, command)` pair; for non-interactive gateway, cron, or CI runs, set
`HERMES_ACCEPT_HOOKS=1`, start Hermes with `--accept-hooks`, or configure
`hooks_auto_accept: true` in the Hermes config.

The Devin CLI integration writes Claude-compatible command hooks for Devin for
Terminal. `devin` remains a legacy alias for `devin-cli`. Project-level installs
use `.devin/hooks.v1.json`; user-level installs use
`~/.config/devin/config.json` under the `hooks` key. The hooks invoke Beacon's
local Go hook binary and write normalized prompt, tool, command, file, approval,
and session events to the configured runtime JSONL log.

Devin Desktop is exposed separately as `devin-desktop` and uses Devin
Desktop-compatible Cascade/Windsurf hooks. User-level installs write
`~/.codeium/windsurf/hooks.json`; project-level installs write
`.windsurf/hooks.json`, which may also affect Windsurf/Cascade in that
workspace. Beacon installs visibility-only hooks for prompt submission, file
writes, command execution, MCP tool use, and file reads; the hooks do not block
or enforce policy. After installation, generate a Devin Desktop event and check
the Beacon runtime log to validate that the Desktop app executed the hook file.
The main `beacon endpoint install --harness ...` path also handles hook-backed
Devin targets, so `--harness claude,codex,devin-cli,devin-desktop` configures
OTLP-backed Claude/Codex telemetry and Devin hook telemetry in one flow.

Claude Cowork monitoring is configured in the Claude admin console at
`https://claude.ai/admin-settings/cowork`. The OTLP endpoint must be reachable
by Claude Cowork, so use a durable public HTTPS Collector endpoint for ongoing
monitoring. The `--ngrok` mode is for short-lived local testing and prints an
authenticated tunnel URL plus the matching `Authorization` header.

OpenClaw Gateway monitoring is configured in OpenClaw's local Gateway config
with the `diagnostics-otel` plugin enabled. Beacon prints a local OTLP/HTTP
configuration that points OpenClaw at the endpoint collector. OpenClaw does not
export raw prompt, response, tool, or system-prompt content unless
`diagnostics.otel.captureContent.*` is explicitly enabled.

VS Code Copilot monitoring is configured in VS Code settings and exports
OTLP/HTTP to Beacon's local collector. For a first-time local setup, install the
endpoint collector with the VS Code harness, reload VS Code, and validate recent
activity:

```bash
./beacon endpoint install --user --harness vscode
./beacon endpoint integrations vscode validate --user --since 10m
```

VS Code hook support is optional and depends on the `Chat: Use Hooks` setting,
which may be managed by the organization. When enabled, project-level hooks use
`.github/hooks/beacon.json`:

```bash
cd /path/to/workspace
beacon endpoint hooks install --harness vscode --level project --user
```

OTel-derived events use `harness.name=vscode_copilot`; hook-derived events use
`harness.name=vscode`.

## Test

```bash
go test ./...
go test -race ./internal/endpoint/...
```
