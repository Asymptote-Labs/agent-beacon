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
for AI agents.

Want to know what all of your agents are doing - across runtimes and environments, from endpoints and CI to browsers and the cloud? Beacon captures that activity and normalizes agent
telemetry into a [single, unified schema](https://docs.asymptotelabs.ai/cli/event-schema).

Beacon supports [21+ local agent runtimes](#local-agents) and
[every major surface agents run on](#supported-surfaces). It ships as a
lightweight [endpoint binary](https://docs.asymptotelabs.ai/cli/endpoint) and
[TypeScript SDK](#cloud-agents), installs with
[one command](https://docs.asymptotelabs.ai/cli/installation) or fleet-wide
through [MDM](#mdm-deployment), and forwards telemetry to
[major SIEM, observability, and data platforms](#output-destinations).

Learn more in the [Agent Beacon documentation](https://docs.asymptotelabs.ai).

## High-Level Architecture

Beacon captures activity where each agent actually runs, then normalizes it into a
single OpenTelemetry-based event model.

<p align="center">
  <img src="images/beacon-architecture.png" alt="Agent Beacon architecture: local agents, browser, agents in code, CI pipelines, and cloud agents feed a Beacon layer that collects, normalizes, stores, correlates, and detects, then forwards unified AI telemetry to customer-owned destinations" width="860">
</p>

- **Sources** — [local agents](#local-agents) through hooks, plugins, and local
  OpenTelemetry; [browser chat](#browser-chat) through an optional extension;
  agents in code through the [TypeScript SDK](#cloud-agents);
  [CI pipelines](#cloud-agents) through a temporary collector; and
  [cloud agents](#cloud-agents) through sandbox hooks.
- **Beacon** — collect, normalize, store, correlate, and detect. Every surface lands
  in one event model, one durable JSONL log, one session timeline, and one
  [local detection engine](#dashboard-and-local-detection).
- **Destinations** — inspect events in the local dashboard, retain JSONL, or forward
  the same stream into the [major enterprise-grade SIEMs](#output-destinations),
  log aggregators, and object storage.

Collection, processing, and inspection stay local by default; the same normalized
event model extends to CI, cloud-agent, and SDK paths under customer control. See the
[open-source architecture reference](https://docs.asymptotelabs.ai/architecture/architecture)
for the full breakdown by surface, or the
[system architecture overview](https://docs.asymptotelabs.ai/architecture/system-architecture)
to compare it with the managed path.

## Supported Surfaces

### Agent Runtimes

#### Local Agents

✅ collected · – not collected today, because the runtime does not expose it ·
~ depends on what the upstream exporter emits. Collection paths: **hooks** native to the
runtime, a Beacon-managed **plugin** or **extension**, local **OTLP**, or a **poll** of the
runtime's own session store.

| Runtime | Collection | Session | Prompt | Tool | Command | File | Approval | MCP | Tokens |
| --- | --- | :-: | :-: | :-: | :-: | :-: | :-: | :-: | :-: |
| [Antigravity CLI](https://docs.asymptotelabs.ai/cli/supported-runtimes-antigravity-cli) | Hooks | ✅ | ✅ | ✅ | ✅ | ✅ | – | – | – |
| [Claude Code](https://docs.asymptotelabs.ai/cli/supported-runtimes-claude-code) | OTLP + hooks | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| [Claude Cowork](https://docs.asymptotelabs.ai/cli/supported-runtimes-claude-cowork) | OTLP | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| [Cline](https://docs.asymptotelabs.ai/cli/supported-runtimes-cline) | Plugin | ✅ | ✅ | ✅ | ✅ | ✅ | – | ✅ | ✅ |
| [Codex CLI](https://docs.asymptotelabs.ai/cli/supported-runtimes-codex-cli) | OTLP + hook | ✅ | ✅ | ✅ | ✅ | – | ✅ | – | ✅ |
| [Cursor](https://docs.asymptotelabs.ai/cli/supported-runtimes-cursor) | Hooks | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | – |
| [Devin CLI](https://docs.asymptotelabs.ai/cli/supported-runtimes-devin) | Hooks | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | – |
| [Devin Desktop](https://docs.asymptotelabs.ai/cli/supported-runtimes-devin-desktop) | Hooks | – | ✅ | ✅ | ✅ | ✅ | – | ✅ | – |
| [Factory Droid](https://docs.asymptotelabs.ai/cli/supported-runtimes-factory-droid) | OTLP + hooks | ✅ | ✅ | ✅ | – | ✅ | – | – | – |
| [fx (Vercel Labs)](https://docs.asymptotelabs.ai/runtimes/vercel-fx) | Poll | ✅ | ✅ | ✅ | ✅ | ✅ | – | ✅ | ✅ |
| [Gemini CLI](https://docs.asymptotelabs.ai/cli/supported-runtimes-gemini-cli) | OTLP | – | ✅ | ✅ | – | ✅ | ✅ | ✅ | – |
| [GitHub Copilot CLI](https://docs.asymptotelabs.ai/cli/supported-runtimes-github-copilot-cli) | OTLP | ✅ | ✅ | ✅ | – | – | ✅ | – | – |
| [goose](https://docs.asymptotelabs.ai/runtimes/goose) | Plugin + OTLP | ✅ | ✅ | ✅ | ✅ | ✅ | – | ✅ | ✅ |
| [Grok Build](https://docs.asymptotelabs.ai/cli/supported-runtimes-grok-build) | Hooks | ✅ | ✅ | ✅ | ✅ | ✅ | – | – | – |
| [Hermes Agent](https://docs.asymptotelabs.ai/cli/supported-runtimes-hermes-agent) | Hooks | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | – | – |
| [Kiro](https://docs.asymptotelabs.ai/runtimes/kiro) | Hooks | ✅ | ✅ | ✅ | ✅ | ✅ | – | ✅ | – |
| [Muse Code](https://docs.asymptotelabs.ai/runtimes/muse-code) | Hooks | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | – | – |
| [Oh My Pi](https://docs.asymptotelabs.ai/runtimes/oh-my-pi) | Extension | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| [OpenClaw Gateway](https://docs.asymptotelabs.ai/cli/supported-runtimes-openclaw-gateway) | OTLP | ~ | ~ | ~ | ~ | ~ | ~ | ~ | ~ |
| [OpenCode](https://docs.asymptotelabs.ai/cli/supported-runtimes-opencode) | Plugin | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| [OpenHands](https://docs.asymptotelabs.ai/runtimes/openhands) | Hooks | ✅ | ✅ | ✅ | ✅ | ✅ | – | ✅ | – |
| [Pi](https://docs.asymptotelabs.ai/runtimes/pi) | Extension | ✅ | ✅ | ✅ | ✅ | ✅ | – | – | ✅ |
| [Prime Agent](https://docs.asymptotelabs.ai/runtimes/prime-agent) | Not yet collected | – | – | – | – | – | – | – | – |
| [Qwen Code](https://docs.asymptotelabs.ai/runtimes/qwen-code) | Hooks | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | – | – |
| [VS Code](https://docs.asymptotelabs.ai/cli/supported-runtimes-vscode) | OTLP + hooks | ✅ | ✅ | ✅ | ~ | ~ | – | ~ | – |

#### Browser Chat

| Site | Collection | Prompt | Response | Tool | Tokens |
| --- | --- | :-: | :-: | :-: | :-: |
| [Claude.ai](https://docs.asymptotelabs.ai/runtimes/claude-web) | Extension → local OTLP | ✅ | ✅ | ✅ | ✅ |
| [ChatGPT](https://docs.asymptotelabs.ai/runtimes/chatgpt-web) | Extension → local OTLP | ✅ | ✅ | ✅ | – |

One optional Chrome extension reads both chat streams and posts them to the local
collector. Prompt and response text is retained in full by default.

#### Cloud Agents

| Runtime | Collection | Session | Prompt | Tool | Command | File | Tokens |
| --- | --- | :-: | :-: | :-: | :-: | :-: | :-: |
| [Claude Code Cloud Agents](https://docs.asymptotelabs.ai/claude-code-cloud-agents) | Sandbox hooks → GCS or S3 | ✅ | ✅ | ✅ | ✅ | ✅ | – |
| [Cursor Cloud Agents](https://docs.asymptotelabs.ai/cursor-cloud-agents) | Sandbox hooks → GCS or S3 | – | ✅ | ✅ | ✅ | ✅ | – |
| [Devin Cloud Agents](https://docs.asymptotelabs.ai/devin-cloud-agents) | API poll → GCS | ✅ | ✅ | – | – | – | ✅ |

CI jobs are the ephemeral case:
[`beacon ci exec`](https://docs.asymptotelabs.ai/supported-runtimes-claude-code-ci) or
`beacon ci start` / `beacon ci finish` runs a temporary local collector for the length of
the job, capturing supported agent prompt, tool, command, file, and run context instead of
installing a persistent endpoint service.

##### SDK Instrumentation

| SDK surface | Collection | Captures |
| --- | --- | --- |
| [Anthropic](https://docs.asymptotelabs.ai/sdk/integrations-anthropic) | OpenLLMetry through `@asymptote/sdk` | Model call spans, errors, and OTel attributes |
| [OpenAI](https://docs.asymptotelabs.ai/sdk/integrations-openai) | OpenLLMetry through `@asymptote/sdk` | Model call spans, errors, and OTel attributes |
| [Claude Agent SDK](https://docs.asymptotelabs.ai/sdk/integrations-claude-agent-sdk) | `Observe.wrapClaudeAgentQuery()` | Query root spans with Beacon-compatible prompt attributes |
| [Vercel AI SDK](https://docs.asymptotelabs.ai/sdk/integrations-vercel-ai-sdk) | `experimental_telemetry` tracer handoff | Model call and tool spans where telemetry is enabled |

### Output Destinations

Beacon writes endpoint telemetry to local JSONL by default and supports
customer-controlled forwarding into SIEM, log aggregation, and object storage
destinations, plus an opt-in managed path to the Asymptote dashboard.

| Destination | Category | Support path |
| --- | --- | --- |
| [Local JSONL](https://docs.asymptotelabs.ai/cli/local-testing-logs) | Local | Default endpoint log and local dashboard source |
| [Asymptote Managed](https://docs.asymptotelabs.ai/log-forwarding/asymptote) | Managed | Vector `http` forwarder with a per-device key approved in the browser, revocable from the dashboard |
| [CrowdStrike Falcon LogScale HEC](https://docs.asymptotelabs.ai/cli/siem-forwarding-falcon) | SIEM | Endpoint forwarding with LogScale ingest tokens during install or repair |
| [Microsoft Sentinel](https://docs.asymptotelabs.ai/cli/siem-forwarding-microsoft-sentinel) | SIEM | Azure Monitor Agent and Data Collection Rule content pack |
| [Rapid7 InsightIDR](https://docs.asymptotelabs.ai/cli/siem-forwarding-rapid7) | SIEM | Custom Logs webhook content pack |
| [Splunk HEC](https://docs.asymptotelabs.ai/cli/siem-forwarding-splunk) | SIEM | Endpoint forwarding during install or repair |
| [Sumo Logic](https://docs.asymptotelabs.ai/cli/siem-forwarding-sumo) | SIEM | HTTP Logs & Metrics Source content pack |
| [Wazuh](https://docs.asymptotelabs.ai/cli/siem-forwarding-wazuh) | SIEM | Localfile configuration and Beacon Wazuh content pack |
| [AWS CloudWatch Logs](https://docs.asymptotelabs.ai/cli/siem-forwarding-cloudwatch) | Log aggregation | Vector content pack using customer-managed AWS credentials |
| [Datadog](https://docs.asymptotelabs.ai/cli/siem-forwarding-datadog) | Log aggregation | Datadog Agent custom log collection |
| [Elastic](https://docs.asymptotelabs.ai/cli/siem-forwarding-elastic) | Log aggregation | Filebeat or Elastic Agent content pack |
| [Customer-managed pipelines](https://docs.asymptotelabs.ai/cli/siem-forwarding) | Log aggregation | Forwarding from local Beacon JSONL under customer control |
| [AWS S3](https://docs.asymptotelabs.ai/cli/siem-forwarding-s3) | Object storage | Vector, CI upload, or direct compressed snapshots from supported cloud agents |
| [Google Cloud Storage](https://docs.asymptotelabs.ai/cli/siem-forwarding-gcs) | Object storage | Vector and packaged macOS helpers, CI upload, or direct compressed snapshots |

Every destination except Asymptote Managed reads the same local JSONL, under your control.

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
`/opt/beacon/jamf/claude/gcs/` that run bundled Vector as a launchd job. Connecting a
system-mode endpoint to Asymptote Managed is interactive today: an admin runs
`sudo beacon endpoint connect --system` on the machine and approves it in the console
user's browser. Headless enrollment tokens for MDM fleets are planned as a follow-up.

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
skip it silently, and `BEACON_ONBOARDING=0` turns it off. Exactly what is sent, and
nothing else:

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
one HTTP request at install time, not an ongoing channel, unless you connect the
machine to Asymptote Managed.

The same first-run prompt ends with one more question, **where should this machine's
agent telemetry go?**, answered with the arrow keys:

- **Keep it on this machine** (the default, so Enter never forwards anything). Local
  JSONL and local dashboard.
- **Forward to your own infrastructure**: a SIEM, observability platform, or an S3/GCS
  bucket you own. Beacon points you at the [log forwarding docs](https://docs.asymptotelabs.ai/log-forwarding)
  and the install stays local until you set up a pack.
- **Forward to Asymptote Managed**: runs `beacon endpoint connect` after the install. Your
  browser opens the Asymptote dashboard, a member of your organization approves this
  specific device, and a Vector forwarder starts shipping the runtime and inventory JSONL
  with a per-device key. Nothing recorded before the approval is sent, and the device can
  be revoked from the dashboard at any time.

The answer stays on the machine and is never sent. Local and own-infrastructure answers
are recorded at once and the question is not asked again; the Asymptote answer is
recorded once the machine is connected, so a failed install or connection is asked again
on the next interactive install. `beacon endpoint install --connect` skips the question
and connects; `BEACON_MANAGED_INGEST=0` hides the Asymptote option. See
[`beacon endpoint connect`](https://docs.asymptotelabs.ai/cli/endpoint-connect) and
[Asymptote Managed forwarding](https://docs.asymptotelabs.ai/log-forwarding/asymptote).

See the [first-run onboarding docs](https://docs.asymptotelabs.ai/cli/endpoint-onboarding#first-run-onboarding)
for fleet attribution without a terminal, inspecting or clearing the record, and
deletion requests.

### For Security & IT Teams

Start with the [security and IT quickstart](https://docs.asymptotelabs.ai/cli/quickstart)
and [managed deployment guidance](https://docs.asymptotelabs.ai/cli/security-it-teams)
for rollout, validation, retention, and SIEM forwarding. For vendor review, see the
[security review](https://docs.asymptotelabs.ai/cli/security-review).

### For Developers

Install the released CLI with Homebrew, or build from source. On macOS the formula also
pulls in the tap's own Vector mirror, so a Homebrew install can connect to Asymptote
Managed without a second step. That mirror is the `beacon-vector` formula, not `vector`:
Homebrew allows a single keg named `vector`, so it installs alongside — and never
conflicts with — a Vector you already have from `vectordotdev/brew`. It is kept off your
PATH and Beacon finds it on its own. On Linux, install the `vector` package from
[vector.dev](https://vector.dev) if you want managed forwarding.

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
