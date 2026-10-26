package harness2session

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// mapCodexExec maps Codex's `exec --json` stream (codex-rs/exec/src/exec_events.rs): thread.started
// names the thread, turn.completed carries usage, and each thread item is reported as it starts,
// updates and completes. Only item.completed is mapped, since that is the item's final state; an
// item still in progress when the stream ended -- a command the timeout killed -- has no outcome to
// record. Harness² gives the prompt as an argument, so it is not in the stream.
func (m *mapper) mapCodexExec(lines []Line) {
	// turn.completed reports the thread's running total (usage_from_last_total in
	// event_processor_with_jsonl_output.rs), so each turn's own usage is the difference from the
	// previous turn's. The previous total is tracked over every line, written or not, so a sweep
	// that resumes mid-stream differences against the right base.
	var previous codexUsage
	for _, line := range lines {
		switch str(line.Object, "type") {
		case "thread.started":
			m.emitStarted(line, 0, schema.FidelityObserved)
		case "turn.completed":
			total := codexUsageOf(object(line.Object, "usage"))
			if m.emits(line) {
				m.emitUsage(line, 0, total.minus(previous).genAI())
			}
			previous = total
		case "turn.failed":
			if m.emits(line) {
				m.emitError(line, 0, schema.SeverityHigh, "turn_failed", str(object(line.Object, "error"), "message"))
			}
		case "error":
			if m.emits(line) {
				m.emitError(line, 0, schema.SeverityHigh, "stream_error", str(line.Object, "message"))
			}
		case "item.completed":
			if m.emits(line) {
				m.emitCodexItem(line, object(line.Object, "item"))
			}
		}
	}
}

func (m *mapper) emitCodexItem(line Line, item map[string]interface{}) {
	id := str(item, "id")
	switch str(item, "type") {
	case "agent_message":
		m.emitText(line, 0, "agent.message", "assistant", str(item, "text"))
	case "reasoning":
		m.emitText(line, 0, "agent.reasoning", "reasoning", str(item, "text"))
	case "command_execution":
		m.emitCodexCommand(line, id, item)
	case "file_change":
		m.emitCodexFileChange(line, id, item)
	case "mcp_tool_call":
		m.emitCodexMCP(line, id, item)
	case "web_search":
		ev := m.base(line, 0, "tool.completed", "tool", schema.SeverityInfo, schema.FidelityObserved, "Harness² executor web search")
		ev.Tool = &schema.ToolInfo{Name: "web_search"}
		toolCall(&ev, "web_search", id, map[string]interface{}{"query": str(item, "query")}, nil)
		m.append(line, "tool."+id, ev)
	case "collab_tool_call":
		m.emitCodexCollab(line, id, item)
	case "error":
		// A thread item error is a warning Codex surfaced and carried on past; turn.failed and the
		// top-level error event are the ones that end a turn.
		m.emitError(line, 0, schema.SeverityMedium, "item_error", str(item, "message"))
	}
}

func (m *mapper) emitCodexCommand(line Line, id string, item map[string]interface{}) {
	status := str(item, "status")
	command := str(item, "command")
	action, severity, message := "command.executed", schema.SeverityInfo, "Harness² executor command executed"
	if status == "declined" {
		action, message = "tool.failed", "Harness² executor command declined"
	}
	ev := m.base(line, 0, action, "command", severity, schema.FidelityObserved, message)
	ev.Tool = &schema.ToolInfo{Name: "shell", Command: command}
	output := str(item, "aggregated_output")
	info := &schema.CommandInfo{Command: command, Output: output}
	if code, ok := num(item, "exit_code"); ok {
		exit := int(code)
		info.ExitCode = &exit
	}
	ev.Command = info
	var result interface{}
	if output != "" {
		result = output
		ev.Content = asymptoteobserve.RetainedContent(output, asymptoteobserve.DefaultStringLimit)
	}
	toolCall(&ev, "shell", id, map[string]interface{}{"command": command}, result)
	if status == "failed" || status == "declined" {
		ev.Error = &schema.ErrorInfo{Type: "command_" + status}
	}
	m.append(line, "tool."+id, ev)
}

// emitCodexFileChange writes one file event per path an apply_patch touched. The stream reports the
// paths and what happened to each, not the patch itself, so these events carry no diff.
func (m *mapper) emitCodexFileChange(line Line, id string, item map[string]interface{}) {
	changes, _ := item["changes"].([]interface{})
	failed := str(item, "status") == "failed"
	for i, raw := range changes {
		change, _ := raw.(map[string]interface{})
		path := str(change, "path")
		if path == "" {
			continue
		}
		action, operation := "file.modified", "modify"
		switch str(change, "kind") {
		case "add":
			action, operation = "file.created", "create"
		case "delete":
			// A deletion stays in the file family, with its own operation word, as the Kiro and fx
			// mappers record one: it is not a modification.
			operation = "delete"
		}
		ev := m.base(line, 0, action, "file", schema.SeverityInfo, schema.FidelityObserved, "Harness² executor file "+operation)
		ev.Tool = &schema.ToolInfo{Name: "apply_patch", Path: path}
		ev.File = &schema.FileInfo{Path: path, Operation: operation, Language: strings.TrimPrefix(filepath.Ext(path), ".")}
		toolCall(&ev, "apply_patch", id, map[string]interface{}{"path": path, "kind": str(change, "kind")}, nil)
		if failed {
			ev.Error = &schema.ErrorInfo{Type: "patch_failed"}
		}
		m.append(line, "file."+id+"."+strconv.Itoa(i), ev)
	}
}

func (m *mapper) emitCodexMCP(line Line, id string, item map[string]interface{}) {
	server, tool := str(item, "server"), str(item, "tool")
	ev := m.base(line, 0, "mcp.tool_invoked", "mcp", schema.SeverityInfo, schema.FidelityObserved, "Harness² executor MCP tool invoked")
	ev.Tool = &schema.ToolInfo{Name: firstNonEmpty(tool, "unknown")}
	ev.MCP = &schema.MCPInfo{Server: server, Tool: tool}
	var result interface{}
	if value, ok := item["result"]; ok && value != nil {
		result = value
	}
	toolCall(&ev, ev.Tool.Name, id, item["arguments"], result)
	if errObj := object(item, "error"); errObj != nil || str(item, "status") == "failed" {
		ev.Error = &schema.ErrorInfo{Type: "tool_error"}
		if text := str(errObj, "message"); text != "" {
			ev.Raw["error_message"] = truncate(text, 2000)
		}
	}
	m.append(line, "tool."+id, ev)
}

// emitCodexCollab records a call to one of Codex's agent-collaboration tools (spawn_agent,
// send_input, wait, close_agent). The agents it reaches run as their own threads, which this stream
// does not contain, so the call is recorded as a tool call with the thread ids it names.
func (m *mapper) emitCodexCollab(line Line, id string, item map[string]interface{}) {
	tool := firstNonEmpty(str(item, "tool"), "collab")
	action, severity := "tool.completed", schema.SeverityInfo
	if str(item, "status") == "failed" {
		action, severity = "tool.failed", schema.SeverityHigh
	}
	ev := m.base(line, 0, action, "tool", severity, schema.FidelityObserved, "Harness² executor agent collaboration")
	ev.Tool = &schema.ToolInfo{Name: tool}
	args := map[string]interface{}{}
	for _, key := range []string{"sender_thread_id", "receiver_thread_ids", "prompt"} {
		if value, ok := item[key]; ok && value != nil {
			args[key] = value
		}
	}
	toolCall(&ev, tool, id, args, item["agents_states"])
	m.append(line, "tool."+id, ev)
}

type codexUsage struct {
	input, cached, output, reasoning int64
}

func codexUsageOf(m map[string]interface{}) codexUsage {
	return codexUsage{
		input:     positive(m, "input_tokens"),
		cached:    positive(m, "cached_input_tokens"),
		output:    positive(m, "output_tokens"),
		reasoning: positive(m, "reasoning_output_tokens"),
	}
}

// minus is the growth from previous. A total smaller than the one before it is not a running total
// any more -- a resumed thread restarting its count -- so it is taken whole.
func (u codexUsage) minus(previous codexUsage) codexUsage {
	if u.input < previous.input || u.cached < previous.cached || u.output < previous.output || u.reasoning < previous.reasoning {
		return u
	}
	return codexUsage{
		input:     u.input - previous.input,
		cached:    u.cached - previous.cached,
		output:    u.output - previous.output,
		reasoning: u.reasoning - previous.reasoning,
	}
}

// genAI converts Codex's counts the way the Codex session collector does (codexsession
// usageFromCodex): Codex's input includes its cached input, which gen_ai.usage counts separately,
// and its output already includes reasoning, which gen_ai.usage counts inside output too.
func (u codexUsage) genAI() *schema.GenAIUsageInfo {
	if u == (codexUsage{}) {
		return nil
	}
	uncached := u.input - u.cached
	if uncached < 0 {
		uncached = 0
	}
	usage := &schema.GenAIUsageInfo{InputTokens: ptr(uncached), OutputTokens: ptr(u.output)}
	if u.cached > 0 {
		usage.CacheRead = &schema.GenAIUsageCacheReadInfo{InputTokens: ptr(u.cached)}
	}
	if u.reasoning > 0 {
		usage.Reasoning = &schema.GenAIUsageReasoningInfo{OutputTokens: ptr(u.reasoning)}
	}
	return usage
}
