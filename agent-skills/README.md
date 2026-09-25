# Beacon memory skills

Agent Skills that turn Beacon's cross-harness traces into reviewed project memory, and
bring that memory back to any agent in any harness.

| Skill | What it does | Network |
|-------|--------------|---------|
| `beacon-memory-recall` | Before a task, fetch approved project memory (MCP `get_memory_context`, or `beacon memory list`). | None |
| `beacon-memory-distill` | Pick traces, score them with the evaluator after a dry run and the user's yes, read each candidate's source trace, draft a grounded lesson, and approve it with the user. | Evaluator call only, with consent |
| `beacon-memory-promote` | Install an approved memory as `.agents/skills/<slug>/SKILL.md` so harnesses load it automatically. | None |

All three need the [Beacon CLI](https://docs.beacon.sh/get-started/overview) with endpoint
capture installed. The plugin also registers the local `beacon mcp serve` server for
harnesses that take MCP servers from plugins.

## Install

| Runtime | Install |
|---------|---------|
| Claude Code | `/plugin marketplace add asymptote-labs/agent-beacon`, then `/plugin install beacon@beacon` |
| Claude Cowork | Customize → Plugins → Add marketplace, then enter `asymptote-labs/agent-beacon` |
| Codex (CLI and desktop) | `codex plugin marketplace add asymptote-labs/agent-beacon`, then `codex plugin add beacon@beacon` |
| GitHub Copilot CLI | `copilot plugin marketplace add asymptote-labs/agent-beacon`, then `copilot plugin install beacon@beacon` |
| VS Code | Add `asymptote-labs/agent-beacon` to the `chat.plugins.marketplaces` setting, then install `beacon` |
| Cursor | An admin imports `https://github.com/asymptote-labs/agent-beacon` under Settings → Plugins → Team Marketplaces; users install `beacon` from Customize |
| Factory Droid | `droid plugin marketplace add asymptote-labs/agent-beacon`, then `droid plugin install beacon@beacon` |
| Grok Build | `grok plugin marketplace add asymptote-labs/agent-beacon`, then `grok plugin install beacon` |
| Devin CLI | `devin plugins install asymptote-labs/agent-beacon#agent-skills` |
| Qwen Code | `qwen extensions install asymptote-labs/agent-beacon:beacon` |
| Oh My Pi | `omp plugin marketplace add asymptote-labs/agent-beacon`, then `omp plugin install beacon@beacon` |
| OpenClaw | `openclaw plugins install beacon --marketplace asymptote-labs/agent-beacon` |
| Kimi Code | From a clone: `/plugins install ./agent-beacon/agent-skills` |
| Kiro | Powers → Add Custom Power → Import power from GitHub, then enter `https://github.com/asymptote-labs/agent-beacon/tree/main/agent-skills` |
| Pi | `pi install git:github.com/asymptote-labs/agent-beacon` |
| Prime Agent | `prime-agent package install git:github.com/asymptote-labs/agent-beacon` |
| Gemini CLI | `gemini skills install https://github.com/asymptote-labs/agent-beacon.git --path agent-skills/skills` |
| Hermes Agent | `hermes skills install asymptote-labs/agent-beacon/agent-skills/skills/<skill>` for each skill |
| Antigravity CLI, goose, OpenCode, OpenHands, and other skills-capable agents | `npx skills add asymptote-labs/agent-beacon`, optionally with `-a <agent>` |
| Cline | Copy `agent-skills/skills/*` into `.cline/skills` |
| DeepSeek Harness, Muse Code, Senpi | Copy `agent-skills/skills/*` into `.agents/skills` |
| fx | `/skills install asymptote-labs/agent-beacon` |

The plugin routes also register the local Beacon MCP server. Package, power, and skills
routes install the skills only; the recall skill then falls back to `beacon memory list`.

## Layout

This directory is a self-contained plugin root. Every harness reads the same `skills/`.

```text
agent-skills/
  plugin.json                  Agent Plugins 1.0 manifest (Codex, Copilot, VS Code, Kiro, Devin)
  mcp.json                     Agent Plugins MCP config
  .claude-plugin/plugin.json   Claude Code (also read by Droid, Devin, OpenClaw, Qwen)
  .mcp.json                    Claude Code MCP config
  .cursor-plugin/plugin.json   Cursor
  kimi.plugin.json             Kimi Code
  gemini-extension.json        Gemini CLI extension manifest
  skills/<name>/SKILL.md       agentskills.io format, shared by all
../.claude-plugin/marketplace.json
                               Marketplace entry, read by Claude Code, Codex, Copilot,
                               Droid, Grok, Qwen, Oh My Pi, OpenClaw, and the skills CLI
../.cursor-plugin/marketplace.json
                               Cursor's marketplace entry
../package.json                Pi and Prime Agent package manifest (pi.skills)
```

`cli/beacon/cmd/agent_skills_test.go` fails when a skill names a `beacon` command or flag
that does not exist, when frontmatter breaks the Agent Skills spec, or when the manifests
disagree on name or version.

## Releasing a new version

1. Bump `version` in `plugin.json`, `.claude-plugin/plugin.json`, `.cursor-plugin/plugin.json`,
   `gemini-extension.json`, `kimi.plugin.json`, the Claude marketplace entry, and each
   skill's `metadata.version`. The Go test enforces this.
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
| Gemini CLI gallery | Needs `gemini-extension.json` at the root of a repository with the `gemini-cli-extension` topic. | Not listed (below) |

### Gemini CLI

The skills live in this repository so that `agent_skills_test.go` checks them in the
same CI run as the CLI they describe. Gemini CLI installs an extension from a repository
root, and its gallery finds extensions by repository topic, so the monorepo is not
installed with `gemini extensions install` and is not listed in the gallery. Gemini CLI
users get the skills through the skills CLI instead, since Gemini reads `.agents/skills`.
`gemini-extension.json` is kept here, so the directory can later be published as an
extension root without changes, for example by a CI job that mirrors it.
