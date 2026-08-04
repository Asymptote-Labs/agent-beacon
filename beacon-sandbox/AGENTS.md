# beacon-sandbox: agent guide

Instructions for a coding agent asked to verify a Beacon change end to end.

This tool rents a disposable Linux sandbox, installs the Beacon under test, runs a **real Claude
Code session** inside it, and checks whether Beacon recorded what the agent actually did. Human
documentation is in `README.md` and on the docs site; this file is the operating manual.

## Always start here

```bash
cd beacon-sandbox
go run ./cmd/beacon-sandbox doctor
```

`doctor` is free, needs no sandbox, and catches the failure modes that otherwise appear
mid-run as something else entirely. Add `--fix` to let it download the collector binary.

If `doctor` reports FAIL, apply the printed `fix` and rerun it. Do not proceed to `run` with a
failing check — the run will fail later and more confusingly.

`doctor --json` gives machine-readable output with a top-level `ready` boolean.

### What you can fix yourself, and what you must not

Resolve these two directly:

```bash
cd cli/beacon && make build-linux-amd64                          # beacon_binary
cd beacon-sandbox && go run ./cmd/beacon-sandbox doctor --fix    # collector_binary (downloads it)
```

**Do not attempt the other two.** They need the user, and trying them yourself wastes a turn or
hangs outright:

- **`modal_auth`** — `modal token new` completes through an authenticated *web session*, so it
  opens a browser and blocks. Running it yourself hangs until timeout. Ask the user to run it,
  suggesting `! pip install modal && modal token new` so the output lands in the conversation. In
  CI, `MODAL_TOKEN_ID` and `MODAL_TOKEN_SECRET` work non-interactively instead.
- **`anthropic_credential`** — never invent, echo, or write a key. Ask which path the user wants:
  `ANTHROPIC_API_KEY`, `--api-key-command CMD`, or `--modal-secret NAME` (most secure; the value
  never enters this process, at the cost of the artifact leak check reporting *unverified*).

## Before spending money

A run costs roughly **$0.06 of sandbox time plus a few cents of Anthropic API usage**. That is
real money on the user's account, so:

- Tell the user what you are about to run and roughly what it costs.
- Use `--scenario <id>` while iterating. Only run the full suite when asked or before a PR.
- Never loop the suite unattended, and never use `--repeat` without being asked.
- If you only changed *what counts as correct*, use `verify` instead — it is free.

## Choosing what to run

| You changed | Run |
|---|---|
| Command capture / exporter tool handling | `--scenario s02-bash-command` |
| File read or write signals | `--scenario s03-file-write` or `s04-file-read` |
| Prompt, session, token, or cost capture | `--scenario s01-hello` |
| Approval or permission handling | `--scenario s07-denied-tool` |
| Something broad, or preparing a PR | the whole suite (no `--scenario`) |

```bash
go run ./cmd/beacon-sandbox run --scenario s02-bash-command   # one scenario
go run ./cmd/beacon-sandbox run                               # the whole suite: all 7, ~20 min, ~$0.50
```

`run` with no `--scenario` is the suite command. Other flags: `--repeat N` to tell flaky from
broken, `--keep-sandbox` to leave the instance up for debugging.

## The trap that matters most

**If the change touches `collector-builder/`, the collector must be rebuilt.** Telemetry
normalization compiles into `beacon-otelcol`, not the `beacon` CLI. Verifying an exporter change
against a downloaded or previously built collector produces a passing run that proves nothing
about the change.

`doctor` warns about this (`collector_freshness`). When it does, rebuild:

```bash
go install go.opentelemetry.io/collector/cmd/builder@v0.121.0
cd collector-builder && mkdir -p dist
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 "$(go env GOPATH)/bin/builder" --config builder.yaml
cp dist/beacon-otelcol/beacon-otelcol dist/beacon-otelcol/linux_amd64/beacon-otelcol
```

This has already caused one wasted investigation: a stale collector made an
already-fixed bug still look broken.

`collector_freshness` covers three ways the binary can be wrong: uncommitted changes under
`collector-builder/`, a downloaded release binary with exporter changes committed since that
release, and a locally built binary older than its sources. It also reports a release tag the clone
cannot resolve as *unknowable* rather than fresh — if you see that, run `git fetch --tags` or
rebuild. Rebuilding locally clears the warning; provenance is tied to the binary on disk, so the
warning can always be satisfied by doing what it asks.

## Reading the result

Three outcomes, and the third is what makes the tool trustworthy:

- **PASS** — Beacon captured what the scenario planted. Exit 0.
- **FAIL** — an expectation, invariant, or safety check failed. Read the `why` field on the failing
  finding before concluding anything. Exit 1.
- **INCONCLUSIVE** — the model never did the requested work, so there was nothing to capture.
  **Retry the scenario. Do not investigate Beacon.** Exits 1 when nothing else passed, so do not
  read a zero exit as the only success signal.

A fail-severity finding always beats INCONCLUSIVE: an idle agent does not excuse a leaked
credential or a corrupt line.

### A WARN means a check could not run

Not that it passed. You will see these on healthy runs and they are not failures, but they are also
not clean bills of health:

| Warning | What is unverified |
|---|---|
| `sentinel ... could not be read` | Whether the agent acted. Expectations are judged without that excuse. |
| `the agent's own result could not be read` | Same, for a scenario with no sentinel. |
| `cannot check for ANTHROPIC_API_KEY` | The leak check had no value to search for. |
| `the argv scan did not run` | Credential handling for that run. |
| `service probe could not run` | The service half of the host guard; files still passed. |

If you are reporting a run to the user, say which halves were verified and which were not. Silence
in this tool means verified-clean, so never paraphrase a WARN as a pass.

### A FAIL is not automatically a Beacon bug

Check, in this order:

1. **Is the expectation stale?** Scenarios encode what Beacon *should* do. When Beacon's
   behavior legitimately improves, an old assertion can become wrong. Three scenarios had to be
   rewritten after v1.0.6 for exactly this reason — they asserted `tool.invoked` *because* that
   was the broken classification. Compare the failing expectation's `why` against the actual
   action histogram in the verdict.
2. **Was the right binary tested?** See the trap above.
3. **Only then** treat it as a capture gap in Beacon.

Each finding carries evidence pointers like `runs/<id>/runtime.jsonl:88`. Read that line before
reporting anything.

## Iterating for free

```bash
go run ./cmd/beacon-sandbox verify runs/<run-dir>/         # re-judge collected artifacts
go run ./cmd/beacon-sandbox diff runs/<before>/ runs/<after>/
```

`verify` is a pure function of what is on disk: no sandbox, no model, no cost. Capture
a session once, then re-check it as often as you like while adjusting expectations.

`diff` compares capability fingerprints across runs. **It cannot distinguish "action removed" from
"action reclassified"** — when Beacon starts classifying an event more precisely, the old action
name shows up as a REGRESSION even though nothing was lost. Read the improvements alongside it.

## Self-testing the checks

A check that cannot fail is worse than no check. To confirm the checks still have teeth:

```bash
go test ./...                                                       # hermetic; no credentials, no cost
go run ./cmd/beacon-sandbox verify --mutate corrupt-line runs/<id>/  # a PASS must become FAIL
```

Other mutations: `drop-action:<action>`, `drop-commands`, `plant-secret`.

Every mutation refuses to be a no-op: an empty log, or an action the log does not contain, errors
rather than reporting a self-test that proved nothing.

`plant-secret` needs no credential of its own. It plants a unique synthetic value and the leak check
is told to search for that, so it works on any run directory regardless of which credential the run
used, or whether one is set at all.

## Writing or editing a scenario

```yaml
id: s02-bash-command
prompt: >-
  Run exactly this shell command and report its output: echo {{canary}} | tee {{sentinel}}
sentinel: /home/agent/work/s02.sentinel   # exists ⇒ the agent really acted
expect:
  - action: command.executed
    contains: ["{{canary}}"]
    fields: ["command.command"]
    why: >-
      Threat rules match scalar leaves, so e.command.command IS the detection surface for the
      rules/risky-command/ category.
```

Rules:

- `{{canary}}` is a unique per-run marker. Matching it is exact because the harness chose it.
- `sentinel` is what makes a missing event unambiguous. Without one, a missing expectation cannot
  be distinguished from an inactive agent and is treated as a failure.
- Every expectation needs a `why`. A failure that cannot explain what it protected is not
  actionable, and a test asserts this.
- `optional: true` records an expectation without failing the run — for a known gap you want
  visible but not blocking.
- Validate without spending anything: `go test ./scenario/`.

## Environment constraints worth knowing

- The sandbox is **Linux/amd64 only**. This cannot verify the macOS build.
- Claude Code must run as a **non-root** user; it refuses `--dangerously-skip-permissions` as root.
- Switching user with `su -` strips the injected credential (login shell resets the environment).
  The code uses `su -p`; a test pins this.
- Telemetry is asynchronous, so the runner waits for the log to go quiet rather than sleeping a
  fixed amount. Occasional flakiness is expected — `--repeat` distinguishes flaky from broken.
- The credential argv check samples the process table *during* the session, but coarsely: a short
  session yields single-digit samples. Read `argv_samples` in `meta.json` alongside the result —
  `secret_in_argv=false` means "not seen in N samples", not "provably never present".
- What is verified is the `beacon ci exec` collector path, which is how Beacon collects in GitHub
  Actions and cloud agent sandboxes. Persistent `endpoint install` is not covered.

## Reporting back

State: which scenario ran, the outcome, and for a FAIL the specific expectation with its `why`
and the evidence line. If the result was INCONCLUSIVE, say the agent did not perform the work and
that a retry is needed — not that Beacon is broken.
