<p align="center">
  <img src="images/beacon-hero.png" alt="Beacon" width="860">
</p>

<h1 align="center">Asymptote Lab's Agent Beacon</h1>

<p align="center">
  <a href="https://github.com/asymptote-labs/agent-beacon/releases"><img src="https://img.shields.io/github/v/release/asymptote-labs/agent-beacon" alt="GitHub release"></a>
  <a href="https://github.com/asymptote-labs/homebrew-tap"><img src="https://img.shields.io/badge/homebrew-beacon-fbb040?logo=homebrew" alt="Homebrew"></a>
  <a href="https://github.com/asymptote-labs/agent-beacon/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/asymptote-labs/agent-beacon/ci.yml" alt="GitHub Workflow Status"></a>
  <a href="https://github.com/asymptote-labs/agent-beacon/blob/main/LICENSE"><img src="https://img.shields.io/github/license/asymptote-labs/agent-beacon" alt="MIT license"></a>
  <a href="https://docs.asymptotelabs.ai"><img src="https://img.shields.io/badge/docs-asymptotelabs.ai-0369a1" alt="Docs"></a>
  <a href="https://discord.gg/zdNChS2fBu"><img src="https://img.shields.io/badge/discord-community-5865F2?logo=discord&logoColor=white" alt="Discord"></a>
</p>

<p align="center">
  <strong>Unified telemetry for AI agents, wherever they run.</strong>
</p>

<p align="center">
  <a href="https://docs.asymptotelabs.ai">Docs</a>
  ·
  <a href="https://discord.gg/zdNChS2fBu">Discord</a>
  ·
  <a href="https://docs.asymptotelabs.ai/cli/installation">Install</a>
  ·
  <a href="https://docs.asymptotelabs.ai/cli/security-it-teams">For Security & IT Teams</a>
  ·
  <a href="https://docs.asymptotelabs.ai/cli/dashboard">Dashboard</a>
  ·
  <a href="https://docs.asymptotelabs.ai/cli/command-reference">Commands</a>
</p>

## What is Agent Beacon

Agent Beacon is the world's first [open-source telemetry layer](https://justindsouza.substack.com/p/introducing-beacon-endpoint-telemetry)
for AI agents. Today there is no way to see all of your agent activity in one
place: it is fragmented across runtimes and environments, from laptops to CI to
the browser to the cloud. Beacon supports [21+ local runtimes](#local-agents) and
covers [every surface agents run on](#supported-surfaces), normalizing all of it
into [one data model](https://docs.asymptotelabs.ai/cli/event-schema) built on the
[OpenTelemetry GenAI standard](https://opentelemetry.io/docs/specs/semconv/gen-ai/).

It ships as a single [endpoint binary](https://docs.asymptotelabs.ai/cli/endpoint)
plus a [TypeScript SDK](#cloud-agents), installs in
[one command](https://docs.asymptotelabs.ai/cli/installation) or across a fleet
through [MDM](#mdm-deployment), and forwards to the
[major SIEM and log destinations](#output-destinations).

Learn more in the [Agent Beacon documentation](https://docs.asymptotelabs.ai).

## High-Level Architecture

Beacon captures activity where each agent actually runs, then normalizes it on the
endpoint into a single OpenTelemetry-based event model. That one stream is what the
local dashboard, JSONL retention, and every SIEM destination read from.

<p align="center">
  <img src="images/beacon-architecture.png" alt="Beacon endpoint architecture" width="860">
</p>

- **Agent runtime layer** — hooks, OpenTelemetry sources, CI wrappers, SDKs, and an
  optional browser extension capture supported agent activity.
- **Beacon endpoint layer** — local processing normalizes events, applies retention
  and redaction settings, and writes durable endpoint telemetry.
- **Output layer** — inspect events in the local dashboard, retain JSONL, or forward
  records into the [major enterprise-grade SIEMs](#output-destinations).

Collection, processing, and inspection stay local by default; the same normalized
event model extends to CI and cloud-agent paths under customer control. See the
[open-source architecture reference](https://docs.asymptotelabs.ai/architecture/architecture)
for the full collection, normalization, storage, and forwarding pipeline, or the
[system architecture overview](https://docs.asymptotelabs.ai/architecture/system-architecture)
to compare it with the managed path.

## Supported Surfaces

### Agent Runtimes

#### Local Agents

| Runtime | Collection path | Telemetry coverage |
| --- | --- | --- |
| [Antigravity CLI](https://docs.asymptotelabs.ai/cli/supported-runtimes-antigravity-cli) | Native hooks | Prompt, pre-tool, post-tool, stop, invocation, command, and file |
| [Claude Code](https://docs.asymptotelabs.ai/cli/supported-runtimes-claude-code) | Local OTLP export plus optional hooks | Prompt, command, tool, file, approval, API/model lifecycle, MCP connection, subagent, and session |
| [Claude Cowork](https://docs.asymptotelabs.ai/cli/supported-runtimes-claude-cowork) | Admin-configured OTLP | Prompt, command, tool, and file |
| [Cline](https://docs.asymptotelabs.ai/cli/supported-runtimes-cline) | Managed plugin hooks | Prompts, task lifecycle and errors, tool lifecycle and results, commands with exit codes, file reads/edits with diffs, MCP activity, and token usage/cost |
| [Codex CLI](https://docs.asymptotelabs.ai/cli/supported-runtimes-codex-cli) | Local OTLP plus a session identity hook | Session, prompt, approval, tool results, and per-user/session/model turn token usage |
| [Cursor](https://docs.asymptotelabs.ai/cli/supported-runtimes-cursor) | Native hooks | Prompt, tool, shell command, MCP-like activity, approval, and file edits |
| [Devin CLI](https://docs.asymptotelabs.ai/cli/supported-runtimes-devin) | Native hooks | Session, prompt, pre-tool, post-tool, permission request, stop, session end, approval, and file |
| [Devin Desktop](https://docs.asymptotelabs.ai/cli/supported-runtimes-devin-desktop) | Cascade/Windsurf hooks | Prompt, command, MCP tool, file read, and file write |
| [Factory Droid](https://docs.asymptotelabs.ai/cli/supported-runtimes-factory-droid) | OTLP HTTP plus optional hooks | Session, prompt, write/edit/create tool use, stop, and session end |
| [fx (Vercel Labs)](https://docs.asymptotelabs.ai/runtimes/vercel-fx) | Poll of fx's own session records under `~/.fx/sessions/` through `beacon endpoint fx sync`; events land a turn late and cannot gate a tool call | Session start, prompts, tool calls, commands with exit codes and output, file reads/creates/edits with diffs, MCP tool calls, agent messages, compaction, and token usage/cost. No approval or session-end record, because fx persists neither |
| [Gemini CLI](https://docs.asymptotelabs.ai/cli/supported-runtimes-gemini-cli) | Opt-in local OTLP | Prompts, tool calls, MCP activity, file operations, and approval-related events |
| [GitHub Copilot CLI](https://docs.asymptotelabs.ai/cli/supported-runtimes-github-copilot-cli) | MDM-managed OTLP HTTP | Prompt, session, tool, and approval-like activity |
| [Grok Build](https://docs.asymptotelabs.ai/cli/supported-runtimes-grok-build) | Native hooks | Session, prompt, pre-tool, post-tool, failed tool, stop, session end, command, and file |
| [Hermes Agent](https://docs.asymptotelabs.ai/cli/supported-runtimes-hermes-agent) | Shell hooks | Prompt, observed tool, command, file, approval request and response, session lifecycle, and subagent stop |
| [Oh My Pi](https://docs.asymptotelabs.ai/runtimes/oh-my-pi) | Managed extension hooks | Session lifecycle, prompts, tool lifecycle and results, approval decisions with the session's approval mode, commands including operator `!` and `$`, file reads/writes/edits with diffs, MCP activity, agent reasoning, and token usage/cost |
| [OpenClaw Gateway](https://docs.asymptotelabs.ai/cli/supported-runtimes-openclaw-gateway) | Gateway-configured OTLP/HTTP | OTLP logs, traces, and metrics from the Gateway diagnostics plugin |
| [OpenCode](https://docs.asymptotelabs.ai/cli/supported-runtimes-opencode) | Managed plugin hooks | Prompts, assistant output and reasoning, model usage/cost, tool lifecycle and results, commands, file/web/MCP activity, approvals, and session errors |
| [Pi](https://docs.asymptotelabs.ai/runtimes/pi) | Managed extension hooks | Session lifecycle, prompts, tool lifecycle and results, commands including operator `!`, file reads/writes/edits with diffs, agent reasoning, and token usage/cost |
| [Prime Agent](https://docs.asymptotelabs.ai/runtimes/prime-agent) | Managed extension hooks | Session lifecycle, prompts, tool lifecycle and results, commands including operator `!`, file reads/writes/edits with diffs, agent reasoning, and token usage/cost |
| [Qwen Code](https://docs.asymptotelabs.ai/runtimes/qwen-code) | Native hooks | Session, prompt, pre-tool, post-tool, failed tool, permission request/approval, subagent, stop, session end, command, and file |
| [VS Code](https://docs.asymptotelabs.ai/cli/supported-runtimes-vscode) | Copilot Chat OTel plus optional preview hooks | Copilot session, prompt, model, and tool activity, plus extra lifecycle detail through optional hooks |

#### Browser Chat

| Runtime | Collection path | Telemetry coverage |
| --- | --- | --- |
| [Claude.ai](https://docs.asymptotelabs.ai/runtimes/claude-web) | Managed browser extension over local OTLP | Prompt, assistant response, tool call, and token usage from the claude.ai chat stream |
| [ChatGPT](https://docs.asymptotelabs.ai/runtimes/chatgpt-web) | Managed browser extension over local OTLP | Prompt, assistant response, and tool call from the chatgpt.com chat stream |

#### CI Agents

| Runtime | Collection path | Telemetry coverage |
| --- | --- | --- |
| [CI agent telemetry](https://docs.asymptotelabs.ai/supported-runtimes-claude-code-ci) | Temporary local collector through `beacon ci exec` or `beacon ci start` / `beacon ci finish` | Supported agent prompt, tool, command, file, and run context emitted during the job |

#### Cloud Agents

| Runtime | Collection path | Telemetry coverage |
| --- | --- | --- |
| [Anthropic](https://docs.asymptotelabs.ai/sdk/integrations-anthropic) | OpenLLMetry instrumentation through `@asymptote/sdk` | Anthropic model call spans, errors, and OpenTelemetry attributes |
| [Claude Agent SDK](https://docs.asymptotelabs.ai/sdk/integrations-claude-agent-sdk) | Query wrapper through `Observe.wrapClaudeAgentQuery()` | Query root spans with Beacon-compatible prompt attributes |
| [Claude Code Cloud Agents](https://docs.asymptotelabs.ai/claude-code-cloud-agents) | Cloud sandbox hooks with direct GCS or S3 upload | Session, prompt, tool, command, file, and lifecycle |
| [Cursor Cloud Agents](https://docs.asymptotelabs.ai/cursor-cloud-agents) | Cloud sandbox hooks with direct GCS or S3 upload | Follow-up prompts, tool, shell command, file, subagent, and compaction once project hooks are active |
| [Devin Cloud Agents](https://docs.asymptotelabs.ai/devin-cloud-agents) | Org-wide API poll through `beacon cloud devin pull`, with GCS upload | Session, prompt, agent message, status, pull request, and ACU usage at message level; the autonomous agent runs no in-sandbox hooks |
| [OpenAI](https://docs.asymptotelabs.ai/sdk/integrations-openai) | OpenLLMetry instrumentation through `@asymptote/sdk` | OpenAI model call spans, errors, and OpenTelemetry attributes |
| [Vercel AI SDK](https://docs.asymptotelabs.ai/sdk/integrations-vercel-ai-sdk) | Tracer handoff through `experimental_telemetry` | AI SDK model call and tool spans where telemetry is enabled |

### Output Destinations

Beacon writes endpoint telemetry to local JSONL by default and supports
customer-controlled forwarding into SIEM, log aggregation, and object storage
destinations.

#### Security Information and Event Management (SIEM)

| Destination | Support path |
| --- | --- |
| [CrowdStrike Falcon LogScale HEC](https://docs.asymptotelabs.ai/cli/siem-forwarding-falcon) | Optional endpoint forwarding with LogScale ingest tokens during install or repair |
| [Microsoft Sentinel](https://docs.asymptotelabs.ai/cli/siem-forwarding-microsoft-sentinel) | Azure Monitor Agent and Data Collection Rule content pack over local JSONL |
| [Rapid7 InsightIDR](https://docs.asymptotelabs.ai/cli/siem-forwarding-rapid7) | Custom Logs webhook content pack over local JSONL |
| [Splunk HEC](https://docs.asymptotelabs.ai/cli/siem-forwarding-splunk) | Optional endpoint forwarding during install or repair |
| [Sumo Logic](https://docs.asymptotelabs.ai/cli/siem-forwarding-sumo) | HTTP Logs & Metrics Source content pack over local JSONL |
| [Wazuh](https://docs.asymptotelabs.ai/cli/siem-forwarding-wazuh) | Localfile configuration and Beacon Wazuh content pack |

#### Log Aggregation

| Destination | Support path |
| --- | --- |
| [AWS CloudWatch Logs](https://docs.asymptotelabs.ai/cli/siem-forwarding-cloudwatch) | Vector content pack over local JSONL using customer-managed AWS credentials |
| [Customer-managed log pipelines](https://docs.asymptotelabs.ai/cli/siem-forwarding) | Forwarding from local Beacon JSONL under customer control |
| [Datadog](https://docs.asymptotelabs.ai/cli/siem-forwarding-datadog) | Datadog Agent custom log collection over local JSONL |
| [Elastic](https://docs.asymptotelabs.ai/cli/siem-forwarding-elastic) | Filebeat or Elastic Agent content pack over local JSONL |

#### Object Storage

| Destination | Support path |
| --- | --- |
| [AWS S3](https://docs.asymptotelabs.ai/cli/siem-forwarding-s3) | Vector over endpoint JSONL, CI upload, or direct compressed snapshots from supported cloud agents |
| [Google Cloud Storage](https://docs.asymptotelabs.ai/cli/siem-forwarding-gcs) | Vector and packaged macOS helpers over endpoint JSONL, CI upload, or direct compressed snapshots from supported cloud agents |

#### Local

| Destination | Support path |
| --- | --- |
| [Local JSONL](https://docs.asymptotelabs.ai/cli/local-testing-logs) | Default endpoint log and local dashboard source |

### MDM Deployment

Every version tag publishes native packages that perform the system-mode install
themselves: they register and start the service, write machine-wide configuration, and
point the interactive user's agent runtimes at the local collector. Homebrew and release
archives remain available for CLI installs.

| Platform | Package | Service manager | Notes |
| --- | --- | --- | --- |
| [macOS](https://docs.asymptotelabs.ai/platforms/macos) | Signed, notarized `.pkg` (Apple Silicon) | launchd | [Jamf Pro](https://docs.asymptotelabs.ai/mdm/jamf), [Fleet](https://docs.asymptotelabs.ai/mdm/fleet), and [Rippling](https://docs.asymptotelabs.ai/mdm/rippling) assets; Homebrew for single machines |
| [Linux](https://docs.asymptotelabs.ai/platforms/linux) | `.deb` / `.rpm` (amd64, arm64) | systemd | Supervised fallback without systemd |
| [Windows](https://docs.asymptotelabs.ai/platforms/windows) | `.msi` (x64) | Service Control Manager | Unsigned for now; verify the published `.sha256` |

The macOS package also ships
[GCS forwarder helpers](https://docs.asymptotelabs.ai/mdm/jamf/claude) under
`/opt/beacon/jamf/claude/gcs/` that run bundled Vector as a launchd job.

## Dashboard and Local Detection

Beacon includes a local, read-only [dashboard](https://docs.asymptotelabs.ai/cli/dashboard)
for validating endpoint activity without a hosted backend. It reads `runtime.jsonl`,
where Beacon writes endpoint activity, alongside the sibling `inventory_state.jsonl`
of periodic Cursor and Claude Code configuration inventory. Storage and retention
behavior is summarized in the
[local testing and logs docs](https://docs.asymptotelabs.ai/cli/local-testing-logs).

For offline threat detection, `beacon scan` runs open threat rules over local
telemetry with no network access. See the
[Threat Rules spec](spec/threat-rules/SPEC.md) and the generated
[rule field reference](spec/threat-rules/FIELDS.md) for rule format, CEL matching,
fixtures, and supported event fields.

## Start Here

- [Beacon CLI docs](https://docs.asymptotelabs.ai) — full documentation index.
- [Installation](https://docs.asymptotelabs.ai/cli/installation) — install Beacon locally.
- [For Security & IT Teams](https://docs.asymptotelabs.ai/cli/security-it-teams) — rollout, validation, and security workflows.
- [Security review](https://docs.asymptotelabs.ai/cli/security-review) — architecture, data handling, and local-only posture.
- [Endpoint agent](https://docs.asymptotelabs.ai/cli/endpoint) — install, status, repair, and uninstall.
- [Dashboard](https://docs.asymptotelabs.ai/cli/dashboard) — inspect local runtime logs.
- [Endpoint event schema](https://docs.asymptotelabs.ai/cli/event-schema) — normalized JSONL event model.
- [Supported surfaces](https://docs.asymptotelabs.ai/runtimes) — supported runtimes, destinations, and boundaries.
- [Command reference](https://docs.asymptotelabs.ai/cli/command-reference) — detailed CLI command docs.

## Quickstart

See the [Quickstart docs](https://docs.asymptotelabs.ai/cli/quickstart) for the full
setup paths.

### First-Run Onboarding

The first time you run `beacon endpoint install` in a terminal, Beacon asks for your
email and whether this is work or personal use, and sends that to Asymptote once.
Knowing who runs Beacon is how we decide which runtimes and integrations to build
next. It happens once per machine and never runs non-interactively: MDM deployments,
package postinstall scripts, `--system` installs, CI, `--dry-run`, and piped stdin all
skip it silently, and `--no-onboarding` or `BEACON_ONBOARDING=0` turns it off. Exactly
what is sent, and nothing else:

| Field | Example |
| --- | --- |
| Email you enter | `you@company.com` |
| Work, personal, or evaluating | `work` |
| OS, architecture, OS version | `darwin`, `arm64`, `15.5` |
| Beacon version and install method | `v0.0.31`, `homebrew` |
| Names of agent runtimes on this machine | `claude_code`, `cursor` |
| A random install ID | `64871b2b…` |

**Never sent:** prompts, file contents, commands, telemetry events, repository names,
or anything else Beacon captures. The endpoint agent itself stays local-only; this is
one HTTP request at install time, not an ongoing channel.

See the [first-run onboarding docs](https://docs.asymptotelabs.ai/cli/endpoint-onboarding#first-run-onboarding)
for fleet attribution without a terminal, inspecting or clearing the record, and
deletion requests.

### For Security & IT Teams

Start with the [security and IT quickstart](https://docs.asymptotelabs.ai/cli/quickstart)
and [managed deployment guidance](https://docs.asymptotelabs.ai/cli/security-it-teams)
for rollout, validation, retention, and SIEM forwarding. For vendor review, see the
[security review](https://docs.asymptotelabs.ai/cli/security-review).

### For Developers

Install the released CLI with Homebrew, or build from source:

```bash
brew tap asymptote-labs/tap
brew install beacon
beacon version
```

```bash
cd cli/beacon
make build
```

To verify a change against a **real** Claude Code session rather than only synthetic
payloads, `beacon-sandbox` runs one in a disposable Linux sandbox and checks what
Beacon actually captured:

```bash
cd beacon-sandbox
go run ./cmd/beacon-sandbox doctor
go run ./cmd/beacon-sandbox run --scenario s02-bash-command
```

See [Verify Beacon In A Sandbox](https://docs.asymptotelabs.ai/contributing/beacon-sandbox)
for setup, coverage, and limitations.

The browser extension is a separate, optional component that builds on its own. It
needs a running Beacon endpoint to post to, and its test suite replays recorded chat
streams through the real extension in headless Chromium, so it needs no login and no
network:

```bash
cd browser-extension
npm ci
npm run build          # load dist/ unpacked in Chrome
npm test               # replay e2e
```

See [`browser-extension/README.md`](browser-extension/) for what it captures and
retains.

## Star Growth

<p align="center">
  <a href="https://star-history.dera.page/#asymptote-labs/agent-beacon&Date">
    <img src="https://star-history.dera.page/svg?repos=asymptote-labs/agent-beacon&type=Date" alt="Beacon GitHub star growth" width="860">
  </a>
</p>

## License

[MIT](LICENSE)
