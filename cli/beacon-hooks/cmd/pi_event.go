package cmd

import (
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// pi-event is the single entry point for every Pi lifecycle payload.
//
// Pi delivers its events to an in-process extension whose handlers all fire in the same process, so
// one command receiving a typed envelope fits better than the command-per-hook shape used for
// runtimes that exec a separate hook per event. This mirrors how opencode and Cline are integrated.
var piEventCmd = &cobra.Command{
	Use:   "pi-event",
	Short: "Record Pi hook telemetry",
	Long:  `pi-event receives raw Beacon Pi extension payloads and writes local endpoint telemetry.`,
	Run:   runPiEvent,
}

func init() {
	rootCmd.AddCommand(piEventCmd)
}

func runPiEvent(cmd *cobra.Command, args []string) {
	input, err := readStdinJSON()
	if err != nil {
		outputJSON(emptyResponse)
		return
	}
	sessionID := resolveSessionID(input, "pi")
	logger := newHookLogger("pi-event", "pi", sessionID)
	for _, event := range piEndpointEvents(input, sessionID) {
		if event.action == "" {
			continue
		}
		_ = logger.EndpointEvent(event.action, event.category, event.severity, event.message, event.fields)
	}
	outputJSON(emptyResponse)
}

// supportedPiEventTypes lists every Pi event type this mapper handles.
//
// These strings are the contract between the managed extension's subscription list and this mapper.
// A typo on either side produces no telemetry rather than an error, so both sides pin the list and
// a test asserts each entry still maps to an event.
func supportedPiEventTypes() []string {
	return []string{
		"session_start",
		"session_shutdown",
		"input",
		"tool_call",
		"tool_result",
		"user_bash",
		"message_end",
	}
}

// piEndpointEvents maps one Pi payload onto the endpoint events it justifies.
//
// An unrecognized type returns nothing rather than a generic event, for the same reason the Cline
// mapper drops unknown stages: Pi publishes far more events than the extension subscribes to, and a
// future one arriving here should be silent rather than becoming an undifferentiated
// "something happened" row that every query matches and none can explain.
func piEndpointEvents(input map[string]interface{}, sessionID string) []normalizedEvent {
	fields := piBaseFields(input, sessionID)
	one := func(action, category, severity, message string, values map[string]interface{}) []normalizedEvent {
		return []normalizedEvent{{action: action, category: category, severity: severity, message: message, fields: values}}
	}

	switch getFirstStr(input, "type") {
	case "session_start":
		// Pi reports why the session started -- startup, reload, new, resume, fork -- and the
		// distinction matters for reading a log: a fork and a resume both produce a session id that
		// has history behind it, which a reader counting sessions needs to know.
		if reason := getFirstStr(input, "reason"); reason != "" {
			fields["raw"] = mergeNested(fields["raw"], map[string]interface{}{"pi_session_reason": reason})
		}
		return one("session.started", "session", "info", "Pi session started", fields)

	case "session_shutdown":
		if reason := getFirstStr(input, "reason"); reason != "" {
			fields["raw"] = mergeNested(fields["raw"], map[string]interface{}{"pi_shutdown_reason": reason})
		}
		return one("session.ended", "session", "info", "Pi session ended", fields)

	case "input":
		prompt := getFirstStr(input, "text")
		if prompt == "" {
			return nil
		}
		return []normalizedEvent{piPromptEvent(fields, prompt, getFirstStr(input, "source"))}

	case "tool_call":
		// The pre-execution half of a tool call: Pi has decided to run it and named its arguments,
		// but nothing has happened yet. Recorded as tool.invoked to match the Cline mapper's
		// tool_before stage, and deliberately not as an approval -- Pi's tool_call handler can
		// block, but that is an extension deciding rather than an operator being asked, so there is
		// no approval decision here to observe.
		mergeMap(fields, piToolFields(input, false))
		return one("tool.invoked", "tool", "info", "Pi tool invoked", fields)

	case "tool_result":
		return piToolResultEvents(input, fields)

	case "user_bash":
		// A command the human ran with Pi's `!` prefix rather than one the agent chose. No tool
		// event covers it, and it is the one command shape in Pi that the agent did not originate,
		// so it is recorded with the operator noted in raw rather than silently merged in with
		// agent-run commands.
		command := getFirstStr(input, "command")
		if command == "" {
			return nil
		}
		fields["command"] = map[string]interface{}{"command": command}
		fields["tool"] = map[string]interface{}{"name": "user_bash", "command": command}
		fields["content"] = retainedContentFields(command)
		fields["raw"] = mergeNested(fields["raw"], map[string]interface{}{
			"pi_user_initiated":       true,
			"pi_exclude_from_context": input["excludeFromContext"],
		})
		return one("command.executed", "command", "info", "Pi user command executed", fields)

	case "message_end":
		return piMessageEndEvents(input, fields)

	default:
		return nil
	}
}

func piBaseFields(input map[string]interface{}, sessionID string) map[string]interface{} {
	fields := sessionFieldsForPlatform(sessionID, input, "pi")
	applyWorkspaceFieldsForPlatform(fields, input, "", "pi")
	fields["raw"] = map[string]interface{}{"pi": input}
	if model := getFirstStr(input, "model"); model != "" {
		fields["model"] = model
	}
	return fields
}

func piPromptEvent(fields map[string]interface{}, prompt, source string) normalizedEvent {
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
	if source != "" {
		// Pi distinguishes interactive input from input delivered over its RPC surface or injected
		// by another extension. Retained because "a human typed this" and "a script sent this" are
		// different facts about the same prompt.
		fields["raw"] = mergeNested(fields["raw"], map[string]interface{}{"pi_input_source": source})
	}
	return normalizedEvent{
		action: "prompt.submitted", category: "prompt", severity: "info",
		message: "Prompt submitted to Pi", fields: fields,
	}
}

// piToolName reads the tool name off a tool_call or tool_result payload.
func piToolName(input map[string]interface{}) string {
	return getFirstStr(input, "toolName", "tool_name")
}

// piToolInput returns a tool call's arguments.
//
// tool_call carries them under `input`; tool_result carries the same arguments under `input` too,
// which is what lets one function serve both and a file path survive onto the result event.
func piToolInput(input map[string]interface{}) map[string]interface{} {
	if args := firstMap(input, "input", "args"); args != nil {
		return args
	}
	return map[string]interface{}{}
}

// piToolFields builds the tool, command, and file blocks for one Pi tool event.
//
// Pi's built-in tools have fixed, documented argument shapes -- bash takes `command`, and read,
// edit and write all take `path` -- so these are read by name rather than by guessing across
// spellings. A custom tool registered by another extension carries an arbitrary shape, and gets
// tool.name plus its raw arguments without a command or file block invented for it.
func piToolFields(input map[string]interface{}, withResult bool) map[string]interface{} {
	name := piToolName(input)
	args := piToolInput(input)
	fields := map[string]interface{}{}
	tool := map[string]interface{}{}
	if name != "" {
		tool["name"] = name
	}

	switch strings.ToLower(name) {
	case "bash":
		if command := getFirstStr(args, "command"); command != "" {
			tool["command"] = command
			fields["command"] = map[string]interface{}{"command": command}
			fields["content"] = retainedContentFields(command)
		}
	case "read", "edit", "write":
		if path := getFirstStr(args, "path"); path != "" {
			tool["path"] = path
			file := map[string]interface{}{
				"path":      path,
				"operation": piFileOperation(name),
			}
			file["language"] = strings.TrimPrefix(filepath.Ext(path), ".")
			fields["file"] = file
		}
	}

	if len(tool) > 0 {
		fields["tool"] = tool
	}
	if withResult {
		if usage := piUsage(firstMap(input, "usage")); len(usage) > 0 {
			fields["gen_ai"] = mergeNested(fields["gen_ai"], map[string]interface{}{"usage": usage})
		}
	}
	return fields
}

// piFileOperation maps a Pi file tool onto the operation vocabulary the event schema uses.
func piFileOperation(name string) string {
	switch strings.ToLower(name) {
	case "read":
		return "read"
	case "write":
		return "create"
	default:
		return "modify"
	}
}

// piToolResultEvents maps a completed tool call onto its outcome event.
func piToolResultEvents(input map[string]interface{}, fields map[string]interface{}) []normalizedEvent {
	mergeMap(fields, piToolFields(input, true))
	name := piToolName(input)

	if isErr, ok := input["isError"].(bool); ok && isErr {
		fields["error"] = map[string]interface{}{"type": "tool_error"}
		return []normalizedEvent{{
			action: "tool.failed", category: "tool", severity: "high",
			message: "Pi tool failed", fields: fields,
		}}
	}

	if diff := piEditDiff(input); diff != "" {
		fields["content"] = retainedContentFields(diff)
	}

	action, category := piToolAction(name)
	// A file action with no file is not a file action. Pi's read tool accepts a path that failed to
	// resolve, and a custom tool can share a built-in's name, so reporting file.read with no file
	// field would produce a row every file-scoped query matches and none can explain -- the same
	// guard clineToolAfterEvents applies for the same reason.
	if strings.HasPrefix(action, "file.") {
		if _, ok := fields["file"]; !ok {
			action, category = "tool.completed", "tool"
		}
	}
	if action == "command.executed" {
		if _, ok := fields["command"]; !ok {
			action, category = "tool.completed", "tool"
		}
	}
	return []normalizedEvent{{
		action: action, category: category, severity: "info",
		message: piToolMessage(action), fields: fields,
	}}
}

// piEditDiff returns the unified patch Pi's edit tool reports, when it reported one.
//
// Pi's EditToolDetails carries both a display-oriented `diff` and a standard unified `patch`. The
// patch is preferred because it is the machine-readable one; the diff is a fallback for a details
// object that carried only the display form.
func piEditDiff(input map[string]interface{}) string {
	details := firstMap(input, "details")
	if details == nil {
		return ""
	}
	return getFirstStr(details, "patch", "diff")
}

// piToolAction maps a Pi tool name onto the endpoint action its completion represents.
func piToolAction(name string) (string, string) {
	switch strings.ToLower(name) {
	case "bash":
		return "command.executed", "command"
	case "read":
		return "file.read", "file"
	case "edit":
		return "file.modified", "file"
	case "write":
		return "file.created", "file"
	default:
		// grep, find, ls, and any tool another extension registered. These are real tool activity
		// with no file or command semantics worth asserting: grep takes a pattern, not a path, and
		// a custom tool's arguments mean whatever its author decided.
		return "tool.completed", "tool"
	}
}

func piToolMessage(action string) string {
	switch action {
	case "command.executed":
		return "Pi command executed"
	case "file.read":
		return "Pi file read"
	case "file.created":
		return "Pi file created"
	case "file.modified":
		return "Pi file modified"
	case "tool.failed":
		return "Pi tool failed"
	default:
		return "Pi tool completed"
	}
}

// piMessageEndEvents records what a finished assistant message tells us: its token usage, and the
// model's reasoning when the provider returned any.
//
// A finalized message is the only place Pi reports usage, and message_end fires for user and
// toolResult messages too, so a message with neither usage nor reasoning produces nothing rather
// than an empty row per turn.
func piMessageEndEvents(input map[string]interface{}, fields map[string]interface{}) []normalizedEvent {
	message := firstMap(input, "message")
	if message == nil {
		return nil
	}
	if role := getFirstStr(message, "role"); role != "assistant" {
		return nil
	}
	if model := getFirstStr(message, "model", "responseModel"); model != "" {
		fields["model"] = model
	}

	var events []normalizedEvent

	if reasoning := piReasoningText(message); reasoning != "" {
		reasoningFields := cloneFields(fields)
		reasoningFields["gen_ai"] = mergeNested(reasoningFields["gen_ai"], map[string]interface{}{
			"output": map[string]interface{}{
				"messages": []interface{}{map[string]interface{}{
					"role":  "assistant",
					"parts": []interface{}{map[string]interface{}{"type": "reasoning", "content": reasoning}},
				}},
			},
		})
		reasoningFields["content"] = retainedContentFields(reasoning)
		events = append(events, normalizedEvent{
			action: "agent.reasoning", category: "reasoning", severity: "info",
			message: "Pi agent reasoning", fields: reasoningFields,
		})
	}

	if usage := piUsage(firstMap(message, "usage")); len(usage) > 0 {
		usageFields := cloneFields(fields)
		usageFields["gen_ai"] = mergeNested(usageFields["gen_ai"], map[string]interface{}{"usage": usage})
		events = append(events, normalizedEvent{
			action: "token.usage", category: "metric", severity: "info",
			message: "Pi token usage", fields: usageFields,
		})
	}

	return events
}

// piReasoningText concatenates the thinking parts of an assistant message.
//
// Pi's assistant content is a list of parts, and a reasoning model emits thinking alongside text in
// the same message. Only the thinking parts are collected here: the assistant's visible answer is
// not reasoning, and recording it as such would put the model's output where a reader looking for
// its private deliberation expects to find it.
func piReasoningText(message map[string]interface{}) string {
	content, ok := message["content"].([]interface{})
	if !ok {
		return ""
	}
	var parts []string
	for _, item := range content {
		part, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if getFirstStr(part, "type") != "thinking" {
			continue
		}
		if text := getFirstStr(part, "thinking", "text", "content"); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

// piUsage normalizes Pi's Usage object into gen_ai.usage.
//
// Pi names its fields input/output/cacheRead/cacheWrite/reasoning and nests cost under `cost`, none
// of which match the OTel GenAI semconv names Beacon writes, and two of which are nested objects on
// Beacon's side rather than scalars. The mapping is spelled out against the canonical
// GenAIUsageInfo shape rather than copied through, so gen_ai.usage stays the only token
// representation in the log and no parallel per-harness field appears beside it.
//
// Pi's `output` already includes its `reasoning` tokens, so reasoning is recorded under its own key
// but never added to anything: treating it as a separate bucket would double-count it in any total.
// Pi also reports a `totalTokens`, which is deliberately dropped -- Beacon's usage shape has no
// total, and a redundant field that can disagree with its own parts is worse than an absent one.
func piUsage(usage map[string]interface{}) map[string]interface{} {
	if usage == nil {
		return nil
	}
	sources := []map[string]interface{}{usage}
	out := map[string]interface{}{}
	if value, ok := firstToolIntAcross(sources, "input"); ok {
		out["input_tokens"] = value
	}
	if value, ok := firstToolIntAcross(sources, "output"); ok {
		out["output_tokens"] = value
	}
	if value, ok := firstToolIntAcross(sources, "cacheRead"); ok {
		out["cache_read"] = map[string]interface{}{"input_tokens": value}
	}
	if value, ok := firstToolIntAcross(sources, "cacheWrite"); ok {
		out["cache_creation"] = map[string]interface{}{"input_tokens": value}
	}
	if value, ok := firstToolIntAcross(sources, "reasoning"); ok {
		out["reasoning"] = map[string]interface{}{"output_tokens": value}
	}
	// Runtime-reported cost only. Beacon never derives cost from a local pricing table, so a Pi
	// build or provider that reports no cost leaves the field absent rather than estimated.
	if cost := firstMap(usage, "cost"); cost != nil {
		if value, ok := jsonFloat(cost["total"]); ok {
			out["cost_usd"] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
