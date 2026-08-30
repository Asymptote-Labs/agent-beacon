package cmd

import (
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// Two products reach Beacon through the Pi coding agent's extension API.
//
// Pi (pi.dev) is the original. Prime Agent (Prime Intellect) ships the same package with a
// rebranded config directory, so the envelope Beacon receives -- the event `type`, the lifted
// session identity, the reason strings, the assistant message shape, the token usage object -- is
// the same for both. What differs is the tool surface: Pi's agent calls read, edit, write and bash,
// while Prime Agent's calls one `ipython` tool and does everything else from inside a persistent
// Python kernel.
//
// So the envelope is mapped once here and the tool surface is mapped per runtime. The alternative
// was a second copy of this file with "Pi" replaced by "Prime Agent", which is how the two mappers
// would come to disagree about what a session start means the first time either is fixed.
type piFamilyMapping struct {
	// platform is the --platform value the hook was invoked with. It is also the key the raw
	// payload is filed under and the prefix on every promoted raw field, so one runtime's rows are
	// never confused with another's in a log that carries both.
	platform string
	// displayName is what event messages call this runtime, in the words its own users use.
	displayName string
}

// piFamilyEventRunner builds the cobra handler for one runtime's event command.
//
// The handler is shared because the work is: read one JSON envelope from stdin, map it to zero or
// more endpoint events, write them, and answer with an empty response so the extension's spawn
// always sees a clean exit. Only the mapper differs.
func piFamilyEventRunner(mapping piFamilyMapping, events func(map[string]interface{}, string) []normalizedEvent) func(*cobra.Command, []string) {
	return func(cmd *cobra.Command, args []string) {
		input, err := readStdinJSON()
		if err != nil {
			outputJSON(emptyResponse)
			return
		}
		sessionID := resolveSessionID(input, mapping.platform)
		logger := newHookLogger(mapping.platform+"-event", mapping.platform, sessionID)
		for _, event := range events(input, sessionID) {
			if event.action == "" {
				continue
			}
			// Both runtimes name every tool invocation with a `toolCallId` on the envelope, and
			// that name is what links the pre-execution row to the result row for the same call.
			// These mappers write through the logger directly rather than through emitHookEvent,
			// so the promotion emitHookEvent performs has to happen here or the join key is lost
			// on the one capture path that always has it.
			applyToolCallID(event.fields, input)
			_ = logger.EndpointEvent(event.action, event.category, event.severity, event.message, event.fields)
		}
		outputJSON(emptyResponse)
	}
}

// piFamilyRawKey namespaces a promoted raw field by runtime, so `pi_session_reason` and
// `prime_session_reason` stay distinguishable in a log that carries both.
func (m piFamilyMapping) rawKey(suffix string) string {
	return m.platform + "_" + suffix
}

func (m piFamilyMapping) message(rest string) string {
	return m.displayName + " " + rest
}

func (m piFamilyMapping) baseFields(input map[string]interface{}, sessionID string) map[string]interface{} {
	fields := sessionFieldsForPlatform(sessionID, input, m.platform)
	applyWorkspaceFieldsForPlatform(fields, input, "", m.platform)
	fields["raw"] = map[string]interface{}{m.platform: input}
	if model := getFirstStr(input, "model"); model != "" {
		fields["model"] = model
	}
	return fields
}

// one is the common case: a payload that justifies exactly one event.
func piFamilyOneEvent(action, category, severity, message string, fields map[string]interface{}) []normalizedEvent {
	return []normalizedEvent{{action: action, category: category, severity: severity, message: message, fields: fields}}
}

// piFamilySessionStartEvents records the start of a session and why it started.
//
// Both runtimes report a reason -- startup, reload, new, resume, fork -- and the distinction matters
// for reading a log: a fork and a resume both produce a session id that has history behind it,
// which a reader counting sessions needs to know.
func (m piFamilyMapping) sessionStartEvents(input map[string]interface{}, fields map[string]interface{}) []normalizedEvent {
	if reason := getFirstStr(input, "reason"); reason != "" {
		fields["raw"] = mergeNested(fields["raw"], map[string]interface{}{m.rawKey("session_reason"): reason})
	}
	return piFamilyOneEvent("session.started", "session", "info", m.message("session started"), fields)
}

func (m piFamilyMapping) sessionShutdownEvents(input map[string]interface{}, fields map[string]interface{}) []normalizedEvent {
	if reason := getFirstStr(input, "reason"); reason != "" {
		fields["raw"] = mergeNested(fields["raw"], map[string]interface{}{m.rawKey("shutdown_reason"): reason})
	}
	return piFamilyOneEvent("session.ended", "session", "info", m.message("session ended"), fields)
}

func (m piFamilyMapping) inputEvents(input map[string]interface{}, fields map[string]interface{}) []normalizedEvent {
	prompt := getFirstStr(input, "text")
	if prompt == "" {
		return nil
	}
	fields["prompt"] = map[string]interface{}{"text": prompt}
	fields["gen_ai"] = mergeNested(fields["gen_ai"], map[string]interface{}{
		"input": map[string]interface{}{"messages": asymptoteobserve.TextInputMessages(prompt)},
	})
	fields["content"] = retainedContentFields(prompt)
	if source := getFirstStr(input, "source"); source != "" {
		// Both runtimes distinguish interactive input from input delivered over their RPC surface
		// or injected by another extension. Retained because "a human typed this" and "a script
		// sent this" are different facts about the same prompt.
		fields["raw"] = mergeNested(fields["raw"], map[string]interface{}{m.rawKey("input_source"): source})
	}
	return piFamilyOneEvent("prompt.submitted", "prompt", "info", "Prompt submitted to "+m.displayName, fields)
}

// userBashEvents records a command the human ran with the `!` prefix rather than one the agent
// chose. No tool event covers it, and it is the one command shape in these runtimes that the agent
// did not originate, so it is recorded with the operator noted in raw rather than silently merged
// in with agent-run commands.
func (m piFamilyMapping) userBashEvents(input map[string]interface{}, fields map[string]interface{}) []normalizedEvent {
	command := getFirstStr(input, "command")
	if command == "" {
		return nil
	}
	fields["command"] = map[string]interface{}{"command": command}
	fields["tool"] = map[string]interface{}{"name": "user_bash", "command": command}
	fields["content"] = retainedContentFields(command)
	fields["raw"] = mergeNested(fields["raw"], map[string]interface{}{
		m.rawKey("user_initiated"):       true,
		m.rawKey("exclude_from_context"): input["excludeFromContext"],
	})
	return piFamilyOneEvent("command.executed", "command", "info", m.message("user command executed"), fields)
}

// messageEndEvents records what a finished assistant message tells us: its token usage, and the
// model's reasoning when the provider returned any.
//
// A finalized message is the only place either runtime reports usage, and message_end fires for
// user and toolResult messages too, so a message with neither usage nor reasoning produces nothing
// rather than an empty row per turn.
func (m piFamilyMapping) messageEndEvents(input map[string]interface{}, fields map[string]interface{}) []normalizedEvent {
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

	if reasoning := piFamilyReasoningText(message); reasoning != "" {
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
			message: m.message("agent reasoning"), fields: reasoningFields,
		})
	}

	if usage := piFamilyUsage(firstMap(message, "usage")); len(usage) > 0 {
		usageFields := cloneFields(fields)
		usageFields["gen_ai"] = mergeNested(usageFields["gen_ai"], map[string]interface{}{"usage": usage})
		events = append(events, normalizedEvent{
			action: "token.usage", category: "metric", severity: "info",
			message: m.message("token usage"), fields: usageFields,
		})
	}

	return events
}

// piFamilyReasoningText concatenates the thinking parts of an assistant message.
//
// Assistant content is a list of parts, and a reasoning model emits thinking alongside text in the
// same message. Only the thinking parts are collected here: the assistant's visible answer is not
// reasoning, and recording it as such would put the model's output where a reader looking for its
// private deliberation expects to find it.
func piFamilyReasoningText(message map[string]interface{}) string {
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

// piFamilyUsage normalizes the runtime's Usage object into gen_ai.usage.
//
// Both runtimes name their fields input/output/cacheRead/cacheWrite/reasoning and nest cost under
// `cost`, none of which match the OTel GenAI semconv names Beacon writes, and two of which are
// nested objects on Beacon's side rather than scalars. The mapping is spelled out against the
// canonical GenAIUsageInfo shape rather than copied through, so gen_ai.usage stays the only token
// representation in the log and no parallel per-harness field appears beside it.
//
// `output` already includes `reasoning` tokens, so reasoning is recorded under its own key but
// never added to anything: treating it as a separate bucket would double-count it in any total. The
// reported `totalTokens` is deliberately dropped -- Beacon's usage shape has no total, and a
// redundant field that can disagree with its own parts is worse than an absent one.
func piFamilyUsage(usage map[string]interface{}) map[string]interface{} {
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
	// Runtime-reported cost only. Beacon never derives cost from a local pricing table, so a build
	// or provider that reports no cost leaves the field absent rather than estimated.
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

// piFamilyToolName reads the tool name off a tool_call or tool_result payload.
func piFamilyToolName(input map[string]interface{}) string {
	return getFirstStr(input, "toolName", "tool_name")
}

// piFamilyToolInput returns a tool call's arguments.
//
// tool_call carries them under `input`; tool_result carries the same arguments under `input` too,
// which is what lets one function serve both and a file path survive onto the result event.
func piFamilyToolInput(input map[string]interface{}) map[string]interface{} {
	if args := firstMap(input, "input", "args"); args != nil {
		return args
	}
	return map[string]interface{}{}
}

// piFamilyBuiltinToolFields builds the tool, command and file blocks for the file and shell tools
// both runtimes' packages define.
//
// These tools have fixed, documented argument shapes -- bash takes `command`, and read, edit and
// write all take `path` -- so they are read by name rather than by guessing across spellings. A
// tool this does not recognize returns nothing here and gets tool.name plus its raw arguments from
// the caller, without a command or file block invented for it.
func piFamilyBuiltinToolFields(name string, args map[string]interface{}) map[string]interface{} {
	fields := map[string]interface{}{}
	tool := map[string]interface{}{}
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
			fields["file"] = map[string]interface{}{
				"path":      path,
				"operation": piFamilyFileOperation(name),
				"language":  strings.TrimPrefix(filepath.Ext(path), "."),
			}
		}
	}
	if len(tool) > 0 {
		fields["tool"] = tool
	}
	return fields
}

// piFamilyFileOperation maps a file tool onto the operation vocabulary the event schema uses.
func piFamilyFileOperation(name string) string {
	switch strings.ToLower(name) {
	case "read":
		return "read"
	case "write":
		return "create"
	default:
		return "modify"
	}
}

// piFamilyToolMessage names a completed tool action in the runtime's own words.
func (m piFamilyMapping) toolMessage(action string) string {
	switch action {
	case "command.executed":
		return m.message("command executed")
	case "file.read":
		return m.message("file read")
	case "file.created":
		return m.message("file created")
	case "file.modified":
		return m.message("file modified")
	case "tool.failed":
		return m.message("tool failed")
	default:
		return m.message("tool completed")
	}
}

// piFamilyDowngradeUnsupportedAction rewrites a file or command action that has no field to stand
// on.
//
// A file action with no file is not a file action: a read tool accepts a path that failed to
// resolve, and a custom tool can share a built-in's name, so reporting file.read with no file field
// would produce a row every file-scoped query matches and none can explain -- the same guard the
// Cline mapper applies for the same reason.
func piFamilyDowngradeUnsupportedAction(action, category string, fields map[string]interface{}) (string, string) {
	if strings.HasPrefix(action, "file.") {
		if _, ok := fields["file"]; !ok {
			return "tool.completed", "tool"
		}
	}
	if action == "command.executed" {
		if _, ok := fields["command"]; !ok {
			return "tool.completed", "tool"
		}
	}
	return action, category
}
