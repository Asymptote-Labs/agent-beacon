# Autopilot payload fixtures

These are hook inputs **captured from Autopilot 2.0.0** (npm `autopilot-ai@2.0.0`), saved from the
hook's stdin by a command hook registered for every event in `~/.config/autopilot/settings.json`.
The sessions ran in print mode (`autopilot -p ... --dangerously-allow-all`, Code Mode off) against a
local OpenAI-compatible mock model that answered with a fixed sequence of tool calls, so the content
is synthetic but the shape is exactly what Autopilot sends. The only edit is the paths: the
throwaway home directory and project the capture ran in are rewritten to `/home/dev` and
`/home/dev/proj`.

`transcript.jsonl` is the Claude Code-format transcript Autopilot wrote for the same session
(`transcript_path` in every payload, `transcript_format: "claude-jsonl"`), with the same path
rewrite. The tests point `transcript_path` at it, which is how the prompt id reaches each event.

Covered payloads:

- `session_start.json` — `SessionStart`, `source: "startup"`, with the session's model and provider
- `pre_tool_bash.json` — `PreToolUse` for `Bash` (Autopilot's `bash`)
- `post_tool_bash.json` — `PostToolUse` for a `Bash` call that exited 0, with `stdout`/`stderr`/`exit_code`
- `post_tool_bash_failed.json` — `PostToolUse` for a `Bash` call that exited 3: top-level
  `error: "Exit code 3\n..."`, `tool_response.is_error: true`
- `post_tool_failure_bash.json` — the `PostToolUseFailure` Autopilot fires *beside* that
  `PostToolUse` (Beacon's installer does not bind it, see `cmd/autopilot.go`)
- `post_tool_write.json` — `PostToolUse` for `Write` (Autopilot's `write`), creating `hello.py`
- `post_tool_edit.json` — `PostToolUse` for `Edit`, one `old_string`/`new_string` pair
- `post_tool_multiedit.json` — `PostToolUse` for `MultiEdit` (Autopilot's `edit` with `edits[]`)
- `post_tool_read.json` — `PostToolUse` for `Read`
- `post_tool_write_failed.json` — `PostToolUse` for a write that failed (EISDIR). Autopilot keeps
  its lowercase name `write` in `tool_name` for a failed write, so it is never read as a file change
- `permission_request.json` — `PermissionRequest` for `Bash`, `permission_mode: "default"`
- `post_tool_permission_denied.json` — the `PostToolUse` for that call after print mode refused it
- `stop.json` — `Stop` with `last_assistant_message`
- `session_end.json` — `SessionEnd`, `reason: "exit"`

Not covered, because print mode does not fire it: `UserPromptSubmit`. Autopilot fires it only from
its interactive session. `PreCompact` is not covered either: it needs a session long enough to
auto-compact. Replace a fixture with a fresh capture when an Autopilot release changes the shape.
