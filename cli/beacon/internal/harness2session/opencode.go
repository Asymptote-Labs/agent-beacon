package harness2session

import (
	"path/filepath"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// mapOpenCode maps OpenCode's `run --format json` stream. Each line is
//
//	{"type": <event>, "timestamp": <ms>, "sessionID": ..., "part": {...}}
//
// where the part is the message part OpenCode stores, in the shape it stores it
// (packages/opencode/src/cli/cmd/run.ts emits it as is). Only finished parts are emitted: a text or
// reasoning part once it has an end time, a tool part once it completed or failed, and a step's
// usage when the step finishes. The prompt is not in the stream -- it is an argument to
// `opencode run` -- so a rollout here has no prompt.submitted.
func (m *mapper) mapOpenCode(lines []Line) {
	for i, line := range lines {
		ts, _ := num(line.Object, "timestamp")
		if i == 0 {
			m.emitStarted(line, ts, schema.FidelityInferred)
		}
		if !m.emits(line) {
			continue
		}
		part := object(line.Object, "part")
		switch str(line.Object, "type") {
		case "text":
			m.emitText(line, ts, "agent.message", "assistant", str(part, "text"))
		case "reasoning":
			m.emitText(line, ts, "agent.reasoning", "reasoning", str(part, "text"))
		case "tool_use":
			m.emitOpenCodeTool(line, ts, part)
		case "step_finish":
			m.emitUsage(line, ts, openCodeUsage(part))
		case "error":
			errObj := object(line.Object, "error")
			m.emitError(line, ts, schema.SeverityHigh, str(errObj, "name"), firstNonEmpty(str(object(errObj, "data"), "message"), str(errObj, "message")))
		}
	}
}

func (m *mapper) emitOpenCodeTool(line Line, ts int64, part map[string]interface{}) {
	state := object(part, "state")
	name := firstNonEmpty(str(part, "tool"), "unknown")
	status := str(state, "status")
	if end := positive(object(state, "time"), "end"); end > 0 {
		ts = end
	}
	args := object(state, "input")
	action, category, severity, message := classifyOpenCodeTool(name, status)
	ev := m.base(line, ts, action, category, severity, schema.FidelityObserved, message)
	ev.Tool = &schema.ToolInfo{Name: name}
	if path := argString(args, "filePath", "file_path", "path"); path != "" {
		ev.Tool.Path = path
	}
	if command := argString(args, "command", "cmd"); command != "" {
		ev.Tool.Command = command
	}
	var result interface{}
	if output, ok := state["output"]; ok && output != nil {
		result = output
	}
	var callArgs interface{}
	if len(args) > 0 {
		callArgs = args
	}
	toolCall(&ev, name, firstNonEmpty(str(part, "callID"), str(part, "id")), callArgs, result)
	output := outputText(result)
	switch category {
	case "command":
		info := &schema.CommandInfo{Command: ev.Tool.Command, Output: output}
		if code, ok := num(object(state, "metadata"), "exit"); ok {
			exit := int(code)
			info.ExitCode = &exit
		}
		if output != "" {
			ev.Content = asymptoteobserve.RetainedContent(output, asymptoteobserve.DefaultStringLimit)
		}
		ev.Command = info
	case "file":
		ev.File = openCodeFile(action, ev.Tool.Path, args, object(state, "metadata"))
		if ev.File.Diff != "" {
			ev.Content = asymptoteobserve.RetainedContent(ev.File.Diff, asymptoteobserve.DefaultStringLimit)
		}
	case "mcp":
		ev.MCP = &schema.MCPInfo{Tool: name}
	}
	if status == "error" {
		ev.Error = &schema.ErrorInfo{Type: "tool_error"}
		if text := str(state, "error"); text != "" {
			ev.Raw["error_message"] = truncate(text, 2000)
		}
	}
	m.append(line, "tool."+firstNonEmpty(str(part, "callID"), str(part, "id")), ev)
}

// classifyOpenCodeTool sorts OpenCode's tool names into Beacon's event families the way the OpenCode
// session collector does (opencodesession.classifyTool), so a tool call reads the same whichever
// path recorded it.
func classifyOpenCodeTool(name, status string) (action, category string, severity schema.Severity, message string) {
	lower := strings.ToLower(strings.TrimSpace(name))
	switch {
	case nameHasToken(lower, "mcp"):
		return "mcp.tool_invoked", "mcp", schema.SeverityInfo, "Harness² executor MCP tool invoked"
	case nameHasToken(lower, "bash") || nameHasToken(lower, "shell") || nameHasToken(lower, "terminal") || lower == "powershell":
		return "command.executed", "command", schema.SeverityInfo, "Harness² executor command executed"
	case nameHasToken(lower, "read") || nameHasToken(lower, "view") || nameHasToken(lower, "list") || nameHasToken(lower, "grep") || nameHasToken(lower, "glob"):
		return "file.read", "file", schema.SeverityInfo, "Harness² executor file read"
	case nameHasToken(lower, "write") || nameHasToken(lower, "create"):
		return "file.created", "file", schema.SeverityInfo, "Harness² executor file created"
	case nameHasToken(lower, "edit") || nameHasToken(lower, "patch") || lower == "applypatch" || lower == "multiedit":
		return "file.modified", "file", schema.SeverityInfo, "Harness² executor file modified"
	case status == "error":
		return "tool.failed", "tool", schema.SeverityHigh, "Harness² executor tool failed"
	default:
		return "tool.completed", "tool", schema.SeverityInfo, "Harness² executor tool completed"
	}
}

func openCodeFile(action, path string, args, metadata map[string]interface{}) *schema.FileInfo {
	operation := "modify"
	switch action {
	case "file.read":
		operation = "read"
	case "file.created":
		operation = "create"
	}
	file := &schema.FileInfo{Path: path, Operation: operation}
	if path != "" {
		file.Language = strings.TrimPrefix(filepath.Ext(path), ".")
	}
	if operation != "read" {
		diff := firstNonEmpty(str(metadata, "diff"), argString(args, "diff", "patch", "patchText"), argString(args, "content", "newString", "new_string"))
		if diff != "" {
			file.Diff = diff
			file.DiffBytes = len(diff)
			file.DiffHash = sha256Hex(diff)
		}
	}
	return file
}

// openCodeUsage reads a finished step's tokens and cost. OpenCode's step usage has the shape it
// stores on messages, so output follows the rule the OpenCode session collector documents on
// opencodesession.TokenUsage.outputTokens: since v1.3.16 OpenCode stores output without reasoning,
// and gen_ai.usage counts reasoning inside output, so it is added back when the provider total says
// output left it out.
func openCodeUsage(part map[string]interface{}) *schema.GenAIUsageInfo {
	tokens := object(part, "tokens")
	cache := object(tokens, "cache")
	input, output, reasoning := positive(tokens, "input"), positive(tokens, "output"), positive(tokens, "reasoning")
	cacheRead, cacheWrite, total := positive(cache, "read"), positive(cache, "write"), positive(tokens, "total")
	if reasoning > 0 && (reasoning > output || (total > 0 && total >= input+cacheRead+cacheWrite+output+reasoning)) {
		output += reasoning
	}
	usage := &schema.GenAIUsageInfo{}
	if input > 0 {
		usage.InputTokens = ptr(input)
	}
	if output > 0 {
		usage.OutputTokens = ptr(output)
	}
	if reasoning > 0 {
		usage.Reasoning = &schema.GenAIUsageReasoningInfo{OutputTokens: ptr(reasoning)}
	}
	if cacheRead > 0 {
		usage.CacheRead = &schema.GenAIUsageCacheReadInfo{InputTokens: ptr(cacheRead)}
	}
	if cacheWrite > 0 {
		usage.CacheCreation = &schema.GenAIUsageCacheCreationInfo{InputTokens: ptr(cacheWrite)}
	}
	if cost, ok := part["cost"].(float64); ok && cost > 0 {
		usage.CostUSD = &cost
	}
	return usage
}

func argString(args map[string]interface{}, keys ...string) string {
	return str(args, keys...)
}

func nameHasToken(name, token string) bool {
	for _, part := range strings.FieldsFunc(name, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	}) {
		if part == token {
			return true
		}
	}
	return false
}
