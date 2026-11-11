package asymptoteobserve

import "strings"

// Antigravity CLI's own vocabulary, shared by the live hook adapter and the session-store
// collector so a prompt, a tool call and its path read the same whichever path recorded them.

// AntigravityPrompt returns the user's text from an Antigravity USER_INPUT step: the text inside
// <USER_REQUEST>, without the ADDITIONAL_METADATA and USER_SETTINGS_CHANGE blocks Antigravity
// writes around it. Content with no <USER_REQUEST> is returned trimmed.
func AntigravityPrompt(content string) string {
	if start := strings.Index(content, "<USER_REQUEST>"); start >= 0 {
		content = content[start+len("<USER_REQUEST>"):]
		if end := strings.Index(content, "</USER_REQUEST>"); end >= 0 {
			content = content[:end]
		}
	}
	return strings.TrimSpace(content)
}

// AntigravityToolAction is the endpoint action a completed Antigravity tool call represents, or ""
// for a tool that is none of command, file or MCP activity. The named tools are Antigravity's
// documented ones (https://antigravity.google/docs/hooks/) plus edit_file, write_file and
// apply_patch, which earlier Antigravity releases used. A name it does not know is classified by
// substring: mcp, a command or shell word, read, then an edit, write, create or patch verb.
func AntigravityToolAction(toolName string) string {
	lower := strings.ToLower(strings.TrimSpace(toolName))
	switch lower {
	case "run_command":
		return "command.executed"
	case "view_file", "list_dir", "grep_search", "find_by_name":
		return "file.read"
	case "write_to_file", "replace_file_content", "multi_replace_file_content", "edit_file", "write_file", "apply_patch":
		return "file.modified"
	}
	switch {
	case strings.Contains(lower, "mcp"):
		return "mcp.tool_invoked"
	case strings.Contains(lower, "shell") || strings.Contains(lower, "terminal") || strings.Contains(lower, "command"):
		return "command.executed"
	case strings.Contains(lower, "read"):
		return "file.read"
	case strings.Contains(lower, "edit") || strings.Contains(lower, "write") || strings.Contains(lower, "create") || strings.Contains(lower, "patch"):
		return "file.modified"
	}
	return ""
}

// antigravityPathKeys are the arguments Antigravity's file tools name their target with:
// write_to_file, replace_file_content and multi_replace_file_content (TargetFile), view_file
// (AbsolutePath), list_dir (DirectoryPath), grep_search (SearchPath) and find_by_name
// (SearchDirectory).
var antigravityPathKeys = []string{"TargetFile", "AbsolutePath", "DirectoryPath", "SearchPath", "SearchDirectory"}

// AntigravityToolPath returns the file or directory an Antigravity tool call names, or "".
func AntigravityToolPath(args map[string]interface{}) string {
	for _, key := range antigravityPathKeys {
		if value, ok := args[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
