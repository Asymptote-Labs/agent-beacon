<p align="center">
  <img src="images/beacon-hero.png" alt="Beacon" width="860">
</p>

<h1 align="center">Asymptote Lab's Beacon</h1>

<p align="center">
  <a href="https://github.com/asymptote-labs/agent-beacon/releases"><img src="https://img.shields.io/github/v/release/asymptote-labs/agent-beacon" alt="GitHub release"></a>
  <a href="https://github.com/asymptote-labs/homebrew-tap"><img src="https://img.shields.io/badge/homebrew-beacon-fbb040?logo=homebrew" alt="Homebrew"></a>
  <a href="https://github.com/asymptote-labs/agent-beacon/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/asymptote-labs/agent-beacon/ci.yml" alt="GitHub Workflow Status"></a>
  <a href="https://github.com/asymptote-labs/agent-beacon/blob/main/LICENSE"><img src="https://img.shields.io/github/license/asymptote-labs/agent-beacon" alt="MIT license"></a>
  <a href="https://docs.asymptotelabs.ai/cli"><img src="https://img.shields.io/badge/docs-asymptotelabs.ai-0369a1" alt="Docs"></a>
</p>

<p align="center">
  <strong>Unified endpoint telemetry for AI agents.</strong>
</p>

<p align="center">
  <a href="https://docs.asymptotelabs.ai/cli">Docs</a>
  ·
  <a href="https://docs.asymptotelabs.ai/cli/installation">Install</a>
  ·
  <a href="https://docs.asymptotelabs.ai/cli/security-it-teams">For Security & IT Teams</a>
  ·
  <a href="https://docs.asymptotelabs.ai/cli/dashboard">Dashboard</a>
  ·
  <a href="https://docs.asymptotelabs.ai/cli/command-reference">Commands</a>
</p>

## What Is Beacon?

Beacon is Asymptote's open-source endpoint agent for security and IT teams that
need visibility into local AI agent activity.

It runs locally, captures supported activity from local agent harnesses like
Claude Code, Codex CLI, Factory Droid, Claude Cowork, and Cursor, then
normalizes that activity into endpoint events your team can inspect and retain
locally.

Beacon is built to be easy to deploy for Security and IT teams through
[MDM deployment](https://docs.asymptotelabs.ai/cli/security-it-teams) and to
connect to Wazuh, Splunk HEC, or customer-managed SIEM pipelines, while
remaining visibility-first and local-first during normal endpoint collection.

## High-Level Architecture

Beacon keeps collection, processing, and inspection local to the endpoint while
leaving forwarding under customer control.

<p align="center">
  <img src="images/beacon-architecture.png" alt="Beacon endpoint architecture" width="860">
</p>

- **Agent runtime layer:** Local hooks and OpenTelemetry sources capture
  supported activity from AI agent harnesses on the endpoint.
- **Beacon endpoint layer:** Local processing normalizes events, applies
  retention and redaction settings, and writes durable endpoint telemetry.
- **Output layer:** Teams inspect events in the local dashboard, retain JSONL,
  or forward records into Wazuh, Splunk HEC, and customer-managed SIEM
  pipelines.

## Dashboard

Beacon includes a local, read-only dashboard for validating endpoint activity
without a hosted backend. The overview screen summarizes recent runtime events
and collection status, while log search helps teams inspect normalized event
records during rollout, testing, and investigations.

<p align="center">
  <img src="images/dashboard-overview.png" alt="Beacon dashboard overview" width="860">
</p>

<p align="center">
  <img src="images/dashboard-log-search.png" alt="Beacon dashboard log search" width="860">
</p>

## Start Here

- [Beacon CLI docs](https://docs.asymptotelabs.ai/cli) — full documentation index.
- [Installation](https://docs.asymptotelabs.ai/cli/installation) — install Beacon locally.
- [For Security & IT Teams](https://docs.asymptotelabs.ai/cli/security-it-teams) — rollout, validation, and security workflows.
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
for rollout, validation, retention, and SIEM forwarding.

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
[Beacon CLI docs](https://docs.asymptotelabs.ai/cli).

## License

[MIT](LICENSE)
