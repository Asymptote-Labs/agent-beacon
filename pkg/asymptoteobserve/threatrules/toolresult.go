package threatrules

import (
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
//     MCP tool (mcp__server__tool, MCP:tool), a web fetch/search tool, or a tool whose
//     tool.path is an http or https URL. These are the
//     results whose content came from somewhere other than the agent itself, which is where
//     indirect prompt injection arrives. A shell command's output already has its own field
//     (command.output), and a write or edit echoes what the agent produced.
//   - The event's content marker, if it has one, says content was retained (included, and
//     retention other than "metadata").
//
// A string result is used as is. An object or list result -- a hook's tool_response, an MCP
// content-block list -- contributes every string it contains, in key order, one per line,
// except that a content block in a list contributes its text without its metadata or encoded
// bytes (asymptoteobserve.ToolResultPlainText says exactly which fields).
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
	text := asymptoteobserve.ToolResultPlainText(result)
	if text == "" {
		return ""
	}
	return asymptoteobserve.CleanString(text, ToolResultTextLimit, true)
}

// withDerivedFields returns event with the engine-derived fields filled in. The sub-objects on
// the path are copied rather than written through, so the caller's event -- which is shared
// across rules and may be evidence in a Finding -- is never mutated.
func withDerivedFields(event asymptoteobserve.Event) asymptoteobserve.Event {
	if event.GenAI == nil {
		return event
	}
	resultText := ToolResultText(event)
	instructionsText := SystemInstructionsText(event)
	callAsserted := event.GenAI.Tool != nil && event.GenAI.Tool.Call != nil && event.GenAI.Tool.Call.ResultText != ""
	if resultText == "" && !callAsserted && instructionsText == "" && event.GenAI.SystemInstructionsText == "" {
		return event
	}
	// Either there is text to set or an in-process caller set a field itself; the fields are
	// derived, never asserted, so the second case is cleared.
	genAI := *event.GenAI
	genAI.SystemInstructionsText = instructionsText
	if resultText != "" || callAsserted {
		tool := *genAI.Tool
		call := *tool.Call
		call.ResultText = resultText
		tool.Call = &call
		genAI.Tool = &tool
	}
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

func toolResultIsIngestedContent(event asymptoteobserve.Event) bool {
	if asymptoteobserve.IngestedContentAction(event.Event.Action) {
		return true
	}
	if event.MCP != nil && (event.MCP.Server != "" || event.MCP.Tool != "") {
		return true
	}
	if event.Tool != nil && asymptoteobserve.IngestedContentTarget(event.Tool.Path) {
		return true
	}
	for _, name := range toolNames(event) {
		if asymptoteobserve.IngestedContentToolName(name) {
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
