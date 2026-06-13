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

Agent Beacon is the world's first [open-source telemetry layer](https://justindsouza.substack.com/p/introducing-beacon-endpoint-telemetry) for AI agents wherever they run: locally, in CI, or in the cloud.

Beacon started with local endpoint telemetry for security and IT teams that need visibility into AI agent activity on employee machines. It now captures supported runtime activity across local agents, CI agents, and cloud agents, then normalizes that activity into events your team can inspect, retain, and forward under your control.

Beacon is built to be easy to deploy for Security and IT teams through
[MDM deployment](#mdm-deployment), CI workflows, and cloud-agent setup paths, and to
emit agent harness telemetry logs to
all the [major enterprise-grade SIEMs](https://github.com/Asymptote-Labs/agent-beacon#output-destinations).

Learn more in the [Agent Beacon Documentation](https://docs.asymptotelabs.ai).

## High-Level Architecture

Beacon keeps endpoint collection, processing, and inspection local by default,
while extending the same normalized event model to CI and cloud-agent telemetry
paths under customer control.

<p align="center">
  <img src="images/beacon-architecture.png" alt="Beacon endpoint architecture" width="860">
</p>

- **Agent runtime layer:** Hooks, OpenTelemetry sources, CI wrappers, and SDKs
  capture supported activity from AI agent harnesses wherever they run.
- **Beacon endpoint layer:** Local processing normalizes events, applies
  retention and redaction settings, and writes durable endpoint telemetry.
- **Output layer:** Teams inspect events in the local dashboard, retain JSONL,
  or forward records into all the [major enterprise-grade SIEMs](https://github.com/Asymptote-Labs/agent-beacon#output-destinations).

## Supported Surfaces

Beacon captures supported agent harness activity across local endpoints, CI
jobs, and cloud-agent surfaces, then writes normalized events that teams can
inspect in place or forward into customer-managed security pipelines.

### Agent Runtimes

Agent Beacon supports the most popular enterprise agent harnesses across local,
CI, and cloud surfaces.

#### Local Agents

##### Coding Agent Harnesses

| Agent harness | Collection path | Telemetry coverage |
| --- | --- | --- |
| [Antigravity CLI](https://docs.asymptotelabs.ai/cli/supported-runtimes-antigravity-cli) | Native hooks | Prompt, pre-tool, post-tool, stop, invocation, command, and file telemetry where Antigravity exposes hook payloads |
| [Claude Code](https://docs.asymptotelabs.ai/cli/supported-runtimes-claude-code) | Local OTLP export plus optional hooks | Prompt, command, tool, file, lifecycle, subagent, and permission telemetry where emitted through OTLP or hooks |
| [Codex CLI](https://docs.asymptotelabs.ai/cli/supported-runtimes-codex-cli) | Local OTLP logs | Session, prompt, approval, and tool-result activity from Codex semantic logs |
| [Cursor](https://docs.asymptotelabs.ai/cli/supported-runtimes-cursor) | Native hooks | Prompt, tool, shell command, MCP-like, approval, and file edit telemetry |
| [Devin CLI](https://docs.asymptotelabs.ai/cli/supported-runtimes-devin) | Native hooks | Session, prompt, pre-tool, post-tool, permission request, stop, session-end, approval, and file telemetry |
| [Devin Desktop](https://docs.asymptotelabs.ai/cli/supported-runtimes-devin-desktop) | Cascade/Windsurf hooks | Prompt, command, MCP tool, file read, and file write telemetry where Desktop exposes Cascade hook payloads |
| [Factory Droid](https://docs.asymptotelabs.ai/cli/supported-runtimes-factory-droid) | OTLP HTTP plus optional hooks | Session, prompt, write/edit/create tool use, stop, session-end, and available OTLP telemetry |
| [Gemini CLI](https://docs.asymptotelabs.ai/cli/supported-runtimes-gemini-cli) | Opt-in local OTLP | Prompts, tool calls, MCP activity, file operations, and approval-related events emitted through OTLP |
| [GitHub Copilot CLI](https://docs.asymptotelabs.ai/cli/supported-runtimes-github-copilot-cli) | MDM-managed OTLP HTTP | Prompt, session, tool, and approval-like activity emitted through Copilot CLI spans |
| [Grok Build](https://docs.asymptotelabs.ai/cli/supported-runtimes-grok-build) | Native hooks | Session, prompt, pre-tool, post-tool, failed tool, stop, session-end, command, and file telemetry |
| [OpenCode](https://docs.asymptotelabs.ai/cli/supported-runtimes-opencode) | Managed plugin hooks | Chat messages, session events, command execution, permission activity, diffs, and errors |
| [VS Code](https://docs.asymptotelabs.ai/cli/supported-runtimes-vscode) | Copilot Chat OTel plus optional preview hooks | Copilot session, prompt, model, and tool activity through OTel; optional hooks for extra lifecycle and cross-agent detail |

##### Knowledge Worker Agent Harnesses

| Agent harness | Collection path | Telemetry coverage |
| --- | --- | --- |
| [Claude Cowork](https://docs.asymptotelabs.ai/cli/supported-runtimes-claude-cowork) | Admin-configured OTLP | Prompt, command, tool, and file telemetry when emitted through Claude Cowork OTLP |
| [Hermes Agent](https://docs.asymptotelabs.ai/cli/supported-runtimes-hermes-agent) | Shell hooks | Prompt, observed tool, command, file, approval request and response, session lifecycle, and subagent stop telemetry |
| [OpenClaw Gateway](https://docs.asymptotelabs.ai/cli/supported-runtimes-openclaw-gateway) | Gateway-configured OTLP/HTTP | OTLP logs, traces, and metrics from the Gateway diagnostics plugin |

#### CI Agents

| Harness | Collection path | Telemetry coverage |
| --- | --- | --- |
| [CI agent telemetry](https://docs.asymptotelabs.ai/supported-runtimes-claude-code-ci) | Temporary local collector through `beacon ci exec` or `beacon ci start` / `beacon ci finish` | Supported agent prompt, tool, command, file, and run context where emitted during the job |

#### Cloud Agents

| Cloud surface | Collection path | Telemetry coverage |
| --- | --- | --- |
| [Anthropic](https://docs.asymptotelabs.ai/sdk/integrations-anthropic) | OpenLLMetry instrumentation through `@asymptote/sdk` | Supported Anthropic model call spans, errors, and OpenTelemetry attributes |
| [Claude Agent SDK](https://docs.asymptotelabs.ai/sdk/integrations-claude-agent-sdk) | Query wrapper through `Observe.wrapClaudeAgentQuery()` | Query root spans with Beacon-compatible prompt attributes |
| [Claude Code Cloud Agents](https://docs.asymptotelabs.ai/claude-code-cloud-agents) | Cloud sandbox hooks with GCS upload | Session, prompt, tool, command, file, and lifecycle telemetry where Claude Code cloud hook payloads expose it |
| [Cursor Cloud Agents](https://docs.asymptotelabs.ai/cursor-cloud-agents) | Cloud sandbox hooks with GCS upload | Tool, shell command, file, subagent, and compaction telemetry where Cursor cloud hook payloads expose it |
| [OpenAI](https://docs.asymptotelabs.ai/sdk/integrations-openai) | OpenLLMetry instrumentation through `@asymptote/sdk` | Supported OpenAI model call spans, errors, and OpenTelemetry attributes |
| [Vercel AI SDK](https://docs.asymptotelabs.ai/sdk/integrations-vercel-ai-sdk) | Tracer handoff through `experimental_telemetry` | AI SDK model call and tool spans where telemetry is enabled |

### Output Destinations

Agent Beacon writes endpoint telemetry to local JSONL by default and supports
customer-controlled forwarding into common security information and event
management (SIEM), log aggregation, and object storage destinations.

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
| [AWS S3](https://docs.asymptotelabs.ai/cli/siem-forwarding-s3) | Vector content pack over local JSONL using customer-managed AWS credentials |
| [Google Cloud Storage](https://docs.asymptotelabs.ai/cli/siem-forwarding-gcs) | Vector content pack over local JSONL using customer-managed Google credentials |

#### Local

| Destination | Support path |
| --- | --- |
| [Local JSONL](https://docs.asymptotelabs.ai/cli/local-testing-logs) | Default endpoint log and local dashboard source |

### MDM Deployment

Agent Beacon is designed for Security and IT teams to deploy and validate
through standard MDM workflows.

| MDM platform | Support path |
| --- | --- |
| [Fleet](https://docs.asymptotelabs.ai/cli/fleet) | macOS package and user-context deployment helpers |
| [Jamf Pro](https://docs.asymptotelabs.ai/cli/jamf) | macOS package, policy scripts, validation, and Extension Attributes |

## Dashboard

Beacon includes a local, read-only dashboard for validating endpoint activity
without a hosted backend. The overview screen summarizes recent runtime events
and collection status, while log search helps teams inspect normalized event
records during rollout, testing, and investigations. The token usage screen
breaks captured token telemetry down by model, session (with per-step
drilldown), CI run, and harness, including context-window utilization and
runtime-reported cost. The detections screen lists the active threat-detection
rules (the local store when present, otherwise the embedded baseline), and the
findings screen runs those rules over the runtime log on load and links each hit
back to its rule — the same read-only, offline detection as `beacon scan`.

For scripted or CI reporting, `beacon endpoint tokens` prints the same token
usage rollups as text or JSON from any runtime JSONL log, for example
`beacon endpoint tokens --log-path "$BEACON_CI_LOG_PATH" --json` after a CI
session.

Beacon writes endpoint activity to a stable local `runtime.jsonl` file. The
active file rotates at 10 MiB with five numbered local archives, keeping the
endpoint handoff file bounded while external SIEM forwarders continue tailing
the active path. The dashboard reads the active log plus retained numbered
archives for local triage; SIEM destinations remain the source of truth for
long-term retention and search.

<p align="center">
  <img src="images/dashboard-overview.png" alt="Beacon dashboard overview" width="860">
</p>

<p align="center">
  <img src="images/dashboard-log-search.png" alt="Beacon dashboard log search" width="860">
</p>

## Detect threats in local telemetry

`beacon scan` runs threat-detection rules over the local runtime log and reports
findings — read-only, with no network access. Rules are an open, versioned format
([`spec/threat-rules`](spec/threat-rules/SPEC.md)) whose match conditions are
[CEL](https://cel.dev) expressions over the endpoint event schema, and each rule ships
its own conformance fixtures.

```bash
beacon scan                       # run the active rules over the runtime log
beacon scan --json                # machine-readable findings
beacon scan --min-severity high   # only high/critical findings
beacon scan --fail-on high        # non-zero exit for CI gating
```

The detection engine ships in the binary, but the rule corpus is **external data** loaded
from a local store (`~/.beacon/endpoint/rules`), so a growing rule set never enlarges the
agent. A small baseline is built in; manage the store with `beacon rules`:

```bash
beacon rules list                 # active rules (baseline or store)
beacon rules add ./my-rules       # install local rule files (validated before install)
beacon rules pull <url>           # explicit, user-initiated fetch of a rule pack
beacon rules lint ./rules         # validate + run a rule pack's fixtures (authoring)
beacon rules fields               # list event fields a rule can match on
```

`beacon rules pull` is the only command that reaches the network, and only when you run
it against a URL you supply — the agent never fetches rules on its own.

## Start Here

- [Beacon CLI docs](https://docs.asymptotelabs.ai) — full documentation index.
- [Installation](https://docs.asymptotelabs.ai/cli/installation) — install Beacon locally.
- [For Security & IT Teams](https://docs.asymptotelabs.ai/cli/security-it-teams) — rollout, validation, and security workflows.
- [Security review](https://docs.asymptotelabs.ai/cli/security-review) — review Beacon's architecture, data handling, and local-only posture.
- [Endpoint agent](https://docs.asymptotelabs.ai/cli/endpoint) — install, status, repair, and uninstall.
- [Dashboard](https://docs.asymptotelabs.ai/cli/dashboard) — inspect local runtime logs.
- [Endpoint event schema](https://docs.asymptotelabs.ai/cli/event-schema) — normalized JSONL event model.
- [Supported surfaces](https://docs.asymptotelabs.ai/cli/supported-surfaces) — supported runtimes, destinations, and boundaries.
- [Command reference](https://docs.asymptotelabs.ai/cli/command-reference) — detailed CLI command docs.

## Quickstart

See the [Quickstart](https://docs.asymptotelabs.ai/cli/quickstart) docs for the
full setup paths.

### For Security & IT Teams

Start with the
[security and IT quickstart](https://docs.asymptotelabs.ai/cli/quickstart) and
[managed deployment guidance](https://docs.asymptotelabs.ai/cli/security-it-teams)
for rollout, validation, retention, and SIEM forwarding. For vendor review, see
the [security review](https://docs.asymptotelabs.ai/cli/security-review).

### For Developers

Install the released Beacon CLI locally with Homebrew:

```bash
brew tap asymptote-labs/tap
brew install beacon
beacon version
```

Or build from source:

```bash
cd cli/beacon
make build
```

For setup, deployment, integrations, and command details, see the
[Beacon CLI docs](https://docs.asymptotelabs.ai).

## Star Growth

<p align="center">
  <a href="https://www.star-history.com/#asymptote-labs/agent-beacon&Date">
    <img src="https://api.star-history.com/svg?repos=asymptote-labs/agent-beacon&type=Date" alt="Beacon GitHub star growth" width="860">
  </a>
</p>

## License

[MIT](LICENSE)
