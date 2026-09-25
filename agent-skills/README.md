# Beacon memory skills

Agent Skills that turn Beacon's cross-harness traces into reviewed project memory, and
bring that memory back to any agent in any harness.

| Skill | What it does | Network |
|-------|--------------|---------|
| `beacon-memory-recall` | Before a task, fetch approved project memory (MCP `get_memory_context`, or `beacon memory list`). | None |
| `beacon-memory-distill` | Pick traces, score them with the evaluator after a dry run and the user's yes, read each candidate's source trace, draft a grounded lesson, and approve it with the user. | Evaluator call only, with consent |
| `beacon-memory-promote` | Install an approved memory as `.agents/skills/<slug>/SKILL.md` so harnesses load it automatically. | None |

All three need the [Beacon CLI](https://docs.asymptotelabs.ai/get-started) with endpoint
capture installed. The plugin also registers the local `beacon mcp serve` server for
harnesses that take MCP servers from plugins.

## Install

| Harness | Command |
|---------|---------|
| Claude Code | `/plugin marketplace add asymptote-labs/agent-beacon`, then `/plugin install beacon@beacon` |
| Codex | `codex plugin marketplace add asymptote-labs/agent-beacon` |
| GitHub Copilot CLI | `copilot plugin marketplace add asymptote-labs/agent-beacon`, then `copilot plugin install beacon@beacon` |
| Factory Droid | `droid plugin marketplace add asymptote-labs/agent-beacon`, then `droid plugin install beacon@beacon` |
| Gemini CLI | `gemini extensions install https://github.com/asymptote-labs/agent-beacon` (see the note below) |
| Any skills-capable agent (Cursor, OpenCode, Amp, goose, Cline, Windsurf, Kiro, …) | `npx skills add asymptote-labs/agent-beacon` |

## Layout

This directory is a self-contained plugin root. Every harness reads the same `skills/`.

```text
agent-skills/
  plugin.json                  Agent Plugins 1.0 manifest (Codex, Cursor, Copilot, VS Code, Kiro)
  mcp.json                     Agent Plugins MCP config
  .claude-plugin/plugin.json   Claude Code (also read by Factory Droid)
  .mcp.json                    Claude Code MCP config
  gemini-extension.json        Gemini CLI extension manifest
  skills/<name>/SKILL.md       agentskills.io format, shared by all
../.claude-plugin/marketplace.json
                               Marketplace entry, read by Claude Code, Codex, Copilot,
                               Factory Droid, and the skills CLI
```

`cli/beacon/cmd/agent_skills_test.go` fails when a skill names a `beacon` command or flag
that does not exist, when frontmatter breaks the Agent Skills spec, or when the manifests
disagree on name or version.

## Releasing a new version

1. Bump `version` in `plugin.json`, `.claude-plugin/plugin.json`, `gemini-extension.json`,
   the marketplace entry, and each skill's `metadata.version`. The Go test enforces this.
2. Run `go test ./cmd -run AgentSkills` in `cli/beacon`, and `claude plugin validate --strict agent-skills`.
3. Merge to `main`. Marketplaces that track the repository pick up the change. Listings
   that pin a commit (the Claude community marketplace) need that commit updated.

## Publishing checklist

| Destination | How | Status |
|-------------|-----|--------|
| Claude Code community marketplace | Submit the repository at https://platform.claude.com/plugins/submit. Listings are pinned to a commit. | To do |
| Cursor Marketplace | Submit at https://cursor.com/marketplace/publish (manual review, needs a public repo with a README). | To do |
| Kiro Powers | Submit at https://kiro.dev/powers/submit/ (Agent Plugins format). | To do |
| OpenAI plugin directory (ChatGPT and Codex) | Submit through OpenAI's plugin submission portal. | To do |
| GitHub `awesome-copilot` | Open a pull request adding the plugin. | To do |
| skills.sh | Nothing to submit. It lists skills by install count. | Automatic |
| SkillsMP | Nothing to submit. It crawls public repositories with at least two stars. | Automatic |
| Gemini CLI gallery | Needs `gemini-extension.json` at the root of a repository with the `gemini-cli-extension` topic. | Needs a mirror repo (below) |

### Gemini and the mirror repository

Gemini CLI reads `gemini-extension.json` from the root of the repository it installs, and
its gallery crawls repositories by topic. The Beacon monorepo is not a good extension
root, because installing it would clone everything. Mirror this directory verbatim to a
small repository (for example `asymptote-labs/beacon-skills`), tag the
`gemini-cli-extension` topic there, and use that repository for the Gemini gallery, the
Cursor and Kiro submissions, and anywhere else that wants the plugin at the repository
root. Everything in this directory already works as a repository root.
