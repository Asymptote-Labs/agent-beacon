package beaconevent

import (
	"net/url"
	"strings"
)

// Beacon event action vocabulary. These are the canonical "<category>.<verb>" action
// values the converter emits and matches against; they are de-facto schema identifiers,
// so reference these constants rather than repeating the string literals.
const (
	ActionSessionStarted    = "session.started"
	ActionSessionActivity   = "session.activity"
	ActionPromptSubmitted   = "prompt.submitted"
	ActionApprovalRequested = "approval.requested"
	ActionApprovalDenied    = "approval.denied"
	ActionToolInvoked       = "tool.invoked"
	ActionToolFailed        = "tool.failed"
	ActionCommandExecuted   = "command.executed"
	ActionFileRead          = "file.read"
	ActionFileModified      = "file.modified"
	ActionFileCreated       = "file.created"
	ActionMCPToolInvoked    = "mcp.tool_invoked"
)

func HarnessName(attrs map[string]interface{}, hints ...string) string {
	name := FirstString(attrs, "beacon.harness.name", "harness.name", "service.name", "telemetry.sdk.name")
	if explicit := FirstString(attrs, "beacon.harness.name", "harness.name"); explicit != "" {
		return NormalizeHarnessName(explicit)
	}
	candidates := append([]string{name}, hints...)
	for _, candidate := range candidates {
		if normalized := NormalizeHarnessName(candidate); normalized != "" {
			return normalized
		}
	}
	if name != "" {
		return name
	}
	return "otel"
}

// harnessNameRules maps substring aliases to a canonical harness name. Rules are
// evaluated in order and the first whose lower-cased input contains any alias wins, so
// more specific names (e.g. "claude_code", "copilot-chat") must precede the broader
// catch-alls ("claude", "copilot"). This replaces a hand-written switch ladder; the
// classification characterization tests pin the resulting mappings.
var harnessNameRules = []struct {
	aliases []string
	name    string
}{
	{[]string{"cowork", "co-work"}, "claude_cowork"},
	{[]string{"claude_agent_sdk", "claude-agent-sdk", "claude agent sdk"}, "claude_agent_sdk"},
	{[]string{"claude_code", "claude-code", "claude code"}, "claude_code"},
	{[]string{"claude"}, "claude_code"},
	{[]string{"openclaw", "open-claw"}, "openclaw_gateway"},
	{[]string{"antigravity", "anti-gravity"}, "antigravity_cli"},
	{[]string{"codex"}, "codex_cli"},
	{[]string{"gemini"}, "gemini_cli"},
	{[]string{"copilot-chat"}, "vscode_copilot"},
	{[]string{"github-copilot", "copilot_cli", "copilot"}, "copilot_cli"},
}

func NormalizeHarnessName(name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" {
		return ""
	}
	for _, rule := range harnessNameRules {
		for _, alias := range rule.aliases {
			if strings.Contains(lower, alias) {
				return rule.name
			}
		}
	}
	// Unrecognized but non-empty: return the original (untrimmed) name verbatim.
	return name
}

func InferAction(attrs map[string]interface{}, fallback string) string {
	tool := strings.ToLower(FirstString(attrs, toolNameKeys...))
	operation := strings.ToLower(FirstString(attrs, "gen_ai.operation.name"))
	mcpMethod := strings.ToLower(FirstString(attrs, "mcp.method.name"))
	harness := HarnessName(attrs, fallback)
	text := strings.ToLower(strings.Join([]string{
		fallback,
		tool,
		operation,
		mcpMethod,
		FirstString(attrs, "event.name", "codex.op", "rpc.method"),
	}, " "))
	// Structured, heterogeneous matchers run first and short-circuit. Their order is
	// significant relative to the keyword fallback below: e.g. the gemini_cli.file_operation
	// case must precede the "file" keyword rule, which would otherwise claim it.
	switch {
	case harness == "copilot_cli" || harness == "vscode_copilot":
		return CopilotAction(attrs, operation, text)
	case mcpMethod == "tools/call":
		return ActionMCPToolInvoked
	case mcpMethod == "resources/read" && IsFileURI(FirstString(attrs, "mcp.resource.uri")):
		return ActionFileRead
	case operation == "execute_tool":
		return ActionToolInvoked
	case (operation == "chat" || operation == "generate_content" || operation == "text_completion") && HasPromptLikeContent(attrs):
		return ActionPromptSubmitted
	case HasToolCall(attrs):
		return ActionToolInvoked
	case strings.Contains(text, "gemini_cli.user_prompt"):
		return ActionPromptSubmitted
	case strings.Contains(text, "gemini_cli.tool_call"):
		return GeminiToolAction(attrs)
	case strings.Contains(text, "gemini_cli.file_operation"):
		return GeminiFileAction(attrs)
	}

	// Ordered substring fallback over the joined text. First rule with a matching keyword wins.
	for _, rule := range textKeywordActionRules {
		for _, keyword := range rule.keywords {
			if strings.Contains(text, keyword) {
				return rule.action
			}
		}
	}
	return ActionToolInvoked
}

// textKeywordActionRules is the ordered substring-match fallback for InferAction, applied
// after the structured matchers above. Order matters: earlier rules take precedence.
var textKeywordActionRules = []struct {
	keywords []string
	action   string
}{
	{[]string{"approval_mode_switch", "approval_mode_duration", "plan_execution"}, ActionApprovalRequested},
	{[]string{"prompt", "user_input"}, ActionPromptSubmitted},
	{[]string{"mcp"}, ActionMCPToolInvoked},
	{[]string{"command", "shell", "exec"}, ActionCommandExecuted},
	{[]string{"file", "write", "edit"}, ActionFileModified},
	{[]string{"approval"}, ActionApprovalRequested},
}

func HasToolCall(attrs map[string]interface{}) bool {
	if IsMeaningfulValue(attrs["gen_ai.tool.call.id"]) {
		return true
	}
	return IsMeaningfulValue(attrs["gen_ai.tool.call.arguments"])
}

func IsMeaningfulValue(value interface{}) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		trimmed := strings.TrimSpace(typed)
		return trimmed != "" && trimmed != "<nil>" && trimmed != "{}" && trimmed != "[]" && trimmed != "null"
	case map[string]interface{}:
		for _, item := range typed {
			if IsMeaningfulValue(item) {
				return true
			}
		}
		return false
	case []interface{}:
		for _, item := range typed {
			if IsMeaningfulValue(item) {
				return true
			}
		}
		return false
	default:
		return true
	}
}

func IsFileURI(value string) bool {
	return FilePathFromURI(value) != ""
}

func FilePathFromURI(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "file" {
		return ""
	}
	if parsed.Host != "" && parsed.Host != "localhost" {
		return "//" + parsed.Host + parsed.Path
	}
	return parsed.Path
}

func HasPromptLikeContent(attrs map[string]interface{}) bool {
	if FirstTextAttr(attrs, "gen_ai.prompt", "prompt", "user_prompt", "input.prompt", "copilot_chat.user_request") != "" {
		return true
	}
	if v, ok := AnyAttr(attrs, "gen_ai.input.messages"); ok && firstTextFromAny(v) != "" {
		return true
	}
	return len(LegacyMessages(attrs, "gen_ai.prompt.", "user")) > 0
}

func CopilotAction(attrs map[string]interface{}, operation, text string) string {
	if eventName := FirstString(attrs, "event.name", "name"); eventName != "" {
		switch eventName {
		case "copilot_chat.session.start", "copilot_chat.cloud.session.invoke":
			return ActionSessionActivity
		case "copilot_chat.tool.call":
			if strings.EqualFold(FirstString(attrs, "success"), "false") || FirstString(attrs, "error.type") != "" {
				return ActionToolFailed
			}
			return ActionToolInvoked
		case "copilot_chat.edit.feedback", "copilot_chat.edit.hunk.action", "copilot_chat.inline.done":
			return ActionFileModified
		}
	}
	switch {
	case operation == "invoke_agent" && FirstString(attrs, "copilot_chat.user_request") != "":
		return ActionPromptSubmitted
	case operation == "invoke_agent":
		return ActionSessionActivity
	case operation == "execute_hook":
		return ActionApprovalRequested
	case operation == "chat":
		initiator := strings.ToLower(FirstString(attrs, "github.copilot.initiator"))
		turnID := FirstString(attrs, "github.copilot.turn_id")
		if initiator == "agent" || (turnID != "" && turnID != "0") {
			return ActionSessionActivity
		}
		return ActionPromptSubmitted
	case operation == "execute_tool":
		return ActionToolInvoked
	case strings.Contains(text, "permission"):
		return ActionApprovalRequested
	default:
		return ActionToolInvoked
	}
}

func GeminiToolAction(attrs map[string]interface{}) string {
	if FirstString(attrs, "tool_type") == "mcp" || FirstString(attrs, "mcp_server_name") != "" {
		return ActionMCPToolInvoked
	}
	return ActionToolInvoked
}

func GeminiFileAction(attrs map[string]interface{}) string {
	switch strings.ToLower(FirstString(attrs, "operation")) {
	case "read":
		return ActionFileRead
	case "create":
		return ActionFileCreated
	default:
		return ActionFileModified
	}
}

func EventCategory(action, explicit string) string {
	if explicit != "" {
		return explicit
	}
	switch {
	case strings.HasPrefix(action, "prompt."):
		return "prompt"
	case strings.HasPrefix(action, "command."):
		return "command"
	case strings.HasPrefix(action, "file."):
		return "file"
	case strings.HasPrefix(action, "mcp."):
		return "mcp"
	case strings.HasPrefix(action, "approval.") || strings.HasPrefix(action, "policy."):
		return "approval"
	case strings.HasPrefix(action, "session."):
		return "session"
	case strings.HasPrefix(action, "metric."):
		return "metric"
	case strings.HasPrefix(action, "tool."):
		return "tool"
	default:
		return ""
	}
}
