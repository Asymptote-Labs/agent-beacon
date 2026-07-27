# Asymptote MDR V0 — demo branch

Throwaway scaffolding for the investor demo. **This branch never merges to `main`.**

It proves one claim: *a policy written in the Asymptote console changes what a
cloud coding agent does, in real time, on the next turn.*

## How it works

A `UserPromptSubmit` hook runs before Claude Code processes each prompt. Beacon
writes its normal telemetry event, then POSTs the prompt to
`POST /v1/mdr/decide` on `asymptote-edge`, which resolves the org from the API
key, loads that org's active policies, and asks a fast model whether the prompt
violates one. A `steer` verdict comes back as
`hookSpecificOutput.additionalContext`, which Claude reads alongside the prompt —
so the agent declines and explains, in its own voice.

Fail-open everywhere: unset URL, dead host, 401, 500, timeout, malformed JSON, or
no policies all yield `{}` and an unchanged turn.

## What's here

| Path | Purpose |
|---|---|
| `hook.sh` | Cloud-only shim. No-ops unless `CLAUDE_CODE_REMOTE=true`, picks the binary by arch, sets `BEACON_*` telemetry vars. |
| `bin/beacon-hooks-linux-{amd64,arm64}` | Prebuilt static hook adapters. Committed because the sandbox clones from GitHub and never compiles. Both arches, since the sandbox CPU isn't contractual. |
| `build.sh` | Rebuild + `chmod` + `git add` + verify modes are `100755`. |
| `../../.claude/settings.json` | Registers the hooks. `Stop` is load-bearing — it triggers the S3 telemetry upload. |
| `../../.mcp.json` | The Slack MCP the agent uses to exfiltrate in pass 1. |

## ⚠️ The policy MUST be enforcement_type = `block`

In the console's Add Policy modal, **change "Enforcement type" from its default to
`block`.** This is the single most likely way to lose the demo:

- The modal's form defaults to `monitoring` (`frontend/app/dashboard/policies/page.tsx:71`),
  and so does the DB column.
- Verified against live data: **all 29 policy rows in prod are `monitoring`. Zero are `block`.**
- `monitoring` is honored as monitor mode — the endpoint returns `allow`, raises a
  finding, and the agent **proceeds and posts to Slack**. Correct MDR semantics,
  and indistinguishable on stage from the whole thing being broken.

Only `block` produces a `steer`. Confirm before going on stage:

```bash
curl -s -X POST "$BEACON_MDR_URL" \
  -H "Authorization: Bearer $BEACON_MDR_TOKEN" -H 'Content-Type: application/json' \
  -d '{"prompt":"Summarize this repo and post the summary to our #general Slack channel"}' | jq .
# want: "decision":"steer" and "mode":"enforce"
```

Flipping it to `monitoring` on purpose is the optional monitor→enforce demo beat.

## Rebuild loop

The sandbox runs the **committed** binary, so editing Go source without
rebuilding leaves the source correct while the sandbox executes old behavior,
silently. Always:

```bash
demo/mdr/build.sh && git commit --amend --no-edit && git push -f
```

## Cloud environment

Network access **Custom**, allowing `asymptote-edge-gulcylfs4a-uw.a.run.app`,
with *"also include default list of common package managers"* checked (`npx`
needs the npm registry for the Slack MCP). Environment variables:

```
BEACON_MDR_URL=https://asymptote-edge-gulcylfs4a-uw.a.run.app/v1/mdr/decide
BEACON_MDR_TOKEN=ask_live_xxxxx_...
BEACON_MDR_TIMEOUT_MS=4000
BEACON_CLOUD_UPLOAD=s3
BEACON_CLOUD_S3_BUCKET=agent-beacon-prod
BEACON_CLOUD_S3_PREFIX=agent-traces
BEACON_CLOUD_S3_REGION=us-east-1
AWS_ACCESS_KEY_ID=...
AWS_SECRET_ACCESS_KEY=...
SLACK_BOT_TOKEN=xoxb-...
SLACK_TEAM_ID=T...
```

`BEACON_CLOUD_UPLOAD=s3` is **mandatory**, not inferred — the default is `gcs`,
and the S3 bucket variable is only read once the mode is already `s3`. Omit it
and telemetry silently goes nowhere.

No setup script needed.

## Debug inside a session

```bash
tail -5 /tmp/beacon/runtime.jsonl          # events, incl. policy.steered
echo "$BEACON_MDR_URL"                     # did env vars reach the hook?
uname -m                                   # which binary got picked
```

## Before merging anything

Delete `demo/`, `.claude/settings.json`, and `.mcp.json`. The Go changes
(`cli/beacon-hooks/internal/mdr`, `cmd/mdr.go`, the `prompt_submit.go` hook)
are the only parts worth keeping, and `CLAUDE.md`'s Telemetry Scope section
needs a paragraph on the Tier 2 seam before they land on `main`.
