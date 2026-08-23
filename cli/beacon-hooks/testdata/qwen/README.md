# Qwen Code payload fixtures

These payloads are **constructed from Qwen Code's published hooks documentation, not captured from
a running Qwen Code install**. The documentation specifies the fields precisely -- the common
envelope (`session_id`, `transcript_path`, `cwd`, `hook_event_name`, `timestamp`) and the
event-specific fields for each hook -- so the shapes here are the documented contract rather than a
guess. What they are not is evidence that a shipped build sends exactly this; replace a fixture with
a captured payload whenever one becomes available.

Qwen's own documentation calls the hook input "a forward-extensible JSON contract: new optional
fields can be added to existing events", so the mapper reads the fields it needs and ignores the
rest. These fixtures carry the optional fields as well as the required ones (`permission_mode`,
`tool_use_id`, `tool_call_id`, `submitted_prompt`, the `Stop` context trio) because those are the
part that reaches `raw.qwen` and is easy to drop by accident.

Covered payloads:

- `session_start.json` — `SessionStart` with `source` and the session's model
- `user_prompt_submit.json` — `UserPromptSubmit` with both `prompt` and `submitted_prompt`
- `pre_tool_shell.json` — `PreToolUse` for `run_shell_command`
- `post_tool_shell.json` — `PostToolUse` for `run_shell_command`, with its response
- `post_tool_write_file.json` — `PostToolUse` for `write_file`, a whole-file write
- `post_tool_edit.json` — `PostToolUse` for `edit`, an `old_string`/`new_string` replacement
- `post_tool_failure.json` — `PostToolUseFailure` for `write_file`, carrying `error`
- `stop.json` — `Stop` with `context_usage`, `context_limit` and `input_tokens`

Keep secrets and real prompt content out of fixtures. Use sanitized payloads that preserve the field
shape `beacon-hooks --platform qwen` reads.
