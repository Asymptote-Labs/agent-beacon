---
name: beacon-memory-distill
description: Turn recorded agent sessions (Beacon traces from Claude Code, Cursor, Codex, OpenCode, and other harnesses) into reviewed, reusable project memory. Scores selected traces, reads the source trace behind each high-signal candidate, drafts a grounded lesson, and approves it only after the user confirms. Use when the user asks to "learn from", "remember", "save the lesson from", or "turn into memory" a recent session, fix, or debugging effort, or asks to review Beacon memory candidates.
license: MIT
compatibility: Requires the Beacon CLI (beacon) on PATH with endpoint capture installed. Scoring calls the configured Jev evaluator over the network (hosted TypeSafe by default) and needs TYPESAFE_API_KEY or BEACON_JEV_API_KEY; every other step is local.
metadata:
  author: asymptote-labs
  homepage: https://docs.beacon.sh/concepts/cross-harness-memory
  version: "0.1.0"
---

# Beacon memory distill: traces to memory

Beacon captures what agents do in every supported harness. This skill runs the review
loop that turns a few of those sessions into **approved project memory** that any later
agent can recall, whichever harness it runs in:

1. Pick traces.
2. Score them with the evaluator (the one networked step, and only with consent).
3. For each candidate, read the source trace and draft the lesson.
4. The user confirms, edits, or rejects each draft.
5. Approve with the reviewed text.

The evaluator returns probabilities only. It says a trace looks reusable; it does not say
what the lesson is. **You write the lesson, from the trace, and the user approves it.**
Never approve a candidate with its placeholder body ("no lesson text was extracted").

Run every command from inside the repository the memory is for, or pass
`--project <path>`.

## Step 1: preflight

```bash
beacon version
beacon endpoint traces status --json
```

- If `beacon` is missing, stop and point the user to
  https://docs.beacon.sh/get-started/overview. Do not install it yourself.
- If the trace store is empty or stale, rebuild it (local only, never touches the log):
  `beacon endpoint traces reindex`.

## Step 2: pick traces

Use what the user named: a session, a date, a harness, or a topic. Otherwise list recent
traces and propose a short set (up to 10) that look like finished work with a correction,
a fix, or a non-obvious procedure.

```bash
beacon endpoint traces list --json --limit 20
beacon endpoint traces list --json --limit 20 -q "<topic terms>"
beacon endpoint traces search "<error text or file>" --json --limit 10
```

Prefer traces from this repository. Skip trivial sessions (a single question, an
abandoned attempt) since they cost evaluator calls and yield nothing.

Reading the list:

- Only `session:` IDs are sessions. IDs starting `event:` are single events with no
  session, almost always OTLP metric samples such as `claude_code.active_time.total`.
  They arrive every few seconds and sort to the top, so a short list can be all noise;
  raise `--limit` or use `--page` until you have sessions, and never select them.
- A session whose `updated_at` is within the last few minutes is still being written.
  Leave it for next time: a lesson drafted from half a session is usually wrong about
  how it ended.
- `repository` is null for many sessions, because not every event carries one. Such a
  session cannot tell you which project it belongs to; evaluate it only with an explicit
  `--project <path>` you can justify from the paths in its commands.

## Step 3: dry run, then ask

The dry run is local. It shows the traces selected and the estimated cost:

```bash
beacon memory evaluations run --dry-run --trace <trace-id>
beacon memory evaluations run --dry-run --limit 10 --harness <name> --since <rfc3339>
```

Before the real run, tell the user plainly and **wait for an explicit yes**:

- Each selected trace is sent, as a bounded and redacted projection, to the evaluator at
  `BEACON_JEV_ENDPOINT` if that is set, otherwise to the hosted TypeSafe endpoint.
- It costs about the estimate the dry run printed.
- Nothing is approved or written into memory by this step.

Check for a key without printing it:

```bash
test -n "${TYPESAFE_API_KEY:-}${BEACON_JEV_API_KEY:-}" && echo "evaluator key present" || echo "no evaluator key"
```

With no key, stop and tell the user to export `TYPESAFE_API_KEY` (or point
`BEACON_JEV_ENDPOINT` at their organization's compatible evaluator). Never ask them to
paste a key into the chat, and never pass `--jev-api-key` on the command line. If the user
or their organization does not allow external evaluation, stop here: candidates come only
from evaluations.

## Step 4: score

Repeat the exact selection the user approved, without `--dry-run`:

```bash
beacon memory evaluations run --trace <trace-id> --json
beacon memory evaluations run --limit 10 --harness <name> --since <rfc3339> --json
```

A trace becomes a candidate only when `task_success` is at least 0.50 and the mean score
is at least 0.60. Report how many were scored and how many became candidates. The text
output names why each trace was not promoted; do not try to overturn that.

## Step 5: draft a lesson for each candidate

```bash
beacon memory candidates list --state candidate --json
beacon memory candidates show <candidate-id> --json
```

For each candidate, read its evidence trace. Filter to the event types that carry
substance and write the JSON to a file before reading it:

```bash
beacon endpoint traces show <trace-id> --json --event-type user_message,tool_call,command,tool_result,approval,error --limit 150 > trace-1.json
beacon endpoint traces show <trace-id> --json --event-type user_message,tool_call,command,tool_result,approval,error --offset 151 --limit 150 > trace-2.json
beacon endpoint traces show <trace-id> --json --around-event <n> --before 5 --after 5
```

How to read what comes back:

- `--event-type` filters the coarse `type` field (`user_message`, `tool_call`,
  `command`, `tool_result`, `approval`, `error`, `session`, `token_usage`, `metric`).
  Action names such as `prompt.submitted` match nothing.
- Filter every read. Hooks and OTLP both record, so an unfiltered session is mostly
  `token_usage` and `session` events, and an unfiltered `traces show` on a large log can
  take minutes and exceed a tool timeout. Filtered reads return in seconds.
  `range.total_events` counts the filtered events, so it tells you how much is left.
- The log usually holds no assistant text and no tool output. For Claude Code, a
  session's substance is its `user_message` events (the only ones with a `content`
  object), its `tool_call` and `command` events (tool name and command line), and
  whether a `tool_result` was a failure. The strongest evidence for a lesson is a
  failed command followed by a different command that succeeded.
- A `user_message` with `content.included` false, or only a hash, means Beacon kept
  metadata only. You cannot draft from a hash; say the trace was unreadable.

Work out, from the events themselves:

- What the task was (the first prompt).
- What went wrong or was non-obvious: a failed command, a correction from the user, a
  retry, a wrong assumption that got reversed.
- What finally worked, with the specific command, file, flag, or order of steps.
- How the agent confirmed it worked (a passing test, a clean build).

Then check whether it is already known:

```bash
beacon memory list --json -q "<two or three distinctive terms>"
```

Draft each lesson in the shape given in [references/lesson-quality.md](references/lesson-quality.md):
title, kind, applicability, body, tags, and the event numbers it rests on. Read that file
before writing your first draft.

Recommend **reject** when the trace has no transferable lesson (it was routine, too
specific to one moment, or the fix was later reverted), and **supersede** when an
existing memory already says the same thing.

## Step 6: review with the user

Show every draft at once, each with its recommendation (approve, reject, or supersede),
the candidate ID, and the trace events that support it. Ask the user to confirm or edit
each one. Do not approve, reject, or supersede anything they have not confirmed. Silence
is not approval.

## Step 7: record the decisions

Approve with the reviewed text. Pass the body on stdin so quoting stays safe:

```bash
beacon memory candidates approve <candidate-id> \
  --title "<title>" \
  --kind <workflow|correction|debugging_pattern|gotcha|convention> \
  --applicability "<when this applies>" \
  --tag <tag> --tag <tag> \
  --reason "Reviewed with the user; lesson drafted from trace events <n>-<m>" \
  --body-file - --json <<'LESSON'
<body>
LESSON
```

```bash
beacon memory candidates reject <candidate-id> --reason "<why it is not reusable>"
beacon memory candidates supersede <candidate-id> --replacement <memory-id> --reason "<which memory already covers it>"
```

Confirm what was stored with `beacon memory show <memory-id>`, then summarize: approved,
rejected, superseded, and the new memory IDs. Mention that any agent in any harness can
now recall these with the `beacon-memory-recall` skill or the Beacon MCP tools, and that a
memory worth loading automatically can be installed as a skill with
`beacon-memory-promote`.

## Boundaries

- The evaluation run is the only networked step. Run it only after the dry run and the
  user's explicit yes, and only on the selection they approved.
- Memory is shared with every future agent in this project. Never put secrets, tokens,
  credentials, internal hostnames, customer data, or personal information into a title,
  body, tag, or reason, even when the trace contains them. Describe them instead ("the
  staging API key from 1Password").
- Quote trace content sparingly and only to support a draft.
- Never edit `memory.db` or the runtime log directly. Use the CLI.
