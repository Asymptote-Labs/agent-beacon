---
name: beacon-memory-promote
description: Install approved Beacon project memory as an Agent Skill in the repository (.agents/skills/<slug>/SKILL.md), so every skill-capable harness loads the lesson automatically without a memory lookup. Use when the user asks to "make this a skill", "promote", "install", or "share" an approved Beacon memory, or wants the team's agents to always follow a learned workflow or convention.
license: MIT
compatibility: Requires the Beacon CLI (beacon) on PATH. Local only. Writes files inside the current project.
metadata:
  author: asymptote-labs
  homepage: https://docs.asymptotelabs.ai/cli/memory
  version: "0.1.0"
---

# Beacon memory promote: memory to skill

Approved memory is recalled on demand. A **skill** is loaded by the harness itself
whenever its description matches the task, and when it is committed to the repository it
reaches every teammate and every harness that reads `.agents/skills` (Codex, Cursor,
Gemini CLI, GitHub Copilot, OpenCode, Amp, goose, and others; Claude Code reads
`.claude/skills`).

Promote only what earns that. Each installed skill adds to what every agent considers on
every task.

## What to promote

Good candidates:

- `workflow` and `convention` memories that apply to many tasks in this repository.
- A `debugging_pattern` for a failure that keeps recurring.

Leave in memory, where recall finds them when needed:

- One-off gotchas tied to a single file or incident.
- Anything that duplicates the repository's CLAUDE.md, AGENTS.md, or contributing docs.
  Suggest editing those docs instead.

## Step 1: choose the memory

```bash
beacon memory list --limit 25
beacon memory show <memory-id> --json
```

The skill commands take the **candidate** ID, which is the `candidate_id` field of the
memory.

## Step 2: preview

```bash
beacon memory skills preview <candidate-id>
```

Check the preview with the user:

- The `description` is the title plus the memory's applicability. It decides when a
  harness loads the skill, so it must name the trigger (a command, path, or error). If it
  is vague, the fix is a better memory: draft a sharper title and applicability, approve a
  replacement through the `beacon-memory-distill` workflow, and supersede the old one.
- The body must hold no secrets, hostnames, or personal details. It is about to become a
  file that is likely to be committed.

## Step 3: install, after the user confirms

```bash
beacon memory skills install <candidate-id>
```

This writes `.agents/skills/<slug>/SKILL.md` in the project (or the `--project` path). It
refuses to overwrite an existing file; pass `--force` only when the user wants to replace
the generated skill, and never over a file a person has edited by hand without asking.

For Claude Code, which reads `.claude/skills` rather than `.agents/skills`, offer to
link the skill there:

```bash
mkdir -p .claude/skills && ln -s ../../.agents/skills/<slug> .claude/skills/<slug>
```

## Step 4: hand off

Show the path and remind the user that:

- The file carries Beacon provenance under `metadata` (`beacon_memory_id`,
  `beacon_candidate_id`), so a reviewer can trace it back to the approved memory and the
  source session.
- It becomes team-wide only when they commit it. Do not commit or push unless they ask.
- If the memory is later superseded, the installed skill is not updated automatically.
  Re-run preview and install with `--force` from the replacement candidate.

## Boundaries

- Never install without the user seeing the preview and confirming.
- Never approve or edit memory here. Use `beacon-memory-distill`.
- Do not write to user-level skill directories (such as `~/.agents/skills`). Beacon memory
  is scoped to a project.
