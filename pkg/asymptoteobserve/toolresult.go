package asymptoteobserve

import (
	"encoding/json"
	"sort"
	"strings"
)

// IngestedContentAction reports whether an event action names a tool result whose content came
// from outside the agent -- a file it read, an MCP server's reply -- and so is where indirect
// prompt injection arrives.
//
// This and IngestedContentToolName are the one definition of that scope. The threat-rules engine
// reads them to decide which events expose gen_ai.tool.call.result_text, and the hook adapter
// reads them to decide which tool results it records, so a capture path cannot drop a result the
// engine would have matched on, or record one the engine ignores.
func IngestedContentAction(action string) bool {
	switch action {
	case "file.read", "mcp.tool_invoked":
		return true
	}
	return false
}

// toolNameSeparators folds web_fetch, web-fetch and WebFetch to one spelling.
var toolNameSeparators = strings.NewReplacer("_", "", "-", "")

// IngestedContentToolName reports whether a tool's name says it brings outside content into the
// agent's context whatever action it was classified as: an MCP tool (mcp__server__tool,
// MCP:tool), or a web fetch or search. Those are recorded as tool.invoked or tool.completed on
// most capture paths, so the action alone misses them.
func IngestedContentToolName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if strings.HasPrefix(lower, "mcp__") || strings.HasPrefix(lower, "mcp:") {
		return true
	}
	switch toolNameSeparators.Replace(lower) {
	case "webfetch", "websearch", "fetch", "fetchurl", "readurl":
		return true
	}
	return false
}

// ToolResultPlainText renders a tool result as text: a string as is; for an object or list (a
// hook's tool_response, an MCP content-block list), every string it contains, maps in sorted key
// order, one per line, trimmed. Numbers, booleans and nulls are sizes, counts and flags rather
// than content and contribute nothing. Deterministic for the same result on every run.
//
// It does no redaction or truncation; callers apply the limit that governs where the text goes.
func ToolResultPlainText(result interface{}) string {
	var parts []string
	collectToolResultStrings(result, &parts)
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func collectToolResultStrings(v interface{}, out *[]string) {
	switch typed := v.(type) {
	case string:
		if strings.TrimSpace(typed) != "" {
			*out = append(*out, typed)
		}
	case map[string]interface{}:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			collectToolResultStrings(typed[key], out)
		}
	case []interface{}:
		for _, item := range typed {
			collectToolResultStrings(item, out)
		}
	case nil, bool, float64, float32, int, int64, int32, json.Number:
	default:
		// A typed value from an in-process caller (a mapper's struct or []map) rather than the
		// generic shape a log line decodes to. Normalize it through JSON once; the result is a
		// string, map, list or scalar, all handled above without reaching this case again.
		data, err := json.Marshal(typed)
		if err != nil {
			return
		}
		var generic interface{}
		if json.Unmarshal(data, &generic) == nil {
			collectToolResultStrings(generic, out)
		}
	}
}
