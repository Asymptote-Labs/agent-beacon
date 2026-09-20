package clinesession

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func RecordsFromEntries(ref TraceRef, entries []Entry) []Record {
	var records []Record
	calls := map[string]toolState{}
	order := 1
	var lastModel string
	for _, entry := range entries {
		role := strings.ToLower(strings.TrimSpace(entry.Role))
		if role != "user" && role != "assistant" {
			continue
		}
		model := firstNonEmpty(entry.ModelID, entry.Model, stringValue(entry.ModelInfo["modelId"]), stringValue(entry.ModelInfo["model"]), stringValue(entry.ModelInfo["id"]), lastModel)
		if model != "" {
			lastModel = model
		}
		ts := parseTimestampMS(entry.Timestamp, ref.UpdatedAtUnixMS)
		before := len(records)
		for _, block := range normalizeContentBlocks(entry.Content, entry.Text, entry.Message) {
			record, more := recordFromContentBlock(role, block, calls, entry, model, ts, order)
			if record != nil {
				records = append(records, *record)
				order++
			}
			for _, extra := range more {
				extra.Order = order
				records = append(records, extra)
				order++
			}
		}
		if len(records) == before {
			text := extractVisibleText(role, firstNonEmpty(extractText(entry.Content), entry.Text, entry.Message))
			if text != "" {
				typ := "agent_text"
				if role == "user" {
					typ = "user_message"
				}
				records = append(records, Record{Order: order, Type: typ, TimestampMS: ts, Content: text, ModelID: model, Raw: entry.Raw})
				order++
			}
		}
		if role == "assistant" {
			usage := tokenUsage(firstNonNil(entry.Metrics, entry.Usage))
			if usage != nil {
				for i := len(records) - 1; i >= before; i-- {
					if records[i].Type == "agent_text" || records[i].Type == "agent_reasoning" || records[i].Type == "tool_call" {
						records[i].Tokens = usage
						break
					}
				}
			}
		}
	}
	return records
}

func recordFromContentBlock(role string, block interface{}, calls map[string]toolState, entry Entry, model string, ts int64, order int) (*Record, []Record) {
	if text, ok := block.(string); ok {
		return recordFromText(role, text, calls, entry, model, ts, order), nil
	}
	obj, ok := block.(map[string]interface{})
	if !ok || obj == nil {
		return nil, nil
	}
	typ := strings.ToLower(strings.TrimSpace(stringValue(obj["type"])))
	if typ == "" || typ == "text" {
		text := firstNonEmpty(extractText(obj["text"]), extractText(obj["content"]), extractText(obj["value"]), extractText(obj["message"]))
		return recordFromText(role, text, calls, entry, model, ts, order), nil
	}
	if typ == "thinking" || typ == "reasoning" || typ == "redacted_thinking" {
		text := firstNonEmpty(extractText(obj["thinking"]), extractText(obj["text"]), extractText(obj["content"]))
		if role == "assistant" && strings.TrimSpace(text) != "" {
			return &Record{Order: order, Type: "agent_reasoning", TimestampMS: ts, Content: text, ModelID: model, Raw: entry.Raw}, nil
		}
		return nil, nil
	}
	if typ == "tool_use" || typ == "tool-call" || typ == "tool_call" {
		callID := firstNonEmpty(stringValue(obj["id"]), stringValue(obj["tool_use_id"]), stringValue(obj["callId"]), fmt.Sprintf("entry-%d-call-%d", entry.Order, order))
		name := firstNonEmpty(stringValue(obj["name"]), stringValue(obj["toolName"]), "unknown")
		args := parseToolArgs(firstNonNil(obj["input"], obj["args"], obj["arguments"]))
		calls[callID] = toolState{Name: name, Args: args}
		record := &Record{Order: order, Type: "tool_call", TimestampMS: ts, ModelID: model, ToolName: name, CallID: callID, Args: args, Raw: entry.Raw}
		var extra []Record
		if role == "assistant" {
			if text := assistantToolMessage(name, args); text != "" {
				extra = append(extra, Record{Type: "agent_text", TimestampMS: ts, Content: text, ModelID: model, Raw: entry.Raw})
			}
		}
		return record, extra
	}
	if typ == "tool_result" || typ == "tool-result" || typ == "tool_response" {
		callID := firstNonEmpty(stringValue(obj["tool_use_id"]), stringValue(obj["callId"]), stringValue(obj["id"]), fmt.Sprintf("entry-%d-call", entry.Order))
		state := calls[callID]
		name := firstNonEmpty(state.Name, stringValue(obj["name"]), stringValue(obj["toolName"]), "unknown")
		args := state.Args
		if args == nil {
			args = map[string]interface{}{}
		}
		output := firstNonNil(obj["content"], obj["output"], obj["result"])
		outputText := extractToolResultOutput(output)
		status := "success"
		if obj["is_error"] == true || strings.ToLower(stringValue(obj["status"])) == "error" {
			status = "error"
		}
		delete(calls, callID)
		record := &Record{Order: order, Type: "tool_result", TimestampMS: ts, ModelID: model, ToolName: name, CallID: callID, Args: args, Output: output, OutputText: outputText, Status: status, Raw: entry.Raw}
		var extra []Record
		if role == "user" && name == "attempt_completion" {
			if feedback := extractTaggedText(outputText, "feedback"); feedback != "" {
				extra = append(extra, Record{Type: "user_message", TimestampMS: ts, Content: feedback, ModelID: model, Raw: entry.Raw})
			}
		}
		return record, extra
	}
	return nil, nil
}

func recordFromText(role, text string, calls map[string]toolState, entry Entry, model string, ts int64, order int) *Record {
	if role == "user" {
		if result := parseTextToolResult(text, calls, fmt.Sprintf("entry-%d-call", entry.Order)); result != nil {
			return &Record{Order: order, Type: "tool_result", TimestampMS: ts, ModelID: model, ToolName: result.ToolName, CallID: result.CallID, OutputText: result.Output, Output: result.Output, Status: "success", Raw: entry.Raw}
		}
	}
	visible := extractVisibleText(role, text)
	if visible == "" {
		return nil
	}
	typ := "agent_text"
	if role == "user" {
		typ = "user_message"
	}
	return &Record{Order: order, Type: typ, TimestampMS: ts, Content: visible, ModelID: model, Raw: entry.Raw}
}

type toolState struct {
	Name string
	Args map[string]interface{}
}

type parsedTextResult struct {
	CallID   string
	ToolName string
	Output   string
}

func parseTextToolResult(text string, calls map[string]toolState, fallbackID string) *parsedTextResult {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "[") {
		return nil
	}
	end := strings.Index(text, "]")
	if end <= 1 {
		return nil
	}
	rest := strings.TrimSpace(text[end+1:])
	if !strings.HasPrefix(rest, "Result:") {
		return nil
	}
	name := text[1:end]
	if idx := strings.Index(strings.ToLower(name), " for "); idx > 0 {
		name = name[:idx]
	}
	name = strings.TrimSpace(name)
	callID := fallbackID
	for id, tool := range calls {
		if normalizeToolKey(tool.Name) == normalizeToolKey(name) {
			callID = id
			name = tool.Name
			break
		}
	}
	return &parsedTextResult{CallID: callID, ToolName: name, Output: strings.TrimSpace(strings.TrimPrefix(rest, "Result:"))}
}

func normalizeToolKey(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(value) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func normalizeContentBlocks(values ...interface{}) []interface{} {
	for _, value := range values {
		switch v := value.(type) {
		case nil:
			continue
		case []interface{}:
			return v
		case string:
			if strings.TrimSpace(v) != "" {
				return []interface{}{v}
			}
		default:
			return []interface{}{v}
		}
	}
	return nil
}

func extractVisibleText(role, text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if role == "assistant" {
		return text
	}
	for _, tag := range []string{"user_message", "user_input", "feedback", "task"} {
		if tagged := extractTaggedText(text, tag); tagged != "" {
			return tagged
		}
	}
	if idx := strings.Index(text, "[TASK RESUMPTION]"); idx >= 0 {
		return strings.TrimSpace(text[idx:])
	}
	if strings.Contains(text, "<environment_details>") || strings.HasPrefix(text, "# task_progress") || strings.HasPrefix(text, "[ERROR] You did not use a tool") || strings.HasPrefix(text, "New instructions for task continuation:") || strings.HasPrefix(text, "Command executed.\nOutput:") || strings.HasPrefix(text, "The user has provided feedback on the results.") {
		return ""
	}
	return text
}

func extractTaggedText(text, tag string) string {
	open := "<" + tag
	start := strings.Index(strings.ToLower(text), strings.ToLower(open))
	if start < 0 {
		return ""
	}
	gt := strings.Index(text[start:], ">")
	if gt < 0 {
		return ""
	}
	bodyStart := start + gt + 1
	close := "</" + tag + ">"
	end := strings.Index(strings.ToLower(text[bodyStart:]), strings.ToLower(close))
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(text[bodyStart : bodyStart+end])
}

func extractText(value interface{}) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case []interface{}:
		var parts []string
		for _, item := range v {
			if text := extractText(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n\n")
	case map[string]interface{}:
		if stringValue(v["type"]) == "image" {
			return ""
		}
		return firstNonEmpty(stringValue(v["text"]), stringValue(v["content"]), stringValue(v["message"]), stringValue(v["value"]), stringValue(v["output"]))
	default:
		return ""
	}
}

func extractToolResultOutput(value interface{}) string {
	if text := extractText(value); text != "" {
		return text
	}
	if value == nil {
		return "(no output)"
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(data)
}

func parseToolArgs(value interface{}) map[string]interface{} {
	if obj, ok := value.(map[string]interface{}); ok && obj != nil {
		return obj
	}
	if text, ok := value.(string); ok {
		var obj map[string]interface{}
		if err := json.Unmarshal([]byte(text), &obj); err == nil && obj != nil {
			return obj
		}
		if strings.TrimSpace(text) != "" {
			return map[string]interface{}{"raw": text}
		}
	}
	if value == nil {
		return map[string]interface{}{}
	}
	return map[string]interface{}{"raw": value}
}

func assistantToolMessage(name string, args map[string]interface{}) string {
	switch name {
	case "attempt_completion":
		return stringValue(args["result"])
	case "ask_followup_question":
		return stringValue(args["question"])
	default:
		return ""
	}
}

func tokenUsage(value interface{}) *TokenUsage {
	obj, ok := value.(map[string]interface{})
	if !ok || obj == nil {
		return nil
	}
	usage := &TokenUsage{
		Input:      intValue(firstNonNil(obj["inputTokens"], obj["input_tokens"], obj["tokensIn"], obj["promptTokens"])),
		Output:     intValue(firstNonNil(obj["outputTokens"], obj["output_tokens"], obj["tokensOut"], obj["completionTokens"])),
		CacheRead:  intValue(firstNonNil(obj["cacheReadTokens"], obj["cache_read_tokens"], obj["cachedTokens"])),
		CacheWrite: intValue(firstNonNil(obj["cacheWriteTokens"], obj["cache_write_tokens"], obj["cacheCreationTokens"])),
		Reasoning:  intValue(firstNonNil(obj["thoughtsTokenCount"], obj["reasoningTokenCount"], obj["reasoningTokens"])),
		CostUSD:    floatValue(firstNonNil(obj["totalCost"], obj["total_cost"], obj["costUsd"], obj["cost_usd"], obj["cost"])),
	}
	if usage.Input == 0 && usage.Output == 0 && usage.CacheRead == 0 && usage.CacheWrite == 0 && usage.Reasoning == 0 && usage.CostUSD == 0 {
		return nil
	}
	return usage
}

func parseTimestampMS(value interface{}, fallback int64) int64 {
	switch v := value.(type) {
	case int64:
		if v > 0 && v < 1_000_000_000_000 {
			return v * 1000
		}
		return v
	case int:
		return parseTimestampMS(int64(v), fallback)
	case float64:
		if !math.IsNaN(v) && v > 0 {
			if v < 1_000_000_000_000 {
				return int64(v * 1000)
			}
			return int64(v)
		}
	case string:
		if n, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return parseTimestampMS(n, fallback)
		}
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return t.UnixMilli()
		}
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t.UnixMilli()
		}
	}
	return fallback
}

func stringValue(value interface{}) string {
	if value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		return ""
	}
}

func intValue(value interface{}) int64 {
	switch v := value.(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n
	default:
		return 0
	}
}

func floatValue(value interface{}) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case string:
		n, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return n
	default:
		return 0
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstNonNil(values ...interface{}) interface{} {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func truncate(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit]
}

func fileLanguage(path string) string {
	ext := strings.TrimPrefix(filepath.Ext(path), ".")
	return ext
}
