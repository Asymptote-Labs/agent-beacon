<p align="center">
  <img src="images/beacon-hero.png" alt="Beacon" width="860">
</p>

<h1 align="center">Beacon</h1>

<p align="center">
  <a href="https://github.com/asymptote-labs/agent-beacon/releases"><img src="https://img.shields.io/github/v/release/asymptote-labs/agent-beacon" alt="GitHub release"></a>
  <a href="https://github.com/asymptote-labs/homebrew-tap"><img src="https://img.shields.io/badge/homebrew-beacon-fbb040?logo=homebrew" alt="Homebrew"></a>
  <a href="https://github.com/asymptote-labs/agent-beacon/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/asymptote-labs/agent-beacon/ci.yml" alt="GitHub Workflow Status"></a>
  <a href="https://github.com/asymptote-labs/agent-beacon/blob/main/LICENSE"><img src="https://img.shields.io/github/license/asymptote-labs/agent-beacon" alt="MIT license"></a>
  <a href="https://docs.beacon.sh"><img src="https://img.shields.io/badge/docs-beacon.sh-0369a1" alt="Docs"></a>
  <a href="https://discord.gg/zdNChS2fBu"><img src="https://img.shields.io/badge/discord-community-5865F2?logo=discord&logoColor=white" alt="Discord"></a>
</p>

<p align="center">
  <strong>The cross-harness self-improving memory layer for AI agents.</strong>
</p>

<p align="center">
  <a href="https://docs.beacon.sh">Docs</a>
  ·
  <a href="https://discord.gg/zdNChS2fBu">Discord</a>
  ·
  <a href="https://docs.beacon.sh/cli/install">Install</a>
  ·
  <a href="https://docs.beacon.sh/cli">Commands</a>
</p>

Beacon captures **agent session history** across Claude Code, Cursor, Codex, OpenCode, and 20+ other harnesses, then turns useful workflows, corrections, and debugging patterns into reusable knowledge for future agents.

**A problem solved by one agent shouldn't need to be learned from scratch by another.**

---

## Beacon Overview

AI agents learn useful things every time you work with them. But that knowledge is usually trapped inside individual sessions and individual harnesses.

Beacon gives your agents a shared history.

It continuously captures agent session history across your coding agents, normalizes those traces into one common format, and helps turn the highest-signal work into reviewed memory that other agents can reuse.

Your Claude Code sessions can teach Codex.  
Your Cursor debugging can improve OpenCode.  
A workflow solved once can become reusable everywhere.

### Key Capabilities

- **Cross-harness session history** — one record across Claude Code, Cursor, Codex, OpenCode, Cline, and 20+ more
- **Compounding knowledge** — surface successful workflows, repeated corrections, debugging patterns, and repo conventions from real agent work
- **Reviewed agent memory** — promote useful knowledge into memory future agents can retrieve through MCP or Agent Skills
- **Unified traces** — prompts, responses, tool calls, commands, file edits, approvals, MCP activity, and tokens in one event model
- **Local-first** — durable JSONL and a local dashboard with no account or network dependency
- **Open and portable** — your session history is independent of whichever agent harness you use

---

## Use Cases

- **Cross-harness memory** — let knowledge discovered in one coding agent benefit every other agent you use
- **Repository knowledge** — capture conventions, workflows, and context agents repeatedly have to rediscover
- **Debugging memory** — preserve successful approaches to difficult bugs instead of losing them in old sessions
- **Repeated corrections** — identify things developers keep teaching agents and make them reusable
- **Session replay** — reconstruct what an agent actually did across commands, tools, files, and prompts
- **Portable history** — switch harnesses without throwing away everything your previous agents learned

---

## 🚀 Quick Start

Beacon is open source, local-first, and requires no account.

### macOS

```bash
brew trust asymptote-labs/tap
brew tap asymptote-labs/tap
brew install beacon

beacon endpoint install
```

Then use Claude Code, Cursor, Codex, or another supported agent normally.

Open your local session history:

```bash
beacon endpoint dashboard
```

Events are also written directly to:

```text
~/.beacon/endpoint/logs/runtime.jsonl
```

**No account. No API key. Nothing leaves your machine unless you configure it to.**

### Linux

Install the `.deb` or `.rpm` from the [latest release](https://github.com/asymptote-labs/agent-beacon/releases/latest):

```bash
sudo apt install ./beacon_<version>_linux_amd64.deb
```

or:

```bash
sudo dnf install ./beacon_<version>_linux_amd64.rpm
```

### Windows

Install the x64 MSI from the [latest release](https://github.com/asymptote-labs/agent-beacon/releases/latest):

```bash
msiexec /i BeaconEndpointAgent-<version>-x64.msi
```

For silent installation:

```bash
msiexec /i BeaconEndpointAgent-<version>-x64.msi /qn
```

---

## 🧠 Turn Session History Into Memory

Every agent session contains potentially useful knowledge about your codebase.

Beacon creates a loop around that history:

```text
Run agents
    ↓
Capture session history
    ↓
Evaluate what worked
    ↓
Extract useful knowledge
    ↓
Review + approve
    ↓
Reuse across future agents
```

That could be:

- the right way to run a migration
- a debugging path that finally fixed an obscure issue
- a testing convention agents repeatedly get wrong
- a repository-specific workflow
- the right sequence of internal tools
- a correction you've given multiple agents

Instead of disappearing into old sessions, that knowledge becomes reusable.

---

## 🔀 Cross-Harness by Design

Most agent memory belongs to a single harness.

Beacon sits across the harness layer.

```text
Claude Code ─┐
Cursor ──────┤
Codex ───────┼──→ Beacon ──→ shared project knowledge
OpenCode ────┤
Cline ───────┘
```

Because Beacon captures and normalizes session history across tools, knowledge learned through Claude Code doesn't have to stay in Claude Code.

Your Cursor sessions can improve Codex.  
Your Codex sessions can improve OpenCode.  
Your history keeps compounding even as you switch tools.

**Your agent session history belongs to you, not the harness.**

---

## 🔎 One Trace Format for Every Agent

Beacon captures agent execution where it happens and normalizes it into a common OpenTelemetry-based event model.

That includes:

- sessions
- prompts and responses
- tool calls
- commands
- file activity
- approvals
- MCP interactions
- token usage

Instead of separate proprietary histories for every coding tool, you get one dataset you can inspect, search, learn from, and build on.

---

## 🖥️ Local Dashboard

Beacon ships with a local, read-only dashboard:

```bash
beacon endpoint dashboard
```

Use it to explore session history across harnesses and understand what your agents actually did.

The underlying JSONL remains directly accessible, so you're never dependent on the UI.

---

## Supported Agents

Beacon supports local agents, browser agents, cloud agents, CI workflows, and agent SDKs.

Popular supported runtimes include:

**Claude Code · Cursor · Codex CLI · Codex Desktop · OpenCode · Cline · Gemini CLI · GitHub Copilot CLI · Devin · Factory Droid · Pi · OpenHands · Kiro · goose · Hermes**

And 20+ more.

### Local Agent Coverage

| Runtime | Collection | Session | Prompt | Tool | Command | File | Approval | MCP | Tokens |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Antigravity CLI | Hooks | ✅ | ✅ | ✅ | ✅ | ✅ | – | – | – |
| Claude Code | OTLP + hooks + poll | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Claude Cowork | OTLP | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Cline | Plugin + poll | ✅ | ✅ | ✅ | ✅ | ✅ | – | ✅ | ✅ |
| Codex CLI | OTLP + hooks + poll | ✅ | ✅ | ✅ | ✅ | – | ✅ | – | ✅ |
| Codex Desktop | OTLP | ✅ | ✅ | ✅ | ✅ | – | ✅ | – | ✅ |
| Cursor | Hooks + poll | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | – |
| DeepSeek Harness | Hooks | ✅ | ✅ | ✅ | ✅ | ✅ | – | ✅ | – |
| Devin CLI | Hooks | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | – |
| Devin Desktop | Hooks | – | ✅ | ✅ | ✅ | ✅ | – | ✅ | – |
| Factory Droid | OTLP + hooks + poll | ✅ | ✅ | ✅ | – | ✅ | ✅ | – | – |
| fx (Vercel Labs) | Poll | ✅ | ✅ | ✅ | ✅ | ✅ | – | ✅ | ✅ |
| Gemini CLI | OTLP | – | ✅ | ✅ | – | ✅ | ✅ | ✅ | – |
| GitHub Copilot CLI | OTLP | ✅ | ✅ | ✅ | – | – | ✅ | – | – |
| goose | Adapter only; manual hooks/OTLP | ✅ | ✅ | ✅ | ✅ | ✅ | – | ✅ | ✅ |
| Grok Build | Hooks + poll | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | – | – |
| Hermes Agent | Hooks + poll | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | – |
| Kiro | Hooks | ✅ | ✅ | ✅ | ✅ | ✅ | – | ✅ | – |
| Muse Code | Hooks | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | – | – |
| Oh My Pi | Extension | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| OpenClaw Gateway | Plugin + OTLP + poll | ✅ | ✅ | ✅ | ✅ | ✅ | – | ✅ | ~ |
| OpenCode | Plugin + poll | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| OpenHands | Hooks | ✅ | ✅ | ✅ | ✅ | ✅ | – | ✅ | – |
| Pi | Extension + poll | ✅ | ✅ | ✅ | ✅ | ✅ | – | ✅ | ✅ |
| Prime Agent | Extension + poll | ✅ | ✅ | ✅ | ✅ | ✅ | – | – | ✅ |
| Qwen Code | Hooks | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | – | – |
| Senpi | Extension | ✅ | ✅ | ✅ | ✅ | ✅ | – | – | ✅ |
| VS Code | OTLP + hooks | ✅ | ✅ | ✅ | ~ | ~ | – | ~ | – |

### Browser Chat

| Site | Collection | Prompt | Response | Tool | Tokens |
| --- | --- | --- | --- | --- | --- |
| Claude.ai | Extension → local OTLP | ✅ | ✅ | ✅ | ~ |
| ChatGPT | Extension → local OTLP | ✅ | ✅ | ✅ | – |

### Cloud Agents

| Runtime | Collection | Session | Prompt | Tool | Command | File | Tokens |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Claude Code Cloud Agents | Sandbox hooks → GCS or S3 | ✅ | ✅ | ✅ | ✅ | ✅ | – |
| Cursor Cloud Agents | Sandbox hooks → GCS or S3 | – | ✅ | ✅ | ✅ | ✅ | – |
| Devin Cloud Agents | API poll → GCS | ✅ | ✅ | – | – | – | ✅ |
| CI jobs | `beacon ci exec` → temporary local collector | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |

### SDK Instrumentation

| SDK Surface | Collection | Captures |
| --- | --- | --- |
| Anthropic | OpenLLMetry through `@asymptote/sdk` | Model call spans, errors, and OTel attributes |
| OpenAI | OpenLLMetry through `@asymptote/sdk` | Model call spans, errors, and OTel attributes |
| Claude Agent SDK | `Observe.wrapClaudeAgentQuery()` | Query root spans with Beacon-compatible prompt attributes |
| Vercel AI SDK | `experimental_telemetry` tracer handoff | Model call and tool spans where telemetry is enabled |

---

## Output Destinations

Beacon writes endpoint telemetry to local JSONL by default.

You can also forward the same normalized session history into infrastructure you already use:

**Splunk · Datadog · Elastic · Microsoft Sentinel · CrowdStrike Falcon LogScale · Sumo Logic · Wazuh · AWS S3 · GCS · CloudWatch**

| Destination | Category | Support Path |
| --- | --- | --- |
| Local JSONL | Local | Default endpoint log and local dashboard source |
| CrowdStrike Falcon LogScale HEC | SIEM | Endpoint forwarding with LogScale ingest tokens |
| Microsoft Sentinel | SIEM | Azure Monitor Agent and Data Collection Rule content pack |
| Rapid7 InsightIDR | SIEM | Custom Logs webhook content pack |
| Splunk HEC | SIEM | Endpoint forwarding during install or repair |
| Sumo Logic | SIEM | HTTP Logs & Metrics Source content pack |
| Wazuh | SIEM | Localfile configuration and Beacon content pack |
| AWS CloudWatch Logs | Log aggregation | Vector content pack |
| Datadog | Log aggregation | Datadog Agent custom log collection |
| Elastic | Log aggregation | Filebeat or Elastic Agent |
| Customer-managed pipelines | Log aggregation | Forward directly from local Beacon JSONL |
| AWS S3 | Object storage | Vector, CI upload, or cloud-agent snapshots |
| Google Cloud Storage | Object storage | Vector, CI upload, or cloud-agent snapshots |

---

## Architecture

Beacon captures activity where agents actually run and normalizes it into one shared event model.

```text
Local agents ───────┐
Browser chat ───────┤
CI ─────────────────┼──→ Beacon ──→ unified session history
Cloud agents ───────┤                    │
Agent SDKs ─────────┘                    ├──→ local JSONL
                                         ├──→ reviewed memory
                                         ├──→ MCP / Agent Skills
                                         └──→ your own infrastructure
```

See the [documentation](https://docs.beacon.sh/architecture/architecture) for the full architecture breakdown.

---

## Documentation

Read the docs for:

- installation
- supported runtimes
- event schema
- session history
- memory
- MCP
- Agent Skills
- forwarding
- advanced configuration

[**Read the docs →**](https://docs.beacon.sh)

---

## Contributing

Contributions are welcome.

Open an issue, submit a pull request, or join the [Discord](https://discord.gg/zdNChS2fBu).

---

## License

[MIT](LICENSE)

## Star History

<a href="https://www.star-history.com/?repos=asymptote-labs%2Fagent-beacon&type=date&legend=top-left">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/chart?repos=asymptote-labs/agent-beacon&type=date&theme=dark&legend=top-left" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/chart?repos=asymptote-labs/agent-beacon&type=date&legend=top-left" />
   <img alt="Star History Chart" src="https://api.star-history.com/chart?repos=asymptote-labs/agent-beacon&type=date&legend=top-left" />
 </picture>
</a>
