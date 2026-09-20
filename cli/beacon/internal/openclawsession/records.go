package openclawsession

import (
	"encoding/json"
	"fmt"
	"strings"
)

func recordsFromEntries(entries []RawEntry, fallbackTimestamp int64) []Record {
	var records []Record
	order := 1
	for i, entry := range entries {
		kind := strings.ToLower(strings.TrimSpace(entry.Type))
		timestamp := parseTimestamp(entry.Timestamp, fallbackTimestamp)
		nativeID := firstNonEmpty(entry.ID, fmt.Sprintf("line-%d", i+1))
		switch kind {
		case "session", "session.started":
			records = append(records, Record{
				Order:       order,
				NativeID:    nativeID + ":session",
				TimestampMS: timestamp,
				Type:        "session_started",
				Content:     entry.CWD,
				ModelID:     firstNonEmpty(entry.ModelID, entry.Model),
				Raw:         map[string]any{"entry": entry.Raw},
			})
			order++
		case "message":
			if entry.Message == nil {
				continue
			}
			for _, rec := range recordsFromMessage(nativeID, timestamp, entry.Message, entry.Raw) {
				rec.Order = order
				records = append(records, rec)
				order++
			}
		case "model_change":
			// Model changes are context for later records; the session-store format also repeats
			// the model on messages, which is what mapper events use.
		case "custom_message":
			customType := strings.ToLower(strings.TrimSpace(entry.CustomType))
			if customType == "openclaw.runtime-context" {
				if text := textFromAny(entry.Content); text != "" && !strings.HasPrefix(text, "Read HEARTBEAT.md") {
					records = append(records, Record{
						Order:       order,
						NativeID:    nativeID + ":context",
						TimestampMS: timestamp,
						Type:        "context_injection",
						Content:     text,
						Raw:         map[string]any{"entry": entry.Raw},
					})
					order++
				}
			}
		case "error":
			records = append(records, Record{
				Order:       order,
				NativeID:    nativeID + ":error",
				TimestampMS: timestamp,
				Type:        "error",
				Content:     firstNonEmpty(textFromAny(entry.Content), textFromAny(entry.Error)),
				Raw:         map[string]any{"entry": entry.Raw},
			})
			order++
		}
	}
	return records
}

func recordsFromMessage(baseID string, timestamp int64, msg *Message, raw map[string]any) []Record {
	role := strings.ToLower(strings.TrimSpace(msg.Role))
	ts := parseTimestamp(msg.Timestamp, timestamp)
	model := firstNonEmpty(msg.ModelID, msg.Model)
	blocks := normalizeContentBlocks(msg.Content)
	var records []Record
	type callContext struct {
		name string
		args map[string]any
	}
	calls := map[string]callContext{}
	for i, block := range blocks {
		blockID := firstNonEmpty(block.ID, fmt.Sprintf("%d", i+1))
		blockType := strings.ToLower(strings.TrimSpace(block.Type))
		nativeID := baseID + ":" + blockID
		switch {
		case role == "user" && (blockType == "" || blockType == "text"):
			if text := firstNonEmpty(block.Text, textFromAny(block.Content)); text != "" {
				records = append(records, Record{NativeID: nativeID + ":user", TimestampMS: ts, Type: "user_message", Role: role, Content: cleanUserText(text), ModelID: model, Raw: map[string]any{"message": raw, "block": block.Raw}})
			}
		case role == "assistant" && (blockType == "" || blockType == "text"):
			if text := firstNonEmpty(block.Text, textFromAny(block.Content)); text != "" {
				records = append(records, Record{NativeID: nativeID + ":assistant", TimestampMS: ts, Type: "assistant_text", Role: role, Content: cleanAssistantText(text), ModelID: model, Raw: map[string]any{"message": raw, "block": block.Raw}})
			}
		case role == "assistant" && (blockType == "thinking" || blockType == "reasoning"):
			if text := firstNonEmpty(block.Text, textFromAny(block.Content)); text != "" {
				records = append(records, Record{NativeID: nativeID + ":reasoning", TimestampMS: ts, Type: "agent_reasoning", Role: role, Content: text, ModelID: model, Raw: map[string]any{"message": raw, "block": block.Raw}})
			}
		case isToolCallBlock(blockType):
			callID := firstNonEmpty(block.CallID, block.CallIDAlt, block.ID, blockID)
			name := firstNonEmpty(block.ToolName, block.ToolNameAlt, block.Tool, block.Name)
			args := parseArgs(firstPresent(block.Input, block.Args, block.Arguments, block.Content))
			if callID != "" {
				calls[callID] = callContext{name: name, args: args}
			}
			records = append(records, Record{
				NativeID:    nativeID + ":tool-call",
				TimestampMS: ts,
				Type:        "tool_call",
				Role:        role,
				ModelID:     model,
				ToolName:    name,
				CallID:      callID,
				Args:        args,
				Status:      "invoked",
				Raw:         map[string]any{"message": raw, "block": block.Raw},
			})
		case isToolResultBlock(blockType):
			callID := firstNonEmpty(block.CallID, block.CallIDAlt, block.ID, blockID)
			name := firstNonEmpty(block.ToolName, block.ToolNameAlt, block.Tool, block.Name)
			args := map[string]any(nil)
			if ctx, ok := calls[callID]; ok {
				name = firstNonEmpty(name, ctx.name)
				args = ctx.args
			}
			status := strings.ToLower(firstNonEmpty(block.Status, "success"))
			if block.Error != nil && textFromAny(block.Error) != "" {
				status = "error"
			}
			records = append(records, Record{
				NativeID:    nativeID + ":tool-result",
				TimestampMS: ts,
				Type:        "tool_result",
				Role:        role,
				ModelID:     model,
				ToolName:    name,
				CallID:      callID,
				Args:        args,
				Output:      firstPresent(block.Output, block.Result, block.Content, block.Error),
				Status:      status,
				Raw:         map[string]any{"message": raw, "block": block.Raw},
			})
		}
	}
	if msg.Usage != nil && hasUsage(msg.Usage) {
		records = append(records, Record{
			NativeID:    baseID + ":usage",
			TimestampMS: ts,
			Type:        "token_usage",
			ModelID:     model,
			Tokens:      msg.Usage,
			Raw:         map[string]any{"message": raw},
		})
	}
	return records
}

func normalizeContentBlocks(value any) []ContentBlock {
	if s, ok := value.(string); ok {
		return []ContentBlock{{Type: "text", Text: s}}
	}
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]ContentBlock, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, ContentBlock{Type: "text", Text: s})
			continue
		}
		data, err := json.Marshal(item)
		if err != nil {
			continue
		}
		var raw map[string]any
		_ = json.Unmarshal(data, &raw)
		var block ContentBlock
		if err := json.Unmarshal(data, &block); err != nil {
			continue
		}
		block.Raw = raw
		out = append(out, block)
	}
	return out
}

func isToolCallBlock(kind string) bool {
	switch strings.ReplaceAll(kind, "-", "_") {
	case "toolcall", "tool_call", "tooluse", "tool_use", "function_call":
		return true
	default:
		return false
	}
}

func isToolResultBlock(kind string) bool {
	switch strings.ReplaceAll(kind, "-", "_") {
	case "toolresult", "tool_result", "tooloutput", "tool_output", "function_call_output":
		return true
	default:
		return false
	}
}

func hasUsage(usage *Usage) bool {
	return usage.Input > 0 || usage.Output > 0 || usage.CacheRead > 0 || usage.CacheWrite > 0 || usage.Reasoning > 0 || usage.ReasoningTokens > 0
}

func parseArgs(value any) map[string]any {
	switch v := value.(type) {
	case nil:
		return nil
	case map[string]any:
		return v
	case string:
		var out map[string]any
		if err := json.Unmarshal([]byte(v), &out); err == nil {
			return out
		}
		return map[string]any{"raw": v}
	default:
		return map[string]any{"raw": v}
	}
}

func textFromAny(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case nil:
		return ""
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprint(v)
		}
		return string(data)
	}
}

func extractText(value any) string {
	blocks := normalizeContentBlocks(value)
	for _, block := range blocks {
		if strings.ToLower(block.Type) == "text" || block.Type == "" {
			return firstNonEmpty(block.Text, textFromAny(block.Content))
		}
	}
	return textFromAny(value)
}

func directoryFromAny(value any) string {
	switch v := value.(type) {
	case map[string]any:
		for _, key := range []string{"workspaceDir", "workspace", "workspaceDir", "cwd", "workdir", "directory", "dir", "projectPath", "projectDir", "root", "rootDir"} {
			if s, ok := v[key].(string); ok && strings.TrimSpace(s) != "" {
				return s
			}
		}
	case string:
		var decoded map[string]any
		if err := json.Unmarshal([]byte(v), &decoded); err == nil {
			return directoryFromAny(decoded)
		}
	}
	return ""
}

func firstPresent(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func cleanUserText(text string) string {
	return strings.TrimSpace(text)
}

func cleanAssistantText(text string) string {
	return strings.TrimSpace(strings.TrimPrefix(text, "[[reply_to_current]]"))
}
