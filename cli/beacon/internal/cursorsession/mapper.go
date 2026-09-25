package cursorsession

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

type MappedEvent struct {
	SourceOrder int
	Event       schema.Event
}

type MapOptions struct {
	MinOrder int
}

func MapTrace(ref TraceRef, records []Record, opts MapOptions) []MappedEvent {
	var out []MappedEvent
	for _, record := range records {
		if record.Order <= opts.MinOrder {
			continue
		}
		ev, ok := mapRecord(ref, record)
		if !ok {
			continue
		}
		key := fmt.Sprintf("%s:%s:%d:%s:%s", ref.Kind, ref.ID, record.Order, record.NativeID, record.Type)
		ev.Event.ID = cursorEventID(key)
		out = append(out, MappedEvent{SourceOrder: record.Order, Event: ev})
		// The link shares its prompt's record, so a sync that resumes from that record's order
		// never splits the pair.
		if link, ok := handoffLink(ref, record); ok {
			link.Event.ID = cursorEventID(key + ":handoff")
			out = append(out, MappedEvent{SourceOrder: record.Order, Event: link})
		}
	}
	return out
}

// handoffLink is the session.handoff event for a prompt that carries the handoff marker.
func handoffLink(ref TraceRef, record Record) (schema.Event, bool) {
	if record.Type != "user_message" {
		return schema.Event{}, false
	}
	info, ok := asymptoteobserve.ParseHandoffMarker(record.Content)
	if !ok {
		return schema.Event{}, false
	}
	link := baseEvent(ref, record, "session.handoff", "session", schema.SeverityInfo, schema.FidelityObserved, "Session continued from a "+info.SourceHarness+" session")
	link.Handoff = &info
	return link, true
}

func mapRecord(ref TraceRef, record Record) (schema.Event, bool) {
	ev := baseEvent(ref, record, "tool.completed", "tool", schema.SeverityInfo, schema.FidelityObserved, "Cursor session event")
	switch record.Type {
	case "user_message":
		if strings.TrimSpace(record.Content) == "" {
			return schema.Event{}, false
		}
		ev.Event.Action = "prompt.submitted"
		ev.Event.Category = "prompt"
		ev.Message = "Cursor prompt submitted"
		ev.Prompt = &schema.PromptInfo{Text: record.Content}
		ev.Content = asymptoteobserve.RetainedContent(record.Content, asymptoteobserve.DefaultStringLimit)
		ev.GenAI = withGenAI(ev.GenAI, func(genAI *schema.GenAIInfo) {
			genAI.Input = &schema.GenAIInputInfo{Messages: asymptoteobserve.TextInputMessages(record.Content)}
		})
	case "agent_text":
		if strings.TrimSpace(record.Content) == "" {
			return schema.Event{}, false
		}
		ev.Event.Action = "agent.message"
		ev.Event.Category = "session"
		ev.Message = "Cursor agent message"
		ev.Content = asymptoteobserve.RetainedContent(record.Content, asymptoteobserve.DefaultRawStringLimit)
		ev.GenAI = withGenAI(ev.GenAI, func(genAI *schema.GenAIInfo) {
			genAI.Output = &schema.GenAIOutputInfo{Messages: asymptoteobserve.TextOutputMessages(record.Content)}
		})
	case "agent_thinking":
		if strings.TrimSpace(record.Content) == "" {
			return schema.Event{}, false
		}
		ev.Event.Action = "agent.reasoning"
		ev.Event.Category = "session"
		ev.Message = "Cursor agent reasoning"
		ev.Content = asymptoteobserve.RetainedContent(record.Content, asymptoteobserve.DefaultStringLimit)
		ev.GenAI = withGenAI(ev.GenAI, func(genAI *schema.GenAIInfo) {
			genAI.Output = &schema.GenAIOutputInfo{Messages: []interface{}{
				map[string]interface{}{
					"role": "assistant",
					"parts": []interface{}{
						map[string]interface{}{"type": "reasoning", "content": record.Content},
					},
				},
			}}
		})
	case "tool_call":
		ev.Event.Action = "tool.invoked"
		ev.Event.Category = "tool"
		ev.Message = "Cursor tool invoked"
		applyTool(&ev, record)
	case "tool_result":
		action, category, message := classifyTool(record)
		ev.Event.Action = action
		ev.Event.Category = category
		ev.Message = message
		applyTool(&ev, record)
		if record.Status == "error" {
			ev.Error = &schema.ErrorInfo{Type: "tool_execution_failed"}
		}
		if action == "command.executed" {
			applyCommand(&ev, record)
		}
		if strings.HasPrefix(action, "file.") {
			applyFile(&ev, record)
		}
		if action == "mcp.tool_invoked" {
			ev.MCP = &schema.MCPInfo{Tool: record.ToolName}
		}
	case "error":
		ev.Event.Action = "tool.failed"
		ev.Event.Category = "tool"
		ev.Severity = schema.SeverityHigh
		ev.Message = firstNonEmpty(record.Content, "Cursor session error")
		ev.Error = &schema.ErrorInfo{Type: "cursor_session_error"}
	case "system_event":
		ev.Event.Action = "session.compacting"
		ev.Event.Category = "session"
		ev.Message = "Cursor compacted session history"
	default:
		return schema.Event{}, false
	}
	raw := map[string]interface{}{
		"source":      ref.Kind,
		"source_path": ref.SourcePath,
		"native_id":   record.NativeID,
		"order":       record.Order,
	}
	if record.Subtype != "" {
		raw["subtype"] = record.Subtype
	}
	if len(record.Data) > 0 {
		raw["data"] = record.Data
	}
	ev.Raw = map[string]interface{}{"cursor": raw}
	return ev, true
}

func baseEvent(ref TraceRef, record Record, action, category string, severity schema.Severity, fidelity, message string) schema.Event {
	ev := schema.NewEvent(schema.NewEventOptions{
		Action:   action,
		Category: category,
		Severity: severity,
		Fidelity: fidelity,
		Message:  message,
		Origin:   schema.OriginLocal,
		Harness: schema.HarnessInfo{
			Name:             Harness,
			CollectionMethod: schema.CollectionMethodPoll,
		},
	})
	if record.TimestampMS > 0 {
		ev.Timestamp = schema.FormatTimestamp(time.UnixMilli(record.TimestampMS))
	} else if ref.UpdatedAtUnixMS > 0 {
		ev.Timestamp = schema.FormatTimestamp(time.UnixMilli(ref.UpdatedAtUnixMS))
	}
	ev.Session = &schema.SessionInfo{ID: ref.ID, WorkingDirectory: ref.Workspace}
	if record.Model != "" {
		ev.Model = asymptoteobserve.NormalizeModelName(record.Model)
	}
	if record.ContextUsedTokens > 0 || record.ContextLimitTokens > 0 {
		ev.GenAI = withGenAI(ev.GenAI, func(genAI *schema.GenAIInfo) {
			context := &schema.GenAIContextInfo{}
			if record.ContextUsedTokens > 0 {
				context.UsedTokens = &record.ContextUsedTokens
			}
			if record.ContextLimitTokens > 0 {
				context.LimitTokens = &record.ContextLimitTokens
			}
			genAI.Context = context
		})
	}
	return ev
}

func applyTool(ev *schema.Event, record Record) {
	ev.Tool = &schema.ToolInfo{Name: record.ToolName}
	if path := pathFromArgs(record.Args); path != "" {
		ev.Tool.Path = path
	}
	if command := commandFromArgs(record.Args); command != "" {
		ev.Tool.Command = command
	}
	ev.GenAI = withGenAI(ev.GenAI, func(genAI *schema.GenAIInfo) {
		genAI.Operation = &schema.GenAIOperationInfo{Name: "execute_tool"}
		genAI.Tool = &schema.GenAIToolInfo{
			Name: record.ToolName,
			Call: &schema.GenAIToolCallInfo{
				ID:        record.CallID,
				Arguments: record.Args,
			},
		}
		if record.Type == "tool_result" && strings.TrimSpace(record.Output) != "" {
			genAI.Tool.Call.Result = record.Output
		}
	})
}

func classifyTool(record Record) (action, category, message string) {
	name := strings.ToLower(record.ToolName)
	switch {
	case strings.HasPrefix(name, "mcp_") || strings.Contains(name, "__"):
		return "mcp.tool_invoked", "mcp", "Cursor MCP tool invoked"
	case commandFromArgs(record.Args) != "" || strings.Contains(name, "terminal") || strings.Contains(name, "command") || strings.Contains(name, "bash") || strings.Contains(name, "shell"):
		return "command.executed", "command", "Cursor command executed"
	case pathFromArgs(record.Args) != "":
		if strings.Contains(name, "read") || strings.Contains(name, "grep") || strings.Contains(name, "search") || strings.Contains(name, "list") {
			return "file.read", "file", "Cursor file read"
		}
		if strings.Contains(name, "write") || strings.Contains(name, "create") {
			return "file.created", "file", "Cursor file created"
		}
		if strings.Contains(name, "edit") || strings.Contains(name, "delete") || strings.Contains(name, "apply") {
			return "file.modified", "file", "Cursor file modified"
		}
	case record.Status == "error":
		return "tool.failed", "tool", "Cursor tool failed"
	}
	return "tool.completed", "tool", "Cursor tool completed"
}

func applyCommand(ev *schema.Event, record Record) {
	info := &schema.CommandInfo{Command: commandFromArgs(record.Args)}
	if strings.TrimSpace(record.Output) != "" {
		info.Output = record.Output
		ev.Content = asymptoteobserve.RetainedContent(record.Output, asymptoteobserve.DefaultStringLimit)
	}
	if record.DurationMS > 0 {
		info.DurationMS = record.DurationMS
	}
	ev.Command = info
}

func applyFile(ev *schema.Event, record Record) {
	path := pathFromArgs(record.Args)
	info := &schema.FileInfo{Path: path, Operation: fileOperation(record)}
	if path != "" {
		info.Language = strings.TrimPrefix(filepath.Ext(path), ".")
	}
	if diff := diffFromArgs(record.Args); diff != "" {
		info.Diff = diff
		info.DiffBytes = len(diff)
		info.DiffHash = sha256Hex(diff)
		ev.Content = asymptoteobserve.RetainedContent(diff, asymptoteobserve.DefaultStringLimit)
	}
	ev.File = info
}

func fileOperation(record Record) string {
	name := strings.ToLower(record.ToolName)
	switch {
	case strings.Contains(name, "read") || strings.Contains(name, "grep") || strings.Contains(name, "search") || strings.Contains(name, "list"):
		return "read"
	case strings.Contains(name, "write") || strings.Contains(name, "create"):
		return "create"
	default:
		return "modify"
	}
}

func pathFromArgs(args map[string]interface{}) string {
	for _, key := range []string{"path", "file_path", "filePath", "targetFile", "target_file", "target_path", "relativeWorkspacePath"} {
		if value, ok := args[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func commandFromArgs(args map[string]interface{}) string {
	for _, key := range []string{"command", "cmd", "commandLine"} {
		if value, ok := args[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func diffFromArgs(args map[string]interface{}) string {
	oldText, oldOK := args["old_string"].(string)
	newText, newOK := args["new_string"].(string)
	if !oldOK || !newOK {
		return ""
	}
	path := pathFromArgs(args)
	var b strings.Builder
	if path != "" {
		b.WriteString("--- a/")
		b.WriteString(path)
		b.WriteString("\n+++ b/")
		b.WriteString(path)
		b.WriteString("\n")
	}
	b.WriteString("-")
	b.WriteString(strings.ReplaceAll(oldText, "\n", "\n-"))
	b.WriteString("\n+")
	b.WriteString(strings.ReplaceAll(newText, "\n", "\n+"))
	b.WriteString("\n")
	return b.String()
}

func withGenAI(genAI *schema.GenAIInfo, edit func(*schema.GenAIInfo)) *schema.GenAIInfo {
	if genAI == nil {
		genAI = &schema.GenAIInfo{}
	}
	edit(genAI)
	return genAI
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

var cursorIDNamespace = [16]byte{
	0x50, 0x73, 0x15, 0x64, 0xc6, 0x90, 0x44, 0x0f,
	0x91, 0x4a, 0x42, 0x31, 0x77, 0xe1, 0x82, 0x9c,
}

func cursorEventID(value string) string {
	digest := sha1.New()
	digest.Write(cursorIDNamespace[:])
	io.WriteString(digest, value)
	var id [16]byte
	copy(id[:], digest.Sum(nil))
	id[6] = (id[6] & 0x0f) | 0x50
	id[8] = (id[8] & 0x3f) | 0x80
	out := make([]byte, 32)
	hex.Encode(out, id[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", out[0:8], out[8:12], out[12:16], out[16:20], out[20:32])
}

func rawJSON(v interface{}) string {
	data, _ := json.Marshal(v)
	return string(data)
}
