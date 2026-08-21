package cmd

import (
	"encoding/json"
	"strings"

	hookdiff "github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/diff"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/policycontract"
	"github.com/spf13/cobra"
)

// Pi (pi.dev) reports through one subcommand rather than one per hook, because it has no hooks
// configuration to install into: its only observation surface is the TypeScript extension API, so
// Beacon installs an extension that forwards every event it subscribes to here. That is the same
// arrangement as the opencode plugin, and it is what keeps the extension a dumb forwarder -- all
// classification, redaction and schema mapping stays in Go, where it is tested and where a fix
// reaches already-installed extensions without rewriting them.
//
// # Payload contract
//
// Both sides of this contract are Beacon's own code, so it is a fixed shape rather than a guess at
// somebody's API. The extension sends one JSON object on stdin:
//
//	type            required; the Pi event name ("session_start", "input", "tool_call", ...)
//	session_id      Pi's session id, from ctx.sessionManager
//	cwd             ctx.cwd
//	model           "<provider>/<model>", from ctx.model
//	session_file    path to Pi's own session JSONL, for correlation only -- never read
//	reason          session_start / session_shutdown reason ("startup", "new", "resume", "fork")
//	prompt          input event: the raw user text
//	tool_name       tool events: the tool being called
//	tool_call_id    tool events: Pi's toolCallId, which correlates a call with its result
//	tool_input      tool events: the arguments object
//	tool_response   tool_result: the result content
//	is_error        tool_result: whether Pi reported the tool as failed
//	duration_ms     tool_result: execution time
//	usage           message_end: Pi's usage object, verbatim
//	reasoning       message_end: concatenated thinking-block text
//	finish_reason   message_end: Pi's stopReason
//	message_id      message_end: the assistant message id
//
// camelCase spellings are accepted alongside snake_case throughout. The extension is TypeScript and
// forwards several of Pi's objects unchanged, so requiring one casing would mean transforming them
// in the shim -- more code in the layer that is hardest to test and hardest to update once
// installed.
//
// The response is a JSON object on stdout. It is empty except on one path: when BEACON_POLICY_PROVIDER
// names an executable and that provider denies a tool_call, the response is Pi's native deny shape,
// {"block":true,"reason":"..."}, which the extension returns to Pi unchanged. With no provider
// configured -- the open build's default -- the seam is inert and the response is always empty, so
// this binary ships no enforcement decisions of its own.
var piEventCmd = &cobra.Command{
	Use:   "pi-event",
	Short: "Record Pi extension telemetry",
	Long:  `pi-event receives Beacon Pi extension payloads and writes local endpoint telemetry.`,
	Run:   runPiEvent,
}

func init() {
	rootCmd.AddCommand(piEventCmd)
}

func runPiEvent(cmd *cobra.Command, args []string) {
	// Running this subcommand *is* the statement that the platform is Pi, so it does not depend on
	// --platform having been passed correctly. Several shared helpers read platformFlag rather than
	// taking a parameter -- working-directory resolution, the file-edit tool classifier, the raw
	// payload echo -- and a stale default would have them disagree with the rest of this command
	// about which runtime produced the event. The payload-driven override for Cursor hooks sets the
	// same global for the same reason.
	platformFlag = "pi"

	input, err := readStdinJSON()
	if err != nil {
		// Unparseable stdin is answered with an empty response rather than an error exit. Pi runs
		// this as a subprocess of an interactive session; a non-zero exit or a missing reply is a
		// visible failure in the user's terminal, and Beacon losing one event is strictly better
		// than Beacon making the agent look broken.
		outputJSON(emptyResponse)
		return
	}
	sessionID := resolveSessionID(input, "pi")
	logger := newHookLogger("pi-event", "pi", sessionID)

	// The policy seam runs on tool_call and only on tool_call: that is the one Pi event that fires
	// before the tool runs and whose handler Pi lets an extension block. Consulting it on a
	// tool_result would ask about work already done.
	//
	// A deny returns immediately without writing tool.invoked. The tool does not run, so recording
	// it as invoked would put an event in the log for something that never happened -- the same
	// reason the pre-tool hook returns early here. enforcePolicy has already written the
	// approval.denied event that does describe what happened.
	if getFirstStr(input, "type", "event", "event_type") == "tool_call" {
		if deny, denied := enforcePolicy(logger, input, sessionID, policycontract.PhasePreTool); denied {
			outputJSON(deny)
			return
		}
	}

	for _, event := range piEndpointEvents(input, sessionID) {
		if event.action == "" {
			continue
		}
		if err := logger.EndpointEvent(event.action, event.category, event.severity, event.message, event.fields); err != nil {
			logger.Error("Failed to write endpoint event", "error", err.Error(), "action", event.action)
		}
	}
	outputJSON(emptyResponse)
}

// piEndpointEvents translates one extension payload into the endpoint events it represents.
//
// Most Pi events produce exactly one. An assistant message produces two when it carried thinking
// text, because reasoning is a separate signal with its own action and its own retention decision;
// folding it into the response event would make one event whose content field describes two
// different pieces of text.
func piEndpointEvents(input map[string]interface{}, sessionID string) []normalizedEvent {
	eventType := getFirstStr(input, "type", "event", "event_type")
	fields := piBaseFields(input, sessionID)
	one := func(action, category, severity, message string, values map[string]interface{}) []normalizedEvent {
		return []normalizedEvent{{action: action, category: category, severity: severity, message: message, fields: values}}
	}

	switch eventType {
	case "session_start":
		return one("session.started", "session", "info", "Pi session started", fields)
	case "session_shutdown":
		return one("session.ended", "session", "info", "Pi session ended", fields)
	case "input":
		prompt := getFirstStr(input, "prompt", "text", "input")
		if prompt == "" {
			return nil
		}
		fields["prompt"] = map[string]interface{}{"text": prompt}
		fields["gen_ai"] = map[string]interface{}{
			"input": map[string]interface{}{
				"messages": []interface{}{map[string]interface{}{
					"role":  "user",
					"parts": []interface{}{map[string]interface{}{"type": "text", "content": prompt}},
				}},
			},
		}
		fields["content"] = retainedContentFields(prompt)
		return one("prompt.submitted", "prompt", "info", "Prompt submitted to Pi", fields)
	case "tool_call":
		// A tool_call is recorded as invoked, not as executed, even for a shell tool. Pi fires this
		// before the tool runs and an extension may still block it, so classifying a shell call as
		// command.executed here would report a command that never ran.
		mergeMap(fields, piToolFields(input, false))
		return one("tool.invoked", "tool", "info", "Pi tool invoked", fields)
	case "tool_result":
		mergeMap(fields, piToolFields(input, true))
		if piIsError(input) {
			fields["error"] = map[string]interface{}{"type": "tool_error"}
			return one("tool.failed", "tool", "high", "Pi tool failed", fields)
		}
		action, category := piToolAction(input)
		return one(action, category, "info", piToolMessage(action), fields)
	case "message_end":
		return piAssistantEvents(input, fields)
	case "agent_end":
		return one("session.status", "session", "info", "Pi agent turn ended", fields)
	default:
		// An unrecognized event type is dropped rather than recorded as a generic event. Pi's
		// extension API has events Beacon does not map (model_select, provider request hooks), and
		// writing a placeholder for each would fill the log with entries that carry no signal while
		// still costing the size budget every real event competes for.
		return nil
	}
}

// piBaseFields builds the context every Pi event carries.
//
// applyWorkspaceFields rather than emitHookEvent because this payload can produce several events and
// emitHookEvent writes exactly one; the enrichment it performs -- working directory, repository,
// branch -- is the part that matters and is shared.
func piBaseFields(input map[string]interface{}, sessionID string) map[string]interface{} {
	fields := sessionFields(sessionID, input)
	applyWorkspaceFields(fields, input, "")
	fields["raw"] = map[string]interface{}{"pi": input}
	if model := getFirstStr(input, "model"); model != "" {
		fields["model"] = model
	}
	return fields
}

// piToolFields maps a tool call or result onto the tool, command, file and MCP fields.
//
// completed distinguishes the two: a tool_call has arguments but no result, and only a result can
// carry an exit code, a diff, or output. Passing the flag rather than inferring it from the presence
// of tool_response keeps a tool that legitimately returns nothing from being treated as pending.
func piToolFields(input map[string]interface{}, completed bool) map[string]interface{} {
	toolName := piToolName(input)
	toolInput := jsonMap(input, "tool_input", "toolInput", "input", "arguments")
	toolResponse := jsonMap(input, "tool_response", "toolResponse", "result", "details")

	fields := toolFieldsWithResponse(toolName, toolInput, toolResponse)

	call := map[string]interface{}{}
	if len(toolInput) > 0 {
		call["arguments"] = toolInput
	}
	if completed && len(toolResponse) > 0 {
		if output, ok := toolResponse["output"]; ok {
			call["result"] = output
		} else {
			call["result"] = toolResponse
		}
	}
	if callID := getFirstStr(input, "tool_call_id", "toolCallId", "toolCallID", "call_id"); callID != "" {
		call["id"] = callID
	}
	// gen_ai.tool is set unconditionally so a call and its result share a correlatable shape even
	// when Pi reported neither arguments nor output.
	fields["gen_ai"] = mergeNested(fields["gen_ai"], map[string]interface{}{
		"operation": map[string]interface{}{"name": "execute_tool"},
		"tool": map[string]interface{}{
			"name": toolName,
			"call": call,
		},
	})

	if duration, ok := firstToolIntAcross([]map[string]interface{}{input}, "duration_ms", "durationMs"); ok {
		fields["tool"] = mergeNested(fields["tool"], map[string]interface{}{"duration_ms": duration})
	}

	if !completed {
		piApplyRetainedToolContent(fields, toolInput, toolResponse)
		return fields
	}

	switch action, _ := piToolAction(input); action {
	case "file.modified":
		path := piToolPath(toolInput)
		if path != "" {
			diffText := hookdiff.FromToolResponse(toolName, toolInput, toolResponse)
			if diffText == "" {
				diffText = firstToolString(toolInput, "content", "new_string", "newString")
			}
			mergeMap(fields, diffFields(path, diffText))
		}
	case "command.executed":
		fields["command"] = piCommandFields(input, toolInput, toolResponse)
	}

	piApplyRetainedToolContent(fields, toolInput, toolResponse)
	return fields
}

// piApplyRetainedToolContent records the retention marker for the raw tool input and output.
//
// Both are retained together under one marker because they are written to the event as one unit; a
// separate hash per half would describe content the event does not store separately.
func piApplyRetainedToolContent(fields, toolInput, toolResponse map[string]interface{}) {
	if len(toolInput) == 0 && len(toolResponse) == 0 {
		return
	}
	encoded, err := json.Marshal(map[string]interface{}{"input": toolInput, "response": toolResponse})
	if err != nil || len(encoded) == 0 {
		return
	}
	fields["content"] = retainedContentFields(string(encoded))
}

func piCommandFields(input, toolInput, toolResponse map[string]interface{}) map[string]interface{} {
	command := map[string]interface{}{}
	if value := firstToolString(toolInput, "command", "cmd", "script"); value != "" {
		command["command"] = value
	}
	if output, ok := toolResponse["output"]; ok {
		command["output"] = output
	}
	if exitCode, ok := firstToolIntAcross([]map[string]interface{}{toolResponse, input}, "exit_code", "exitCode", "exit", "status"); ok {
		command["exit_code"] = exitCode
	}
	if duration, ok := firstToolIntAcross([]map[string]interface{}{input}, "duration_ms", "durationMs"); ok {
		command["duration_ms"] = duration
	}
	return command
}

// piAssistantEvents records what an assistant message reported about the model turn.
//
// The token usage is the reason this event exists at all: Pi reports per-message usage including
// cache reads and writes and a runtime-computed cost, which is what feeds the token attribution
// rollups. Reasoning text, when present, is emitted as its own agent.reasoning event.
func piAssistantEvents(input map[string]interface{}, base map[string]interface{}) []normalizedEvent {
	fields := cloneEventFields(base)

	genAI := map[string]interface{}{}
	response := map[string]interface{}{}
	if finish := getFirstStr(input, "finish_reason", "finishReason", "stop_reason", "stopReason"); finish != "" {
		response["finish_reasons"] = []interface{}{finish}
	}
	if id := getFirstStr(input, "message_id", "messageId", "id"); id != "" {
		response["id"] = id
	}
	if len(response) > 0 {
		genAI["response"] = response
	}
	if usage := piUsage(input); len(usage) > 0 {
		genAI["usage"] = usage
	}
	if len(genAI) > 0 {
		fields["gen_ai"] = genAI
	}

	events := []normalizedEvent{{
		action: "agent.response.completed", category: "session", severity: "info",
		message: "Pi assistant response completed", fields: fields,
	}}

	reasoning := getFirstStr(input, "reasoning", "thinking", "thought")
	if reasoning == "" {
		return events
	}
	reasoningFields := cloneEventFields(base)
	reasoningFields["gen_ai"] = map[string]interface{}{
		"output": map[string]interface{}{"messages": reasoningOutputMessages(reasoning)},
	}
	reasoningFields["content"] = retainedContentFields(reasoning)
	return append(events, normalizedEvent{
		action: "agent.reasoning", category: "session", severity: "info",
		message: "Pi agent reasoning captured", fields: reasoningFields,
	})
}

// piUsage maps Pi's usage object onto gen_ai.usage.
//
// gen_ai.usage is the only token representation Beacon has, and its member names mirror the OTel
// GenAI semconv exactly, so this is a rename rather than a new shape. Pi's own field names are
// accepted in both casings because the extension forwards its usage object unchanged.
//
// Pi's totalTokens is deliberately dropped. The schema has no total member -- it is input plus
// output, derivable by any consumer -- and adding one would be the parallel token field the schema
// rules exist to prevent, with the added hazard that a stored total and a stored breakdown can
// disagree.
//
// Only cost.total maps to cost_usd, and only because Pi computes it: cost_usd carries
// runtime-reported cost and is never derived from a local pricing table. Pi's per-category costs
// have no schema member and are dropped rather than summed into something Pi did not report.
func piUsage(input map[string]interface{}) map[string]interface{} {
	usageInput := jsonMap(input, "usage")
	if len(usageInput) == 0 {
		return nil
	}
	usage := map[string]interface{}{}
	if value, ok := firstToolIntAcross([]map[string]interface{}{usageInput}, "input", "input_tokens", "inputTokens"); ok {
		usage["input_tokens"] = value
	}
	if value, ok := firstToolIntAcross([]map[string]interface{}{usageInput}, "output", "output_tokens", "outputTokens"); ok {
		usage["output_tokens"] = value
	}
	if value, ok := firstToolIntAcross([]map[string]interface{}{usageInput}, "cacheRead", "cache_read", "cacheReadTokens"); ok {
		usage["cache_read"] = map[string]interface{}{"input_tokens": value}
	}
	if value, ok := firstToolIntAcross([]map[string]interface{}{usageInput}, "cacheWrite", "cache_write", "cacheWriteTokens"); ok {
		usage["cache_creation"] = map[string]interface{}{"input_tokens": value}
	}
	if value, ok := firstToolIntAcross([]map[string]interface{}{usageInput}, "reasoning", "reasoning_tokens", "reasoningTokens"); ok {
		usage["reasoning"] = map[string]interface{}{"output_tokens": value}
	}
	if cost, ok := piCost(usageInput); ok {
		usage["cost_usd"] = cost
	}
	return usage
}

// piCost reads the runtime-reported total cost.
//
// Pi nests it as usage.cost.total. A bare usage.cost number is also accepted because that is the
// shape every other runtime Beacon reads uses, and a Pi version that flattens it should not
// silently stop reporting cost.
func piCost(usageInput map[string]interface{}) (float64, bool) {
	if cost := jsonMap(usageInput, "cost"); len(cost) > 0 {
		return jsonFloat(cost["total"])
	}
	return jsonFloat(usageInput["cost"])
}

func piToolName(input map[string]interface{}) string {
	return getFirstStr(input, "tool_name", "toolName", "tool")
}

// piToolAction classifies a completed tool by name, through the same shared classifier every other
// runtime uses. Pi has no fixed tool vocabulary -- extensions register their own tools -- so a
// per-runtime table would be wrong the moment somebody installs one, while the shared name-token
// rules degrade to tool.completed for anything unrecognized.
func piToolAction(input map[string]interface{}) (string, string) {
	name := piToolName(input)
	switch action := actionForTool("", name); action {
	case "mcp.tool_invoked":
		return action, "mcp"
	case "command.executed":
		return action, "command"
	case "file.read":
		return action, "file"
	case "file.modified":
		// A file-editing tool that never said which file it touched cannot be reported as a file
		// event: file.path is what a rule or a query matches on, and an empty one is worse than a
		// generic completion because it looks like a real file event with a missing field.
		if piToolPath(jsonMap(input, "tool_input", "toolInput", "input", "arguments")) == "" {
			return "tool.completed", "tool"
		}
		return action, "file"
	default:
		// actionForTool's fallback is tool.invoked, which is the pre-execution action. A result has
		// by definition already run.
		return "tool.completed", "tool"
	}
}

func piToolMessage(action string) string {
	switch action {
	case "command.executed":
		return "Pi shell command executed"
	case "file.read":
		return "Pi file read"
	case "file.modified":
		return "Pi file modified"
	case "mcp.tool_invoked":
		return "Pi MCP tool executed"
	default:
		return "Pi tool completed"
	}
}

func piToolPath(toolInput map[string]interface{}) string {
	return hookdiff.NormalizePath(firstToolString(toolInput, "file_path", "filePath", "path", "target", "destination"))
}

// piIsError reads Pi's isError flag, which is a boolean in the contract but is accepted as a string
// too: JSON written by hand in a test or by a future extension version should not silently mean
// "succeeded" because it says "true" rather than true.
func piIsError(input map[string]interface{}) bool {
	for _, key := range []string{"is_error", "isError", "error"} {
		switch typed := input[key].(type) {
		case bool:
			if typed {
				return true
			}
		case string:
			if strings.EqualFold(strings.TrimSpace(typed), "true") {
				return true
			}
		}
	}
	return false
}
