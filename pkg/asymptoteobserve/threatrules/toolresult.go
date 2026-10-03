package threatrules

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// ToolResultTextPath is the CEL path of the derived tool-result text, after the "e." prefix.
const ToolResultTextPath = "gen_ai.tool.call.result_text"

// ToolResultTextLimit bounds how much tool-result text one event exposes to rules. It is the
// limit the writers already apply to retained text such as prompt.text and command.output, so
// a rule over tool results costs what a rule over command output costs: CEL's matches() is RE2,
// which runs in time linear in its input, and this caps the input.
const ToolResultTextLimit = asymptoteobserve.DefaultStringLimit

// ToolResultText returns the text a rule sees as e.gen_ai.tool.call.result_text: what a tool
// that brings outside content into the agent's context returned, as retained in the log.
//
// It is empty unless all of these hold:
//
//   - The event carries gen_ai.tool.call.result.
//   - The event is a read-type tool result: file.read, mcp.tool_invoked, a tool named as an
//     MCP tool (mcp__server__tool, MCP:tool), or a web fetch/search tool. These are the
//     results whose content came from somewhere other than the agent itself, which is where
//     indirect prompt injection arrives. A shell command's output already has its own field
//     (command.output), and a write or edit echoes what the agent produced.
//   - The event's content marker, if it has one, says content was retained (included, and
//     retention other than "metadata").
//
// A string result is used as is. An object or list result -- a hook's tool_response, an MCP
// content-block list -- contributes every string it contains, in key order, one per line.
// The text is then capped at ToolResultTextLimit and passed through the same secret redaction
// the writers apply, so a rule never sees more than was retained and never sees a credential
// the writer would have removed, whichever path produced the event.
//
// It is exported so that another engine implementing the Threat Rules contract can derive the
// field the same way; fixtures that exercise it only conform if the derivation agrees.
func ToolResultText(event asymptoteobserve.Event) string {
	if event.GenAI == nil || event.GenAI.Tool == nil || event.GenAI.Tool.Call == nil {
		return ""
	}
	result := event.GenAI.Tool.Call.Result
	if result == nil || !toolResultIsIngestedContent(event) || !contentRetained(event.Content) {
		return ""
	}
	var parts []string
	collectStrings(result, &parts)
	text := strings.TrimSpace(strings.Join(parts, "\n"))
	if text == "" {
		return ""
	}
	return asymptoteobserve.CleanString(text, ToolResultTextLimit, true)
}

// withDerivedFields returns event with the engine-derived fields filled in. The sub-objects on
// the path are copied rather than written through, so the caller's event -- which is shared
// across rules and may be evidence in a Finding -- is never mutated.
func withDerivedFields(event asymptoteobserve.Event) asymptoteobserve.Event {
	text := ToolResultText(event)
	if text == "" && (event.GenAI == nil || event.GenAI.Tool == nil || event.GenAI.Tool.Call == nil || event.GenAI.Tool.Call.ResultText == "") {
		return event
	}
	// Either there is text to set or an in-process caller set the field itself; the field is
	// derived, never asserted, so the second case is cleared.
	genAI := *event.GenAI
	tool := *genAI.Tool
	call := *tool.Call
	call.ResultText = text
	tool.Call = &call
	genAI.Tool = &tool
	event.GenAI = &genAI
	return event
}

func contentRetained(content *asymptoteobserve.ContentInfo) bool {
	if content == nil {
		// Most tool-result events carry no marker. What is in the log is what the writer
		// retained, after its redaction and truncation.
		return true
	}
	return content.Included && content.Retention != asymptoteobserve.ContentRetentionMetadata
}

// toolNameSeparators folds web_fetch, web-fetch and WebFetch to one spelling.
var toolNameSeparators = strings.NewReplacer("_", "", "-", "")

func toolResultIsIngestedContent(event asymptoteobserve.Event) bool {
	switch event.Event.Action {
	case "file.read", "mcp.tool_invoked":
		return true
	}
	if event.MCP != nil && (event.MCP.Server != "" || event.MCP.Tool != "") {
		return true
	}
	for _, name := range toolNames(event) {
		lower := strings.ToLower(strings.TrimSpace(name))
		if strings.HasPrefix(lower, "mcp__") || strings.HasPrefix(lower, "mcp:") {
			return true
		}
		switch toolNameSeparators.Replace(lower) {
		case "webfetch", "websearch", "fetch", "fetchurl", "readurl":
			return true
		}
	}
	return false
}

func toolNames(event asymptoteobserve.Event) []string {
	var names []string
	if event.Tool != nil && event.Tool.Name != "" {
		names = append(names, event.Tool.Name)
	}
	if event.GenAI != nil && event.GenAI.Tool != nil && event.GenAI.Tool.Name != "" {
		names = append(names, event.GenAI.Tool.Name)
	}
	return names
}

// collectStrings appends every string in v, walking maps in sorted key order so the text is
// the same for the same result on every run.
func collectStrings(v interface{}, out *[]string) {
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
			collectStrings(typed[key], out)
		}
	case []interface{}:
		for _, item := range typed {
			collectStrings(item, out)
		}
	case nil, bool, float64, int, int64, json.Number:
		// Scalars other than strings are sizes, counts and flags, not content.
	default:
		// A typed value from an in-process caller (a mapper's struct or []map) rather than
		// the generic shape a log line decodes to. Normalize it through JSON once.
		data, err := json.Marshal(typed)
		if err != nil {
			return
		}
		var generic interface{}
		if json.Unmarshal(data, &generic) == nil {
			// generic is now a string, map, list or scalar, all handled above without
			// reaching this case again.
			collectStrings(generic, out)
		}
	}
}
