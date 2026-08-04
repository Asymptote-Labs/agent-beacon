# beacon-sandbox

A testing tool for Beacon contributors. It starts a throwaway Linux machine in the cloud, installs
your local Beacon build on it, runs a real Claude Code session inside it, and checks whether Beacon
recorded what the agent actually did.

Use it when you have changed how Beacon captures telemetry and want proof it still works against a
live agent, not just that the unit tests pass. Nothing runs on your own machine, and the sandbox is
destroyed when the test finishes.

This is a tool for working *on* Beacon. It is not part of Beacon, and it is not in any release.

**Full guide: [Testing Beacon In A Sandbox](https://docs.asymptotelabs.ai/contributing/beacon-sandbox)**
· Coding agents should read [`AGENTS.md`](AGENTS.md).

## Quick start

You need a [Modal](https://modal.com/docs/guide/sandboxes) account for the throwaway machines (the
free tier covers this) and an Anthropic API key with credit, since the test runs a real agent
session. Each test costs a few cents.

```bash
pip install modal && modal token new
export ANTHROPIC_API_KEY=sk-ant-...

cd cli/beacon && make build-linux-amd64                        # the Beacon being tested
cd ../../beacon-sandbox
go run ./cmd/beacon-sandbox doctor --fix                       # checks the setup
go run ./cmd/beacon-sandbox run --scenario s02-bash-command    # one test, ~3 min
go run ./cmd/beacon-sandbox run                                # all seven, ~20 min
```

`doctor` checks everything the tool needs and prints the exact fix for anything missing, so it is
both the right first step and the right thing to rerun if a test behaves oddly.

## Why markers instead of expected output

AI sessions are not repeatable — ask an agent to run a command twice and you get two slightly
different sessions. So instead of comparing against fixed expected output, each test:

- puts a **random one-off string** in the prompt and checks it appears in Beacon's log, and
- asks the agent to **write that string into a file**.

If the file is there the agent definitely did the work, so a missing event is Beacon's problem
rather than the model having declined the task. Without that second check, those two situations look
identical.

Tests that leave no file behind (prompts, token counts, approvals) fall back to the agent's own
success report for the same purpose.

One thing to know when reading results: a warning means a check *could not run*, not that it
passed. Silence from this tool is meant to mean "checked and clean".

## Commands

| Command | What it does |
|---------|--------------|
| `doctor [--fix] [--json]` | Check the setup and say how to fix what is missing |
| `run [--scenario ID] [--repeat N] [--keep-sandbox]` | Start a sandbox, run a session, check results |
| `verify <run-dir> [--mutate MODE]` | Re-check a saved run. Free and instant |
| `diff <before> <after>` | Compare what two runs captured |
| `clean` | Delete saved run output |

`verify` is worth knowing about: every run saves its output under `runs/`, and `verify` re-checks
that output without starting a sandbox or calling the API. Use it while adjusting what a test
expects, rather than paying for another run.

Three ways to supply the API key, in the order the tool looks: `--modal-secret NAME`,
`--api-key-command CMD`, or `ANTHROPIC_API_KEY`.

## Layout

```
credentials/  finds the Anthropic API key
sandbox/      the cloud-machine interface, and the Modal implementation
image/        the sandbox image: Linux, a pinned Claude Code, and your Beacon build
scenario/     the test file format
scenarios/    the tests themselves
runner/       runs one test and collects the output
check/        decides whether the collected output is correct
hostguard/    confirms your own machine was left alone
```

## Checking that the checks work

A test that cannot fail is worse than no test, so you can deliberately damage a saved run and
confirm the tool notices:

```bash
go test ./...                                                        # no accounts or cost needed
go run ./cmd/beacon-sandbox verify --mutate corrupt-line runs/<id>/  # a PASS must become a FAIL
```

Other options are `drop-commands`, `drop-action:<action>`, and `plant-secret`. Each should turn a
`PASS` into a `FAIL`.

## Limitations

Modal only provides Linux x86 machines, so this **cannot test the macOS build** — launchd, the
signed installer, and notarization are out of reach. It also tests one collection path (the
temporary collector Beacon uses in CI and cloud agents) rather than a permanently installed Beacon,
and it confirms the thing it planted was recorded rather than proving nothing else was missed.

The [full guide](https://docs.asymptotelabs.ai/contributing/beacon-sandbox) has the complete list.
