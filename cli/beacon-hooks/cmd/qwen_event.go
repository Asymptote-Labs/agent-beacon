package cmd

import "strings"

// Qwen Code's tool taxonomy.
//
// Qwen sends the Claude Code payload *shape* but not Claude's tool *names*: its built-ins are
// snake_case ids inherited from the Gemini CLI it forked -- `run_shell_command`, `write_file`,
// `edit`, `read_file`, `list_directory`, `glob`, `grep_search`. The generic classifier in
// actionForTool works by substring, and on those names it is wrong in both directions that matter:
//
//   - `write_file`, `edit`, `replace` and `notebook_edit` fall through to `tool.invoked`, because
//     the default isFileEditTool only recognizes Claude's `Write`/`Edit`/`MultiEdit`. A file the
//     agent rewrote would be recorded as an unclassified tool call, which is invisible to every
//     rule and dashboard that keys on `file.modified`.
//   - `list_directory`, `glob` and `grep_search` also fall through, so filesystem reads are not
//     recorded as reads.
//
// Both are misclassification rather than absence, which is the worse failure: the event is there,
// the action is wrong, and nothing looks broken.
//
// The set is closed and matched by equality. A substring rule would be shorter and would also
// classify any MCP tool whose name happens to contain "edit" or "glob" as a Qwen built-in, and MCP
// tools are named by whoever wrote the server.

// qwenReadTools are the built-ins that read from the filesystem without changing it.
//
// `search_file_content` and `read_many_files` are the Gemini CLI spellings of `grep_search` and a
// multi-file read. They are kept because Qwen Code is a fork that renamed some tools and because a
// hook installed against one version can receive payloads from another after an upgrade; an id that
// no longer exists costs one dead map entry, while a missing one costs a misclassified event.
var qwenReadTools = map[string]bool{
	"read_file":           true,
	"read_many_files":     true,
	"list_directory":      true,
	"glob":                true,
	"grep_search":         true,
	"search_file_content": true,
}

// qwenEditTools are the built-ins that create or modify a file.
//
// `replace` is Gemini CLI's id for what Qwen Code documents as `edit`; see qwenReadTools for why
// the older spelling is carried.
var qwenEditTools = map[string]bool{
	"write_file":    true,
	"edit":          true,
	"replace":       true,
	"notebook_edit": true,
}

// qwenCommandTools are the built-ins that execute a shell command.
var qwenCommandTools = map[string]bool{
	"run_shell_command": true,
}

// qwenToolAction maps a Qwen built-in onto an endpoint event action, or returns "" to let the
// generic classifier decide.
//
// Returning "" rather than a default is what keeps MCP tools and Qwen's non-filesystem built-ins
// (`web_fetch`, `web_search`, `todo_write`, `save_memory`, `task`, `skill`) on the shared path,
// where the `mcp` rule and the `tool.invoked` fallback already give the right answer.
func qwenToolAction(toolName string) string {
	switch lower := strings.ToLower(strings.TrimSpace(toolName)); {
	case qwenCommandTools[lower]:
		return "command.executed"
	case qwenReadTools[lower]:
		return "file.read"
	case qwenEditTools[lower]:
		return "file.modified"
	default:
		return ""
	}
}

// isQwenFileEditTool reports whether a Qwen built-in mutates a file.
//
// This gates the diff-capture path as well as the classification, so it is the closed edit set and
// nothing else. A false positive here sends a non-edit tool through diff construction; a false
// negative drops the diff for a real edit.
func isQwenFileEditTool(toolName string) bool {
	return qwenEditTools[strings.ToLower(strings.TrimSpace(toolName))]
}

// qwenFileOperation is the `file.operation` value for a Qwen built-in, or "" to fall through.
//
// Separate from qwenToolAction because the two answer different questions and disagree on one tool:
// the generic fileOperation reads `replace` as neither a read nor a write and leaves the field
// empty, so an edit made through the Gemini-era id would carry a path with no operation.
func qwenFileOperation(toolName string) string {
	lower := strings.ToLower(strings.TrimSpace(toolName))
	switch {
	case qwenReadTools[lower]:
		return "read"
	case lower == "write_file":
		return "create"
	case qwenEditTools[lower]:
		return "modify"
	default:
		return ""
	}
}

// qwenToolFailed reports whether a post-tool payload describes a tool that failed.
//
// Two independent signals, because either can arrive alone. `hook_event_name` is
// "PostToolUseFailure" when Qwen routes the failure to that event, and `error` carries the message;
// a payload delivered through the PostToolUse hook with an error set is still a failure. Reading
// only the event name would miss the second, and reading only `error` would miss an interrupt that
// Qwen reports with `is_interrupt` and an empty message.
func qwenToolFailed(input map[string]interface{}) bool {
	if getFirstStr(input, "hook_event_name") == "PostToolUseFailure" {
		return true
	}
	if getFirstStr(input, "error") != "" {
		return true
	}
	interrupted, _ := input["is_interrupt"].(bool)
	return interrupted
}
