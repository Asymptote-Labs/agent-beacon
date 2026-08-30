package cmd

import (
	"path/filepath"
	"strings"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// The Pi family is Pi (pi.dev) and the forks that kept its extension API.
//
// Oh My Pi is a fork of pi-mono, and its extension event payloads are structurally the same as
// Pi's: the same `type` discriminator, the same `toolName`/`input`/`details` shape on tool events,
// the same assistant message parts, and the same input/output/cacheRead/cacheWrite/cost usage
// object. Mapping them twice would mean two copies of that knowledge drifting apart -- and the
// drift would be silent, because a mapper that stops recognizing a field emits an event missing a
// column rather than an error.
//
// So the shape lives here once, and each runtime supplies only what actually differs: which
// `--platform` it was installed as, and what to call it in an event message. What must NOT be
// shared is the identity: the two are separately installed products, they get separate harness
// names (see asymptoteobserve.NormalizeHarnessName), and every event carries its own runtime's
// name in `raw` so an operator reading a row can tell which binary produced it.
//
// Events one runtime has and the other does not are mapped here too, and gated by the subscription
// list rather than by a branch: Oh My Pi's extension subscribes to its approval and user_python
// events, Pi's does not, so those cases are simply never reached for Pi. That is a stronger
// guarantee than a platform check would be -- Beacon cannot synthesize a Pi approval because Pi
// never sends one, not because a condition remembered to say so. Inventing one to keep the two
// runtimes symmetric would put a decision nobody made into the log.
type piFamily struct {
	// platform is the `--platform` value the runtime's hook is installed with. It selects the
	// session-id and working-directory readers in helpers.go, keys this runtime's block inside
	// `raw`, and prefixes the runtime-specific keys nested under it.
	platform string
	// displayName is how the runtime is named in an event's human-readable message.
	displayName string
}

var (
	piRuntime  = piFamily{platform: "pi", displayName: "Pi"}
	ompRuntime = piFamily{platform: "omp", displayName: "Oh My Pi"}
)

// rawKey namespaces a runtime-specific detail inside the `raw` block.
//
// These keys sit beside the verbatim payload rather than being promoted to schema fields, because
// they describe how one runtime happened to phrase something rather than a fact the endpoint event
// schema defines. Prefixing them with the platform keeps a Pi row and an Oh My Pi row from
// colliding in a store that flattens `raw`.
func (f piFamily) rawKey(suffix string) string {
	return f.platform + "_" + suffix
}

// endpointEvents maps one Pi-family payload onto the endpoint events it justifies.
//
// An unrecognized type returns nothing rather than a generic event, for the same reason the Cline
// mapper drops unknown stages: these runtimes publish far more events than the extension
// subscribes to, and a future one arriving here should be silent rather than becoming an
// undifferentiated "something happened" row that every query matches and none can explain.
//
// Both halves of a tool call carry the runtime's own `toolCallId`, and both promote it to
// `gen_ai.tool.call.id` through the shared alias list in asymptoteobserve.ToolCallIDKeys. That
// field is the only thing linking tool.invoked to the tool.completed, file.modified or
// command.executed it turned into -- and, on Oh My Pi, an approval decision to the execution it
// approved. Without it those rows sit in the log as unrelated events that merely happen to share a
// session id and a nearby timestamp.
func (f piFamily) endpointEvents(input map[string]interface{}, sessionID string) []normalizedEvent {
	fields := f.baseFields(input, sessionID)

	switch getFirstStr(input, "type") {
	case "session_start":
		// The runtime reports why the session started -- startup, reload, new, resume, fork -- and
		// the distinction matters for reading a log: a fork and a resume both produce a session id
		// that has history behind it, which a reader counting sessions needs to know.
		if reason := getFirstStr(input, "reason"); reason != "" {
			fields["raw"] = mergeNested(fields["raw"], map[string]interface{}{f.rawKey("session_reason"): reason})
		}
		return f.one("session.started", "session", "info", "session started", fields)

	case "session_shutdown":
		if reason := getFirstStr(input, "reason"); reason != "" {
			fields["raw"] = mergeNested(fields["raw"], map[string]interface{}{f.rawKey("shutdown_reason"): reason})
		}
		return f.one("session.ended", "session", "info", "session ended", fields)

	case "input":
		prompt := getFirstStr(input, "text")
		if prompt == "" {
			return nil
		}
		return []normalizedEvent{f.promptEvent(fields, prompt, getFirstStr(input, "source"))}

	case "tool_call":
		// The pre-execution half of a tool call: the runtime has decided to run it and named its
		// arguments, but nothing has happened yet. Recorded as tool.invoked to match the Cline
		// mapper's tool_before stage, and deliberately not as an approval -- a tool_call handler
		// can block, but that is an extension deciding rather than an operator being asked. Oh My
		// Pi's real approval decisions arrive as their own events and are mapped there.
		mergeMap(fields, f.toolFields(input, false))
		applyToolCallID(fields, input)
		return f.one("tool.invoked", "tool", "info", "tool invoked", fields)

	case "tool_result":
		return f.toolResultEvents(input, fields)

	case "user_bash":
		// A command the human ran with the `!` prefix rather than one the agent chose. No tool
		// event covers it, and it is the one command shape here that the agent did not originate,
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
			f.rawKey("user_initiated"):       true,
			f.rawKey("exclude_from_context"): input["excludeFromContext"],
		})
		return f.one("command.executed", "command", "info", "user command executed", fields)

	case "user_python":
		// The `$` prefix runs Python in the runtime's own REPL rather than a shell. It is the
		// operator's code, not the agent's, and it is executed just as literally as a bash command
		// -- `os.system("rm -rf /")` is a shell command wearing a Python hat -- so it is recorded
		// in the command category where the risky-command rules can see it, marked as the
		// operator's and as Python rather than being passed off as a shell command.
		code := getFirstStr(input, "code")
		if code == "" {
			return nil
		}
		fields["command"] = map[string]interface{}{"command": code}
		fields["tool"] = map[string]interface{}{"name": "user_python", "command": code}
		fields["content"] = retainedContentFields(code)
		fields["raw"] = mergeNested(fields["raw"], map[string]interface{}{
			f.rawKey("user_initiated"):       true,
			f.rawKey("user_python"):          true,
			f.rawKey("exclude_from_context"): input["excludeFromContext"],
		})
		return f.one("command.executed", "command", "info", "user Python executed", fields)

	case "tool_approval_requested":
		return f.approvalEvents(input, fields, "approval.requested", "requested", "approval requested")

	case "tool_approval_resolved":
		// The runtime states the outcome as a boolean rather than as a word, so there is no
		// unknown case to fall back to: an event that reached here had a decision made on it.
		if approved, _ := input["approved"].(bool); approved {
			return f.approvalEvents(input, fields, "approval.allowed", "approve", "approval allowed")
		}
		return f.approvalEvents(input, fields, "approval.denied", "deny", "approval denied")

	case "message_end":
		return f.messageEndEvents(input, fields)

	default:
		return nil
	}
}

// approvalEvents records an operator approval decision the runtime actually asked for.
//
// This is the one thing Oh My Pi exposes that Pi does not. It matters because Beacon has always
// refused to synthesize an approval from a tool call: a `tool_call` handler that blocks is an
// extension deciding, and recording that as an approval would be indistinguishable from a decision
// a human made. Here the runtime reports a real prompt and a real answer, so the event is
// `observed` rather than `inferred` and carries the operator's decision verbatim.
//
// The approval carries the tool's name and the runtime's `toolCallId` but not its arguments -- the
// runtime does not put them on these events. The call id is the join back to the tool.invoked that
// does carry them, which is why it is promoted here as carefully as on the tool events themselves.
func (f piFamily) approvalEvents(input, fields map[string]interface{}, action, decision, messageSuffix string) []normalizedEvent {
	toolName := piToolName(input)
	if toolName != "" {
		fields["tool"] = mergeNested(fields["tool"], map[string]interface{}{"name": toolName})
		f.applyMCPAttribution(fields, toolName)
	}

	approval := map[string]interface{}{"required": true, "decision": decision}
	// The runtime's own words for why, when it gave any. Left absent rather than filled with a
	// Beacon-authored sentence, so a reader can tell an operator's reason from a default.
	if reason := getFirstStr(input, "reason"); reason != "" {
		approval["reason"] = reason
	}
	fields["approval"] = approval

	// Which approval policy the session was running under. "yolo" means the operator turned the
	// prompts off, which is the single most load-bearing fact about any approval row: it is the
	// difference between a decision someone made and a decision nobody was asked to make.
	if mode := getFirstStr(input, "approvalMode"); mode != "" {
		fields["raw"] = mergeNested(fields["raw"], map[string]interface{}{f.rawKey("approval_mode"): mode})
	}

	applyToolCallID(fields, input)
	return f.one(action, "approval", "info", messageSuffix, fields)
}

// one wraps a single event, prefixing the runtime's display name onto the message.
//
// Messages are built from a suffix rather than spelled out per runtime so that "Pi tool failed"
// and "Oh My Pi tool failed" cannot drift into describing the same thing two different ways.
func (f piFamily) one(action, category, severity, messageSuffix string, values map[string]interface{}) []normalizedEvent {
	return []normalizedEvent{{
		action:   action,
		category: category,
		severity: severity,
		message:  f.displayName + " " + messageSuffix,
		fields:   values,
	}}
}

func (f piFamily) baseFields(input map[string]interface{}, sessionID string) map[string]interface{} {
	fields := sessionFieldsForPlatform(sessionID, input, f.platform)
	applyWorkspaceFieldsForPlatform(fields, input, "", f.platform)
	fields["raw"] = map[string]interface{}{f.platform: input}
	if model := getFirstStr(input, "model"); model != "" {
		fields["model"] = model
	}
	return fields
}

func (f piFamily) promptEvent(fields map[string]interface{}, prompt, source string) normalizedEvent {
	fields["prompt"] = map[string]interface{}{"text": prompt}
	fields["gen_ai"] = mergeNested(fields["gen_ai"], map[string]interface{}{
		"input": map[string]interface{}{"messages": asymptoteobserve.TextInputMessages(prompt)},
	})
	fields["content"] = retainedContentFields(prompt)
	if source != "" {
		// These runtimes distinguish interactive input from input delivered over their RPC surface
		// or injected by another extension. Retained because "a human typed this" and "a script
		// sent this" are different facts about the same prompt.
		fields["raw"] = mergeNested(fields["raw"], map[string]interface{}{f.rawKey("input_source"): source})
	}
	return normalizedEvent{
		action: "prompt.submitted", category: "prompt", severity: "info",
		message: "Prompt submitted to " + f.displayName, fields: fields,
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

// toolFields builds the tool, command, and file blocks for one Pi-family tool event.
//
// The built-in tools have fixed, documented argument shapes -- bash takes `command`, and read,
// edit and write all take `path` -- so these are read by name rather than by guessing across
// spellings. A custom tool registered by another extension carries an arbitrary shape, and gets
// tool.name plus its raw arguments without a command or file block invented for it.
func (f piFamily) toolFields(input map[string]interface{}, withResult bool) map[string]interface{} {
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
	f.applyMCPAttribution(fields, name)
	if withResult {
		if usage := piUsage(firstMap(input, "usage")); len(usage) > 0 {
			fields["gen_ai"] = mergeNested(fields["gen_ai"], map[string]interface{}{"usage": usage})
		}
	}
	return fields
}

// piFileOperation maps a Pi-family file tool onto the operation vocabulary the event schema uses.
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

// applyMCPAttribution fills the `mcp` block when a tool name names an MCP-routed tool.
//
// Without it an MCP call lands in the log as a tool named `mcp__github_create_issue` and nothing
// else -- no server, no tool -- so the two questions actually asked about MCP activity ("which
// server did this agent reach, and what did it call there") have no field to answer them.
func (f piFamily) applyMCPAttribution(fields map[string]interface{}, toolName string) {
	server, tool := piMCPServerTool(toolName)
	if server == "" && tool == "" {
		return
	}
	mcp := map[string]interface{}{}
	if server != "" {
		mcp["server"] = server
	}
	if tool != "" {
		mcp["tool"] = tool
	}
	fields["mcp"] = mergeNested(fields["mcp"], mcp)
	fields["gen_ai"] = mergeNested(fields["gen_ai"], map[string]interface{}{
		"operation": map[string]interface{}{"name": "execute_tool"},
	})
}

// piMCPServerTool splits a Pi-family MCP tool name into its server and tool halves.
//
// Two spellings reach here and they disagree about the separator. `mcp__<server>__<tool>` is the
// widely used double-underscore form, and deriveMCPServerTool already reads it; Oh My Pi mints
// `mcp__<server>_<tool>` with a single underscore (createMCPToolName in its mcp/tool-bridge.ts),
// which that function returns nothing for because it needs three `__`-separated parts.
//
// The double-underscore form is tried first because it is unambiguous. The single-underscore
// fallback splits on the first underscore, which is exactly what Oh My Pi's own parseMCPToolName
// does -- including its ambiguity, since a server named `my_server` yields `mcp__my_server_run` and
// both parsers read that as server `my`. Reproducing the runtime's reading rather than inventing a
// better one is deliberate: Beacon's `mcp.server` should say what the runtime itself would say, so
// an operator comparing the two never finds them disagreeing.
func piMCPServerTool(toolName string) (string, string) {
	if server, tool := deriveMCPServerTool(toolName); server != "" || tool != "" {
		return server, tool
	}
	rest, ok := strings.CutPrefix(strings.TrimSpace(toolName), "mcp__")
	if !ok {
		return "", ""
	}
	server, tool, ok := strings.Cut(rest, "_")
	if !ok || server == "" || tool == "" {
		return "", ""
	}
	return server, tool
}

// toolResultEvents maps a completed tool call onto its outcome event.
func (f piFamily) toolResultEvents(input map[string]interface{}, fields map[string]interface{}) []normalizedEvent {
	mergeMap(fields, f.toolFields(input, true))
	applyToolCallID(fields, input)
	name := piToolName(input)

	if isErr, ok := input["isError"].(bool); ok && isErr {
		fields["error"] = map[string]interface{}{"type": "tool_error"}
		return f.one("tool.failed", "tool", "high", "tool failed", fields)
	}

	if diff := piEditDiff(input); diff != "" {
		fields["content"] = retainedContentFields(diff)
	}

	action, category := piToolAction(name)
	// A file action with no file is not a file action. The read tool accepts a path that failed to
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
	return f.one(action, category, "info", piToolMessageSuffix(action), fields)
}

// piEditDiff returns the unified patch the edit tool reports, when it reported one.
//
// EditToolDetails carries both a display-oriented `diff` and a standard unified `patch`. The patch
// is preferred because it is the machine-readable one; the diff is a fallback for a details object
// that carried only the display form.
func piEditDiff(input map[string]interface{}) string {
	details := firstMap(input, "details")
	if details == nil {
		return ""
	}
	return getFirstStr(details, "patch", "diff")
}

// piToolAction maps a Pi-family tool name onto the endpoint action its completion represents.
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
		// An MCP-routed tool is real, attributable activity rather than an anonymous custom tool,
		// and mcp.tool_invoked is the action every other Beacon capture path already uses for it --
		// so an MCP call through Oh My Pi joins the same rows a detection reads for Cline, Cursor
		// and Claude Code rather than hiding under tool.completed.
		if server, tool := piMCPServerTool(name); server != "" || tool != "" {
			return "mcp.tool_invoked", "mcp"
		}
		// grep, glob, and any tool another extension registered. These are real tool activity with
		// no file or command semantics worth asserting: grep takes a pattern, not a path, and a
		// custom tool's arguments mean whatever its author decided.
		return "tool.completed", "tool"
	}
}

// piToolMessageSuffix returns the runtime-independent half of a tool event's message.
func piToolMessageSuffix(action string) string {
	switch action {
	case "command.executed":
		return "command executed"
	case "file.read":
		return "file read"
	case "file.created":
		return "file created"
	case "file.modified":
		return "file modified"
	case "tool.failed":
		return "tool failed"
	case "mcp.tool_invoked":
		return "MCP tool invoked"
	default:
		return "tool completed"
	}
}

// messageEndEvents records what a finished assistant message tells us: its token usage, and the
// model's reasoning when the provider returned any.
//
// A finalized message is the only place these runtimes report usage, and message_end fires for
// user and toolResult messages too, so a message with neither usage nor reasoning produces nothing
// rather than an empty row per turn.
func (f piFamily) messageEndEvents(input map[string]interface{}, fields map[string]interface{}) []normalizedEvent {
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
		events = append(events, f.one("agent.reasoning", "reasoning", "info", "agent reasoning", reasoningFields)...)
	}

	if usage := piUsage(firstMap(message, "usage")); len(usage) > 0 {
		usageFields := cloneFields(fields)
		usageFields["gen_ai"] = mergeNested(usageFields["gen_ai"], map[string]interface{}{"usage": usage})
		events = append(events, f.one("token.usage", "metric", "info", "token usage", usageFields)...)
	}

	return events
}

// piReasoningText concatenates the thinking parts of an assistant message.
//
// Assistant content is a list of parts, and a reasoning model emits thinking alongside text in the
// same message. Only the thinking parts are collected here: the assistant's visible answer is not
// reasoning, and recording it as such would put the model's output where a reader looking for its
// private deliberation expects to find it.
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

// piUsage normalizes a Pi-family Usage object into gen_ai.usage.
//
// These runtimes name their fields input/output/cacheRead/cacheWrite/reasoning and nest cost under
// `cost`, none of which match the OTel GenAI semconv names Beacon writes, and two of which are
// nested objects on Beacon's side rather than scalars. The mapping is spelled out against the
// canonical GenAIUsageInfo shape rather than copied through, so gen_ai.usage stays the only token
// representation in the log and no parallel per-harness field appears beside it.
//
// `output` already includes `reasoning` tokens, so reasoning is recorded under its own key but
// never added to anything: treating it as a separate bucket would double-count it in any total.
// A `totalTokens` is also reported and deliberately dropped -- Beacon's usage shape has no total,
// and a redundant field that can disagree with its own parts is worse than an absent one.
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
