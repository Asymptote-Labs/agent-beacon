---
name: beacon-memory-recall
description: Retrieve reviewed project memory that Beacon distilled from earlier agent sessions in any harness (Claude Code, Cursor, Codex, OpenCode, and others) before starting work. Use at the start of a non-trivial task in a repository, when the user asks "have we seen this before", "what did we learn last time", or "how did we fix this in Cursor/Codex", or when a build, test, or deploy step fails in a way that may have a known fix.
license: MIT
compatibility: Requires the Beacon CLI (beacon) on PATH with endpoint capture installed. Reads only local state and makes no network calls.
metadata:
  author: asymptote-labs
  homepage: https://docs.asymptotelabs.ai/concepts/cross-harness-memory
  version: "0.1.0"
---

# Beacon memory recall

Beacon records what agents do across every supported harness into a local log. Through
the `beacon-memory-distill` workflow, a person reviews selected sessions and approves the
reusable lessons into **project memory**. This skill reads that memory back, so you start
the task knowing what earlier agents, in this harness or any other, already learned about
this repository.

Approved memory has been reviewed by a person. Raw traces have not. Keep the two apart in
everything you say.

## When to recall

- Before planning a non-trivial change in a repository you have not worked in during this
  session.
- When a command fails in a way that could have a known fix (flaky tests, packaging,
  environment setup, release steps).
- When the user asks what was learned, tried, or decided before.

Skip it for trivial edits, and recall at most once per task unless the task changes
direction.

## Step 1: check that Beacon is available

```bash
beacon version
```

If `beacon` is not found, tell the user that project memory is unavailable because the
Beacon CLI is not installed, point them to https://docs.asymptotelabs.ai/get-started, and
continue the task without memory. Do not install it yourself.

## Step 2: retrieve memory

Prefer the Beacon MCP tools when this session has them. They are named `search_memory`,
`get_memory`, and `get_memory_context`, usually under a server called `beacon`.

1. Call `get_memory_context` with `task` set to a one-sentence description of the current
   task. It returns up to five relevant approved memories for the current project.
2. Call `get_memory` with an `id` from that result when you need the full body and its
   evidence.

Without the MCP tools, use the CLI from inside the repository:

```bash
# Up to five memories matching the task's key terms (every term must match)
beacon memory list --json --limit 5 -q "<two or three distinctive terms>"

# Everything approved for this project, newest first
beacon memory list --limit 25

# One memory in full, with its source traces
beacon memory show <memory-id>
```

Search terms are ANDed, so start with two or three distinctive words (a tool, a file, an
error string) and drop terms if nothing matches. `--kind` narrows to `workflow`,
`correction`, `debugging_pattern`, `gotcha`, or `convention`.

Memory is scoped to the project resolved from the current directory. Run from inside the
repository, or pass `--project <path>`.

## Step 3: use what you found

- Apply a memory only when its applicability matches the current situation. Memories are
  lessons from specific sessions, not project policy. If one conflicts with the user's
  instructions or the repository's own docs (CLAUDE.md, AGENTS.md, README), follow the
  user and the docs, and mention the conflict.
- When a memory changes what you do, say so in one line and cite its ID, for example:
  "Using Beacon memory `memory_4f2c…`: package smoke needs build-pkg.sh first."
- If nothing relevant comes back, carry on. Do not report an empty result unless the user
  asked.

## Raw history, only when asked

When the user explicitly asks what happened in an earlier session ("what did Cursor do
yesterday on the release script?"), you can search raw traces:

```bash
beacon endpoint traces search "<terms>" --json --limit 10
beacon endpoint traces show <trace-id> --json --limit 200
```

Present that as unreviewed history, never as approved memory. Traces can contain prompts,
command output, and file contents: quote only what the user needs, and never repeat
anything that looks like a credential. If the history holds a lesson worth keeping,
suggest the `beacon-memory-distill` workflow so a person can review it.

## Boundaries

- Read-only. Never approve, reject, or edit memory from this skill.
- Never run `beacon memory evaluations run` here. That step sends trace data to an
  evaluator over the network and belongs to `beacon-memory-distill`, where the user
  consents to it.
