# beacon-sandbox

Verifies Beacon against **real** Claude Code activity. It rents a disposable Linux sandbox from
[Modal](https://modal.com/docs/guide/sandboxes), installs the Beacon build from your working tree,
runs an actual agent session inside it, and checks whether Beacon's runtime log contains what the
agent really did. Nothing runs on your machine, and the sandbox is destroyed afterwards.

Full documentation: **[Verify Beacon In A Sandbox](https://docs.asymptotelabs.ai/contributing/beacon-sandbox)**.
Coding agents should read [`AGENTS.md`](AGENTS.md) — it is the operating manual.

## Quick start

Needs a Modal account (the free tier includes monthly credits) and an Anthropic API key with
credit, because the sandbox runs a real agent session.

```bash
pip install modal && modal token new
export ANTHROPIC_API_KEY=sk-ant-...

cd cli/beacon && make build-linux-amd64       # the Beacon under test
cd ../../beacon-sandbox
go run ./cmd/beacon-sandbox doctor --fix      # checks prerequisites, fetches the collector
go run ./cmd/beacon-sandbox run --scenario s02-bash-command   # one scenario, ~3 min
go run ./cmd/beacon-sandbox run                               # the whole suite: 7 scenarios, ~20 min
```

`modal token new` completes through a browser session, so it needs a human; everything else
`doctor` either resolves or explains. `--fix` verifies the collector it downloads against the
release's `checksums.txt` before installing it, and refuses a release that publishes none.

`doctor` prints the exact fix for anything missing, so it is both the right first move and the
right thing to rerun when a run behaves strangely.

## Why it works despite the agent being unpredictable

An AI session does something slightly different every time, so there is no fixed expected output
to compare against. Two cheap ideas replace one expensive oracle:

- **A canary** — a unique marker in the prompt that Beacon's log must contain. The match is exact,
  because the sandbox chose the string.
- **A sentinel** — a file the requested work leaves behind.

The sentinel must *contain* the canary, not merely exist — only this run's agent could have put it
there, whereas a fixture could have created the file. If it is there the agent acted, so a missing
event is a genuine capture gap; if not, the run is `INCONCLUSIVE` and gets retried rather than
blamed on Beacon. Without that, "Beacon dropped the event" and "the model declined the task" look
identical.

Some scenarios leave no artifact at all (`s01-hello` and `s07-denied-tool` assert on prompt, token,
cost and approval signals), so for those the agent's own success report plays the sentinel's role.

A run also distinguishes **"the check passed"** from **"the check could not run"**. An unreadable
probe, a credential this process never held, or a service listing that failed each produce an
explicit *unverified* warning. Silence means verified-clean, never "we did not look" — and an
all-`INCONCLUSIVE` run exits non-zero, so a script cannot mistake "nothing verified" for success.

Marker checks prove presence, not completeness: they confirm the planted thing was captured, not
that nothing else was missed.

## Commands

| Command | Purpose |
|---------|---------|
| `doctor [--fix] [--json]` | Check prerequisites; download the collector |
| `run [--scenario ID] [--repeat N] [--keep-sandbox]` | Capture a session and judge it |
| `verify <run-dir> [--mutate MODE]` | Re-judge collected artifacts — free, instant, offline |
| `diff <before> <after>` | Compare capability fingerprints across runs |
| `clean` | Remove local run artifacts |

**`verify` is the loop to iterate in.** It is a pure function of what is on disk: no sandbox, no
model, no cost. Capture a session once, then re-check it as often as you like while
adjusting what counts as correct. `run` is only for capturing fresh behavior.

A run costs roughly $0.06 of sandbox time plus a few cents of API usage.

Credentials, in the order they resolve: `--modal-secret NAME` (most secure — the value never
touches this process, at the cost of the artifact leak check degrading to *unverified*),
`--api-key-command CMD`, then `ANTHROPIC_API_KEY`.

## Layout

```
credentials/  resolves the Anthropic key: provider secret, helper command, or env var
sandbox/      Provider interface and the Modal implementation -- the only Modal-aware package
image/        base layer (distro + pinned Claude Code) and Beacon artifact resolution
scenario/     declarative scenario format
scenarios/    the shipped suite
runner/       runs one scenario and collects artifacts; stops before judging
check/        invariants, expectation matcher, verdict, capability fingerprint
hostguard/    proves your own Beacon state was untouched
```

## Trusting the checks

A check that cannot fail is worse than no check, because it turns "we did not look" into "we
verified". So the checks are tested against deliberately damaged logs: a dropped command event must
fail exactly that expectation, a corrupted line must trip the parse invariant, a planted credential
must trip the leak check, an absent sentinel must yield `INCONCLUSIVE` rather than `FAIL`, and an
unavailable credential must report *unverified* rather than clean.

```bash
go test ./...                                                        # hermetic; no credentials, no cost
go run ./cmd/beacon-sandbox verify --mutate corrupt-line runs/<id>/  # a PASS must become FAIL
```

Every mutation refuses to be a no-op, so a self-test can never pass without having damaged
anything. `plant-secret` plants a unique synthetic credential and the leak check is told to search
for it, so it works on any run directory with any credential arrangement, including none.

Tests need no Modal account, no API key, and no sandbox, so they cost nothing. (They still
resolve Go modules, like every other test suite in this repo.)

Two Beacon behaviors the matcher models deliberately, because ignoring either produces false
failures: `IsDuplicateEndpointEvent` collapses identical events written within **two seconds**, so
a repeated command yields one event; and token totals must come from `beacon token-usage --json`
rather than summing the raw JSONL, because `dedupeOverlappingChannels` zeroes metric-channel usage
when the log channel already reported it.

## Limitations

Modal is Linux/amd64 only, so this **cannot verify the macOS build** — launchd, the signed `.pkg`,
and notarization are out of reach. What it exercises is the `beacon ci exec` collector path, which
is how Beacon collects in GitHub Actions and cloud agent sandboxes. The docs page has the full
list.

This is contributor tooling. Beacon's own endpoint and hook execution stay local-only with no
network dependency; only this sandbox reaches out, and only when you run it.
