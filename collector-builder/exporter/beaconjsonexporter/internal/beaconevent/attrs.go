package beaconevent

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
)

// Ordered attribute-key precedence lists for resolving a tool name from the differing
// conventions emitted by runtimes. The orderings are deliberate and context-specific, so
// they are named here (rather than repeated as inline literals) to make the precedence
// explicit and reviewable:
//   - toolNameKeys: general resolution for the top-level Tool.Name (OTel tool.name first).
//   - genAIToolNameKeys: GenAI tool info prefers the gen_ai.* name.
//   - mcpToolNameKeys: MCP activity prefers the mcp.* name.
//   - codexToolNameKeys: Codex tool-result records use their own field set.
var (
	toolNameKeys      = []string{"tool.name", "gen_ai.tool.name", "mcp.tool.name", "function_name", "tool_name"}
	genAIToolNameKeys = []string{"gen_ai.tool.name", "tool.name"}
	mcpToolNameKeys   = []string{"mcp.tool.name", "tool.name", "function_name"}
	codexToolNameKeys = []string{"tool.name", "tool_name", "function_name", "tool", "mcp_server"}
)

func AttrsToMap(attrs pcommon.Map) map[string]interface{} {
	out := make(map[string]interface{}, attrs.Len())
	attrs.Range(func(k string, v pcommon.Value) bool {
		out[k] = v.AsRaw()
		return true
	})
	return out
}

func MergeMaps(a, b map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func FirstString(attrs map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := attrs[key]; ok {
			if str := strings.TrimSpace(fmt.Sprint(value)); str != "" && str != "<nil>" {
				return str
			}
		}
	}
	return ""
}

func RunString(attrs map[string]interface{}, keys ...string) string {
	value := FirstString(attrs, keys...)
	if value == "" {
		return ""
	}
	decoded, err := url.PathUnescape(value)
	if err != nil {
		return value
	}
	return decoded
}

func ToolCommandString(attrs map[string]interface{}) string {
	if command := FirstString(attrs, "tool.command", "command", "function_args"); command != "" {
		return command
	}
	return FirstStringAttr(attrs, "gen_ai.tool.call.arguments")
}

func FirstStringAttr(attrs map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := attrs[key]; ok {
			if str, ok := value.(string); ok {
				if trimmed := strings.TrimSpace(str); trimmed != "" {
					return trimmed
				}
			}
		}
	}
	return ""
}

func FirstTextAttr(attrs map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := attrs[key]; ok {
			if text := firstTextFromAny(value); text != "" {
				return text
			}
		}
	}
	return ""
}

func HasAttr(attrs map[string]interface{}, key string) bool {
	_, ok := attrs[key]
	return ok
}

func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func IntAttr(attrs map[string]interface{}, keys ...string) (int, bool) {
	for _, key := range keys {
		switch typed := attrs[key].(type) {
		case int:
			return typed, true
		case int64:
			if !FitsInInt(typed) {
				continue
			}
			return int(typed), true
		case float64:
			value := int64(typed)
			if !FitsInInt(value) {
				continue
			}
			return int(value), true
		case string:
			value, err := strconv.Atoi(strings.TrimSpace(typed))
			if err == nil {
				return value, true
			}
		}
	}
	return 0, false
}

func FitsInInt(value int64) bool {
	if strconv.IntSize == 32 {
		return value >= -1<<31 && value <= 1<<31-1
	}
	return true
}

func Int64Attr(attrs map[string]interface{}, keys ...string) (int64, bool) {
	for _, key := range keys {
		switch typed := attrs[key].(type) {
		case int:
			return int64(typed), true
		case int64:
			return typed, true
		case float64:
			return int64(typed), true
		case string:
			value, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
			if err == nil {
				return value, true
			}
		}
	}
	return 0, false
}

func FloatAttr(attrs map[string]interface{}, keys ...string) (float64, bool) {
	for _, key := range keys {
		switch typed := attrs[key].(type) {
		case float32:
			return float64(typed), true
		case float64:
			return typed, true
		case int:
			return float64(typed), true
		case int64:
			return float64(typed), true
		case string:
			value, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
			if err == nil {
				return value, true
			}
		}
	}
	return 0, false
}

func BoolAttr(attrs map[string]interface{}, keys ...string) (bool, bool) {
	for _, key := range keys {
		switch typed := attrs[key].(type) {
		case bool:
			return typed, true
		case string:
			value, err := strconv.ParseBool(strings.TrimSpace(typed))
			if err == nil {
				return value, true
			}
		}
	}
	return false, false
}

func StringSliceAttr(attrs map[string]interface{}, keys ...string) []string {
	for _, key := range keys {
		value, ok := attrs[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case []string:
			return typed
		case []interface{}:
			out := make([]string, 0, len(typed))
			for _, item := range typed {
				if str := strings.TrimSpace(fmt.Sprint(item)); str != "" && str != "<nil>" {
					out = append(out, str)
				}
			}
			if len(out) > 0 {
				return out
			}
		case string:
			trimmed := strings.TrimSpace(typed)
			if trimmed == "" {
				continue
			}
			var parsed []string
			if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil {
				return parsed
			}
			if strings.Contains(trimmed, ",") {
				parts := strings.Split(trimmed, ",")
				out := make([]string, 0, len(parts))
				for _, part := range parts {
					if part = strings.TrimSpace(part); part != "" {
						out = append(out, part)
					}
				}
				if len(out) > 0 {
					return out
				}
			}
			return []string{trimmed}
		}
	}
	return nil
}

func AnyAttr(attrs map[string]interface{}, keys ...string) (interface{}, bool) {
	for _, key := range keys {
		value, ok := attrs[key]
		if !ok || value == nil {
			continue
		}
		if str, ok := value.(string); ok {
			trimmed := strings.TrimSpace(str)
			if trimmed == "" {
				continue
			}
			if decoded, ok := DecodeJSONValue(trimmed); ok {
				return decoded, true
			}
			return trimmed, true
		}
		return value, true
	}
	return nil, false
}

func DecodeJSONValue(value string) (interface{}, bool) {
	if value == "" {
		return nil, false
	}
	first := value[0]
	if first != '{' && first != '[' && first != '"' {
		return nil, false
	}
	var decoded interface{}
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		return nil, false
	}
	return decoded, true
}

func LegacyMessages(attrs map[string]interface{}, prefix, role string) []interface{} {
	type messagePart struct {
		index int
		text  string
	}
	var parts []messagePart
	for key, value := range attrs {
		if !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, ".content") {
			continue
		}
		indexText := strings.TrimSuffix(strings.TrimPrefix(key, prefix), ".content")
		index, err := strconv.Atoi(indexText)
		if err != nil {
			continue
		}
		text := strings.TrimSpace(fmt.Sprint(value))
		if text == "" || text == "<nil>" {
			continue
		}
		parts = append(parts, messagePart{index: index, text: text})
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].index < parts[j].index })
	out := make([]interface{}, 0, len(parts))
	for _, part := range parts {
		out = append(out, map[string]interface{}{
			"role": role,
			"parts": []interface{}{
				map[string]interface{}{"type": "text", "content": part.text},
			},
		})
	}
	return out
}

func FirstMessageText(genai *GenAIInfo) string {
	if genai == nil || genai.Input == nil || genai.Input.Messages == nil {
		return ""
	}
	return firstTextFromAny(genai.Input.Messages)
}

func firstTextFromAny(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return meaningfulText(typed)
	case []interface{}:
		for _, item := range typed {
			if text := firstTextFromAny(item); text != "" {
				return text
			}
		}
	case map[string]interface{}:
		if content, ok := typed["content"]; ok {
			if text := firstTextFromAny(content); text != "" {
				return text
			}
		}
		if parts, ok := typed["parts"]; ok {
			return firstTextFromAny(parts)
		}
		if messages, ok := typed["messages"]; ok {
			return firstTextFromAny(messages)
		}
	}
	return ""
}

func meaningfulText(value string) string {
	trimmed := strings.TrimSpace(value)
	switch strings.ToLower(trimmed) {
	case "", "<nil>", "{}", "[]", "null":
		return ""
	default:
		return trimmed
	}
}

func IsZeroJSON(value interface{}) bool {
	data, err := json.Marshal(value)
	if err != nil {
		return false
	}
	return string(data) == "{}"
}

func Timestamp(ts time.Time) time.Time {
	if ts.IsZero() || ts.UnixNano() == 0 {
		return time.Now().UTC()
	}
	return ts
}

func Severity(text, number string) string {
	lower := strings.ToLower(text + " " + number)
	switch {
	case strings.Contains(lower, "fatal") || strings.Contains(lower, "critical"):
		return "critical"
	case strings.Contains(lower, "error"):
		return "high"
	case strings.Contains(lower, "warn"):
		return "medium"
	default:
		return "info"
	}
}

func SpanSeverity(status string) string {
	if strings.Contains(strings.ToLower(status), "error") {
		return "high"
	}
	return "info"
}
