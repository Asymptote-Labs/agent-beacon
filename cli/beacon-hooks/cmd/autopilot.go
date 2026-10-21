package cmd

// Autopilot payload handling.
//
// Autopilot (npm `autopilot-ai`) is a terminal coding agent whose hook runner emits Claude Code's
// payload shape on purpose: every event carries `hook_event_name`, `session_id`, `cwd` and
// `transcript_path`, and every tool event carries `tool_name`, `tool_input`, `tool_response` and
// `tool_use_id` under Claude Code's names, with Autopilot's own tools renamed to Claude Code's
// (`bash` -> `Bash`, `edit` -> `Edit` or `MultiEdit`, `write` -> `Write`, `read` -> `Read`, ...) and
// their arguments rewritten to Claude Code's spellings (`path` -> an absolute `file_path`). A
// captured session looks like:
//
//	{"hook_event_name": "PostToolUse", "session_id": "...", "cwd": "...",
//	 "transcript_path": ".../sessions/<id>.jsonl", "transcript_format": "claude-jsonl",
//	 "tool_name": "Bash", "tool_input": {"command": "echo hi"}, "tool_use_id": "call_0",
//	 "tool_response": {"stdout": "hi\n", "stderr": "", "exit_code": 0, "interrupted": false, ...},
//	 "tool": "bash", "args": {"command": "echo hi"}, "autopilot_tool_name": "bash"}
//
// Autopilot's own names stay beside the Claude Code ones (`tool`, `args`, `result`), and are not
// read here: the Claude Code fields are the documented interface for tools like this one, and
// reading both would give one call two descriptions that could disagree.
//
// So Autopilot rides the Claude Code path rather than a reader of its own, and this file exists to
// say where it does and where it deliberately does not. It gets its own --platform value rather
// than reusing `claude` because events must not be filed under claude_code: they are a different
// runtime's sessions, with its own session ids, its own settings file and its own install, and a
// query grouping by harness.name must be able to tell them apart.
//
// Where Autopilot is treated exactly as Claude Code:
//
//   - Pre-tool observes and never synthesizes an approval, and answers with an empty object.
//     Autopilot has a real PermissionRequest event, which Beacon also subscribes to, and an "allow"
//     from a PreToolUse hook would be read as a decision.
//
//   - The policy seam's deny is Claude Code's per-phase object. Autopilot reads
//     `hookSpecificOutput.permissionDecision` on PreToolUse and `hookSpecificOutput.decision.behavior`
//     on PermissionRequest, the same two shapes claudePolicyDenyResponse writes.
//
//   - A Bash call that exited non-zero is recorded as command.executed with its exit code.
//     Autopilot reports the failure the way Claude Code does, as a top-level `error` that begins
//     `Exit code N`, followed by the command's output.
//
//   - Stop's `last_assistant_message` is the turn's final reply.
//
//   - The prompt id comes from the transcript. Autopilot writes a Claude Code-format JSONL
//     transcript and stamps `promptId` on each prompt entry and on every tool result after it.
//
// Where it is not:
//
//   - Failure is reported on PostToolUse itself, not only on PostToolUseFailure. Autopilot fires
//     PostToolUse for every call, successful or not, and adds PostToolUseFailure beside it for a
//     failure, where Claude Code fires one or the other. The installer therefore binds only
//     PostToolUse to post-tool on this runtime; binding both would record every failure twice.
//
//   - A failed write or edit keeps Autopilot's lowercase name (`write`, `edit`) in `tool_name`, so
//     isFileEditTool does not send it down the diff path and it is recorded as tool.failed rather
//     than as a file.modified for a change that never landed. Nothing here has to do that: the
//     runtime refuses to call a failed write `Write` precisely so a Claude-shaped reader gets it
//     right.
//
//   - CLAUDE_ENV_FILE is Claude Code's alone and is not written for Autopilot (see
//     exportClaudeSessionEnv), and neither is the Cursor payload sniffing in
//     applyPayloadPlatformOverride, which exists for Cursor's use of the `claude` flag.
const autopilotPlatform = "autopilot"

// sendsClaudeCodePayloads reports whether a platform's hook payloads are Claude Code's own shape,
// field for field, so the readers written against Claude Code's documented payloads apply as they
// are.
//
// A predicate rather than a second `|| platformFlag == autopilotPlatform` at each call site, so the
// readers that depend on the claim are the ones that name it, and a reader that should not follow
// along (CLAUDE_ENV_FILE, the Cursor sniffing in applyPayloadPlatformOverride) is visibly not using it.
func sendsClaudeCodePayloads(platform string) bool {
	return platform == "claude" || platform == autopilotPlatform
}
