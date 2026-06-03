# Contributing to Beacon

Thanks for helping improve Beacon. This guide covers the day-to-day workflow for
changes to the local endpoint agent, hook adapter, Collector exporter, and
packaging assets.

Beacon is a local-only endpoint telemetry agent for AI runtimes. Keep
contributions aligned with that posture: normal hook execution should not depend
on hosted accounts, remote policy fetches, hosted dashboards, or external network
services.

## Ways to Contribute

Useful contributions include:

- **Bug fixes:** Improve the endpoint agent, hook adapter, Collector exporter,
  packaging, and local dashboard.
- **Documentation:** Clarify examples, validation steps, rollout guidance, and
  user-visible behavior.
- **Tests:** Make supported runtime, event schema, retention, redaction, and
  packaging behavior easier to verify.
- **Integrations:** Improve runtime or destination behavior that fits Beacon's
  local-only scope and supported forwarding model.
- **Packaging:** Improve MDM assets, package scripts, and smoke tests for
  supported macOS deployment paths.

## Project Layout

- `cli/beacon`: public `beacon` CLI, endpoint runtime, local dashboard, and
  endpoint integrations.
- `cli/beacon-hooks`: hook adapter invoked by supported AI agent runtimes.
- `collector-builder`: OpenTelemetry Collector distribution and
  `beaconjson` exporter.
- `packaging`: macOS package scripts, deployment helpers, and MDM assets.

Removed mirror trees and old product surfaces are not active contribution
targets. New work should stay focused on the Beacon paths above unless a
maintainer asks otherwise.

## Development Setup

Install Go 1.24 or newer. The repository has separate Go modules for the CLI,
hook adapter, and Collector exporter, so run Go commands from the module you are
working in.

Optional tools are only needed for specific workflows:

- `golangci-lint` for `make lint` in `cli/beacon`.
- `ocb` when building the custom Collector distribution from
  `collector-builder/builder.yaml`.
- macOS for endpoint packaging checks and smoke tests.

Build the public CLI:

```bash
cd cli/beacon
make build
```

Run the local dashboard during manual testing:

```bash
cd cli/beacon
go run . endpoint dashboard
```

## Development Workflow

1. Fork the repository or create a branch in your working copy.
2. Create a focused branch for the change:

   ```bash
   git checkout -b feature/your-change
   ```

3. Make the change, keeping refactors separate from feature or bug-fix work when
   practical.
4. Run the relevant tests and validation commands.
5. Open a pull request against `main` with a clear description of what changed,
   why it changed, and how reviewers can verify it locally.

## Common Workflows

Run CLI tests:

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

Run Collector exporter tests:

```bash
cd collector-builder/exporter/beaconjsonexporter
go test ./...
```

Check macOS packaging scripts:

```bash
sh packaging/macos/test-endpoint-scripts.sh
```

Run the macOS endpoint smoke test:

```bash
sh packaging/macos/smoke-endpoint.sh
```

## Validation Before PRs

Run the checks that match your change. For shared behavior, public CLI behavior,
event schema changes, hook normalization, Collector output, or packaging changes,
prefer the full recommended set:

```bash
cd cli/beacon && go test ./...
cd cli/beacon && go test -race ./internal/endpoint/...
cd cli/beacon-hooks && go test ./...
cd collector-builder/exporter/beaconjsonexporter && go test ./...
sh packaging/macos/test-endpoint-scripts.sh
sh packaging/macos/smoke-endpoint.sh
```

If you cannot run a relevant check locally, mention that in the pull request and
explain why.

## Contribution Guidelines

- **Preserve local-only behavior:** Do not add hosted account requirements,
  remote policy services, hosted dashboards, or external network dependencies to
  normal endpoint or hook execution.
- **Keep destinations scoped:** Use the repository's supported local and
  customer-managed forwarding patterns. Do not add vulnerability scanning,
  dependency remediation, broad endpoint enforcement, or new hosted SIEM
  dependencies unless a maintainer explicitly requests that direction.
- **Protect event contracts:** Keep `vendor`, `product`, `schema_version`,
  required event fields, and Wazuh-compatible JSONL output stable. When adding a
  new signal, include stable identifiers, counts, or hashes alongside retained
  raw content, and route raw fields through the configured content retention and
  redaction behavior.
- **Keep the dashboard read-only:** It should inspect local status and JSONL
  events, not mutate endpoint configuration or telemetry.
- **Write deterministic tests:** Prefer `t.TempDir()`, `t.Setenv("HOME", ...)`,
  fake binaries, and free local ports. Avoid tests that require root, real
  `launchctl` service changes, Wazuh, a live Collector, or external network
  access. Gate macOS-only behavior with `runtime.GOOS == "darwin"` or assert the
  non-Darwin contract explicitly.
- **Update relevant docs:** Keep documentation current when install, packaging,
  Collector, dashboard, or event schema behavior changes. The top-level
  `README.md` and module-level READMEs should stay consistent with user-visible
  behavior.
- **Verify platform support:** Do not claim support for macOS packaging paths,
  runtime harnesses, SIEM forwarding patterns, or other environments you cannot
  actually run or test. Open an issue or draft PR to discuss support you can only
  partially verify.

## AI-Assisted Contributions

AI-assisted development is welcome. Contributors still own the correctness,
security, maintainability, and product fit of every change they submit.

Before opening a PR, make sure you can explain non-trivial parts of the diff:
why the implementation works, what alternatives you considered, what you tested,
and what could break. If an AI tool generated a large portion of the change, a
short note about how you validated the result can help reviewers, but disclosure
is not required unless a maintainer asks.

## Getting Help

If you are unsure whether a change fits Beacon's scope, open a GitHub issue or a
draft pull request with the problem, proposed approach, and what you can verify
locally. You can also join the
[Discord community](https://discord.gg/zdNChS2fBu) for questions and discussion.

## Pull Request Checklist

- **Scope:** Keep the change focused on one contributor-visible problem or
  capability, and avoid mixing unrelated refactors with behavior changes.
- **Summary:** Explain what changed and why in a few sentences.
- **Tests:** Add or update tests for behavior changes.
- **Validation:** Run the relevant validation commands, include them in the PR
  description, and note any relevant checks you could not run.
- **Verification:** Provide local review steps, expected output, a minimal repro,
  or another concrete way to confirm the change works.
- **Tradeoffs:** Call out important edge cases, compatibility concerns, or
  tradeoffs when they are relevant to review.
- **Docs:** Update docs for user-visible behavior, deployment, schema, or
  packaging changes.
- **Artifacts:** Do not commit secrets, local credentials, generated binaries,
  temporary logs, or unrelated build artifacts.
