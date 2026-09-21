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

Beacon captures what happens across Claude Code, Codex, Cursor, OpenCode, and 20+ other agent harnesses, then turns useful workflows, corrections, and debugging patterns into reusable knowledge for future agents.

A problem solved by one agent shouldn't need to be learned from scratch by another.

## Your Agents Keep Forgetting

You teach Claude Code how your repo handles migrations.

Later, Cursor hits the same problem and has to figure it out again.

Then Codex makes the same mistake.

The knowledge exists. It's just trapped across isolated agent transcripts.

Beacon gives your agents a shared history across harnesses.

```text
Claude Code --+
Cursor -------+
Codex --------+--> Beacon --> reusable knowledge
OpenCode -----+                  |
Cline --------+                  v
                            future agents
```

Your Claude Code sessions can teach Codex.
Your Cursor debugging can improve OpenCode.
A workflow solved once can become reusable everywhere.

## How It Works

Beacon continuously captures agent activity across supported harnesses:

- prompts and responses
- tool calls
- commands
- file changes
- approvals
- MCP activity
- token usage
- session context

Every harness is normalized into the same event model.

Beacon can then evaluate historical traces, identify useful workflows and corrections, and turn them into reviewed project memory that future agents can retrieve through MCP or Agent Skills.

```text
Run agents -> capture traces -> find what worked -> extract knowledge -> reuse it everywhere
```

Your agent history stops being dead logs and becomes knowledge that compounds as you work.

## Getting Started

Beacon is open source, local-first, and requires no account.

### macOS

```bash
brew trust asymptote-labs/tap
brew tap asymptote-labs/tap
brew install beacon

beacon endpoint install
```

Then use your coding agents normally.

To inspect your agent history:

```bash
beacon endpoint dashboard
```

Events are also written locally to:

```text
~/.beacon/endpoint/logs/runtime.jsonl
```

No account. No API key. No network dependency.

Your data stays on your machine unless you explicitly configure forwarding.

### Linux

Install a `.deb` or `.rpm` from the [latest release](https://github.com/asymptote-labs/agent-beacon/releases/latest):

```bash
sudo apt install ./beacon_<version>_linux_amd64.deb
```

or:

```bash
sudo dnf install ./beacon_<version>_linux_amd64.rpm
```

### Windows

Install the x64 `.msi` from the [latest release](https://github.com/asymptote-labs/agent-beacon/releases/latest):

```powershell
msiexec /i BeaconEndpointAgent-<version>-x64.msi
```

## What Beacon Gives You

### Cross-Harness Memory

Useful knowledge isn't tied to one coding agent.

A debugging pattern discovered in Claude Code can be reused by Codex. A correction made in Cursor can become available to OpenCode.

Switch harnesses without throwing away everything your previous agents learned.

### Knowledge From Real Work

Beacon helps surface things your agents repeatedly learn while working:

- successful debugging approaches
- repository conventions
- repeated corrections
- useful tool sequences
- migration and deployment workflows
- recurring failure patterns

Instead of manually documenting every lesson, build knowledge from the agent work already happening.

### One History Across Every Agent

Beacon also gives you a normalized record of what your agents actually did.

See commands, tools, edits, failures, retries, approvals, and context across harnesses from one local dataset.

When something goes wrong, reconstruct the run instead of digging through separate transcripts.

### Local-First

Beacon stores telemetry as durable local JSONL.

Use the local dashboard, query the data yourself, build on top of it, or forward it into your own infrastructure.

Your agent history belongs to you, not the harness.

## Supported Agents

Beacon supports Claude Code, Cursor, Codex, OpenCode, Cline, Gemini CLI, GitHub Copilot CLI, Devin, Factory Droid, Pi, and 20+ other agent runtimes.

### Local Agents

| Runtime | Session | Prompt | Tool | Command | File | MCP | Tokens |
| --- | :-: | :-: | :-: | :-: | :-: | :-: | :-: |
| Claude Code | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Cursor | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | - |
| Codex CLI | ✅ | ✅ | ✅ | ✅ | - | - | ✅ |
| OpenCode | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Cline | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Gemini CLI | - | ✅ | ✅ | - | ✅ | ✅ | - |
| GitHub Copilot CLI | ✅ | ✅ | ✅ | - | - | - | - |
| Devin CLI | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | - |
| Factory Droid | ✅ | ✅ | ✅ | - | ✅ | - | - |
| Pi | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |

[See full runtime coverage ->](https://docs.beacon.sh/runtimes)

Beacon also supports browser chat, cloud agents, CI jobs, and SDK instrumentation.

## Where The Data Can Go

Local JSONL is the default.

Beacon can also forward the same normalized event stream into systems including:

Splunk · Datadog · Elastic · Microsoft Sentinel · CrowdStrike LogScale · Sumo Logic · Wazuh · AWS S3 · GCS · CloudWatch

Or build directly on the JSONL yourself.

## Open Source

Beacon is MIT licensed.

Try it with the coding agents you already use:

```bash
brew install asymptote-labs/tap/beacon
```

If Beacon is useful, give the repo a star.

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
