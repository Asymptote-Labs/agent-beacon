---
name: self-verify-beacon-in-sandbox
description: Verify a Beacon change end to end by running a real Claude Code session inside a disposable Linux cloud sandbox and checking that Beacon captured what the agent actually did. Use when asked to verify, validate, test, or prove that a Beacon change works for real rather than just compiling; when asked whether telemetry, event capture, commands, file paths, prompts, tokens, or approvals are still recorded correctly; when investigating a suspected capture gap; or when preparing a Beacon pull request that touches the CLI, hooks, or the collector exporter.
---

# Verify Beacon in a sandbox

This repository ships `beacon-sandbox`, which rents a disposable Linux sandbox from Modal,
installs the Beacon under test, runs a real Claude Code session inside it, and checks whether
Beacon recorded what the agent did.

**Read `beacon-sandbox/AGENTS.md` for the full operating manual** — scenario selection, how to
interpret a verdict, and the failure modes that look like Beacon bugs but are not. The commands
below are enough to start; that file is what stops you misreading the result.

## Step 1: check prerequisites

Always start here. It is free, needs no sandbox, and prints the exact fix for anything missing:

```bash
cd beacon-sandbox
go run ./cmd/beacon-sandbox doctor
```

Add `--json` for machine-readable output with a top-level `ready` boolean.

## Step 2: resolve what doctor reports

Apply the `fix:` line it prints, then rerun `doctor`. The two build artifacts you can resolve
yourself:

```bash
cd cli/beacon && make build-linux-amd64      # if beacon_binary FAILs
cd beacon-sandbox && go run ./cmd/beacon-sandbox doctor --fix   # downloads the collector
```

**Two prerequisites you must NOT try to resolve yourself:**

1. **Modal authentication.** `modal token new` completes through an authenticated *web session*,
   so it opens a browser and blocks — running it yourself will hang until timeout. If
   `modal_auth` FAILs, ask the user to run it, suggesting they type
   `! pip install modal && modal token new` so the output lands in the conversation. In CI,
   `MODAL_TOKEN_ID` and `MODAL_TOKEN_SECRET` work non-interactively instead.
2. **The Anthropic credential.** Never invent, echo, or write a key. If `anthropic_credential`
   FAILs, ask the user which of the three paths they want: `ANTHROPIC_API_KEY`,
   `--api-key-command CMD`, or `--modal-secret NAME`.

Do not proceed to step 3 while `doctor` reports any FAIL — the run will fail later and more
confusingly.

## Step 3: run it

```bash
go run ./cmd/beacon-sandbox run --scenario s02-bash-command   # one scenario -- do this while iterating
go run ./cmd/beacon-sandbox run                               # the whole suite: all 7 scenarios, ~20 min
```

Pick the scenario matching what changed:

| You changed | Scenario |
|---|---|
| Command capture / exporter tool handling | `s02-bash-command` |
| File read or write signals | `s03-file-write` or `s04-file-read` |
| Prompt, session, token, or cost capture | `s01-hello` |
| Approval or permission handling | `s07-denied-tool` |
| Something broad, or preparing a PR | the whole suite (bare `run`) |

Other flags: `--repeat N` to tell flaky from broken, `--keep-sandbox` to leave the instance up for
debugging.

## Four things not to get wrong

1. **A run costs real money** — about $0.06 of sandbox plus a few cents of API per scenario, on
   the user's account. Say what you are about to run and roughly what it costs before spending
   it, and prefer a single `--scenario` while iterating. If you only changed *what counts as
   correct*, use `verify <run-dir>` instead — it re-judges collected artifacts offline and free.
2. **A change under `collector-builder/` needs the collector rebuilt.** The telemetry
   normalization compiles into `beacon-otelcol`, not the `beacon` CLI, so otherwise you verify
   the wrong binary and get a meaningless pass. `doctor` warns about this as
   `collector_freshness`.
3. **INCONCLUSIVE means the model never did the work** — retry the scenario; do not investigate
   Beacon.
4. **A FAIL may be a stale assertion, not a bug.** Read the failing expectation's `why` field
   against the verdict's action histogram before concluding anything.

`beacon-sandbox/AGENTS.md` explains each of these properly, plus `diff`, scenario authoring, and
how to self-test that the checks still have teeth.
