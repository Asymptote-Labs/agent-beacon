package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// prime-event is the single entry point for every Prime Agent lifecycle payload.
//
// Prime Agent delivers its events to an in-process extension whose handlers all fire in the same
// process, so one command receiving a typed envelope fits better than the command-per-hook shape
// used for runtimes that exec a separate hook per event -- the same reason pi-event exists, and the
// same envelope, because Prime Agent ships the Pi coding agent under its own branding.
//
// What is not shared is everything below. Prime Agent's agent has one tool: `ipython`, a cell of
// Python executed in a persistent kernel. Reading a file, editing a file, and running a shell
// command are all Python written into that cell rather than distinct tool calls, so the mapping
// that serves Pi's read/edit/write/bash surface would record a Prime Agent session as a long series
// of undifferentiated tool calls with nothing in them.
var primeEventCmd = &cobra.Command{
	Use:   "prime-event",
	Short: "Record Prime Agent hook telemetry",
	Long:  `prime-event receives raw Beacon Prime Agent extension payloads and writes local endpoint telemetry.`,
	Run:   runPrimeEvent,
}

// runPrimeEvent is named rather than inlined into the command so tests can drive the whole path --
// stdin envelope to written event -- without building a cobra command.
func runPrimeEvent(cmd *cobra.Command, args []string) {
	runPiFamilyEventFrom(primeRuntime, primeEndpointEvents)
}

// primeKernelToolName is the name of the tool that is Prime Agent's entire execution surface.
const primeKernelToolName = "ipython"

func init() {
	rootCmd.AddCommand(primeEventCmd)
}

// supportedPrimeEventTypes lists every Prime Agent event type this mapper handles.
//
// These strings are the contract between the managed extension's subscription list and this mapper.
// A typo on either side produces no telemetry rather than an error, so both sides pin the list and
// a test asserts each entry still maps to an event.
//
// It is Pi's list plus the two events Prime Agent's build fires and Pi's does not: a compaction,
// which rewrites the history the agent is working from, and a refinement, which rewrites the
// durable harness it will start every future session with.
func supportedPrimeEventTypes() []string {
	return []string{
		"session_start",
		"session_shutdown",
		"input",
		"tool_call",
		"tool_result",
		"user_bash",
		"message_end",
		"session_compact",
		"refine_complete",
	}
}

// primeEndpointEvents maps one Prime Agent payload onto the endpoint events it justifies.
//
// An unrecognized type returns nothing rather than a generic event, for the same reason the Pi and
// Cline mappers drop unknown ones: the runtime publishes far more events than the extension
// subscribes to, and a future one arriving here should be silent rather than becoming an
// undifferentiated "something happened" row that every query matches and none can explain.
func primeEndpointEvents(input map[string]interface{}, sessionID string) []normalizedEvent {
	switch getFirstStr(input, "type") {
	case "tool_call":
		fields := primeRuntime.baseFields(input, sessionID)
		// The pre-execution half of a tool call: the runtime has decided to run it and named its
		// arguments, but nothing has happened yet. Recorded as tool.invoked, and deliberately not
		// as an approval -- the tool_call handler can block, but that is an extension deciding
		// rather than an operator being asked, so there is no approval decision here to observe.
		//
		// For the kernel tool this row is the only record of what the agent intended to run if the
		// cell never returns: a kernel that hangs, is interrupted, or takes the process down with
		// it produces no tool_result at all.
		mergeMap(fields, primeToolFields(input))
		applyToolCallID(fields, input)
		return primeRuntime.one("tool.invoked", "tool", "info", "tool invoked", fields)

	case "tool_result":
		return primeToolResultEvents(input, primeRuntime.baseFields(input, sessionID))

	case "session_compact":
		return primeSessionCompactEvents(input, primeRuntime.baseFields(input, sessionID))

	case "refine_complete":
		return primeRefineCompleteEvents(input, primeRuntime.baseFields(input, sessionID))

	default:
		// session_start, session_shutdown, input, user_bash and message_end are the envelope Prime
		// Agent shares with Pi, mapped once in pi_family.go. Delegating rather than restating them
		// is what keeps the two runtimes from drifting apart on what a session start means; an
		// unrecognized type falls through that mapper's own default and produces nothing.
		return primeRuntime.endpointEvents(input, sessionID)
	}
}

// primeIsKernelTool reports whether a tool name is Prime Agent's Python kernel.
func primeIsKernelTool(name string) bool {
	return strings.EqualFold(name, primeKernelToolName)
}

// primeToolFields builds the tool, command, and file blocks for one Prime Agent tool event.
//
// The kernel cell is recorded as a command, not merely as a tool input. That is a deliberate
// reading of what the runtime is: the cell is the only thing Prime Agent's agent ever executes, so
// every shell command it runs, every file it writes and every network call it makes is Python text
// inside one of these. Leaving that text out of `command.command` would mean no command-shaped
// detection -- in `beacon scan` or in a customer's SIEM -- ever matches a Prime Agent session,
// while the same activity in every other supported runtime matches. `tool.name` stays `ipython`
// beside it, so a reader can always tell a Python cell from a shell command.
//
// Anything else -- the file and shell tools the package still defines, and any tool another
// extension registered -- goes through the shared reader, which names what it recognizes and
// invents nothing for what it does not.
func primeToolFields(input map[string]interface{}) map[string]interface{} {
	name := piToolName(input)
	if !primeIsKernelTool(name) {
		return primeRuntime.toolFields(input, false)
	}

	fields := map[string]interface{}{}
	if code := getFirstStr(piToolInput(input), "code"); code != "" {
		fields["tool"] = map[string]interface{}{"command": code}
		fields["command"] = map[string]interface{}{"command": code}
		fields["content"] = retainedContentFields(code)
	}
	fields["tool"] = mergeNested(fields["tool"], map[string]interface{}{"name": name})
	return fields
}

// primeToolAction maps a Prime Agent tool name onto the endpoint action its completion represents.
func primeToolAction(name string) (string, string) {
	if primeIsKernelTool(name) {
		return "command.executed", "command"
	}
	// Everything else -- the file and shell tools the package still defines, an MCP-routed tool, or
	// any tool another extension registered -- reads the same as it does for Pi, and reads through
	// the same function so the two cannot drift on what a `write` is.
	return piToolAction(name)
}

// primeToolResultEvents maps a completed tool call onto the outcome events it justifies.
//
// One payload becomes several events because one cell does several things. A cell that edits two
// files and messages a sibling agent reports all of it in a single `details` object; recording only
// the cell would mean the edits and the message appear nowhere a file-scoped or session-scoped
// query can find them.
//
// The edits and messages are emitted whether or not the cell as a whole failed, because they
// already happened: the kernel streams them as the cell runs, so an exception on the last line does
// not un-write the file the third line wrote.
func primeToolResultEvents(input map[string]interface{}, base map[string]interface{}) []normalizedEvent {
	details := firstMap(input, "details")
	name := piToolName(input)

	fields := cloneFields(base)
	mergeMap(fields, primeToolFields(input))
	if primeIsKernelTool(name) {
		primeApplyCellOutcome(fields, details)
	}
	// The join key back to the tool.invoked that named this call. It is applied after the tool and
	// command blocks are in place, because applyToolCallID only writes onto an event that describes
	// a tool action -- and it is the only thing linking the two halves of one call, which otherwise
	// sit in the log as unrelated events sharing a session and a nearby timestamp.
	applyToolCallID(fields, input)

	var events []normalizedEvent
	if primeToolFailed(input, details) {
		fields["error"] = map[string]interface{}{"type": primeErrorType(details)}
		events = append(events, primeRuntime.one("tool.failed", "tool", "high",
			piToolMessageSuffix("tool.failed"), fields)...)
	} else {
		action, category := primeToolAction(name)
		action, category = piDowngradeUnsupportedAction(fields, action, category)
		events = append(events, primeRuntime.one(action, category, "info",
			piToolMessageSuffix(action), fields)...)
	}

	// Built from the base rather than from the enriched fields on purpose. A file edit and an agent
	// message are their own facts, and copying the whole Python cell onto each of them would repeat
	// the cell body once per edit while telling a reader nothing the cell's own row does not. The
	// link back to the cell is gen_ai.tool.call.id, which every event from this payload carries.
	events = append(events, primeKernelDiffEvents(input, details, base)...)
	events = append(events, primeAgentMessageEvents(details, base)...)
	return events
}

// primeToolFailed reads failure from both places the runtime reports it.
//
// `isError` is the tool protocol's own flag; `details.status` is the kernel's. They normally agree,
// but a cell aborted by an interrupt sets the status without the flag, and treating that as a
// success would record a cell that never finished as one that did.
func primeToolFailed(input, details map[string]interface{}) bool {
	if isErr, ok := input["isError"].(bool); ok && isErr {
		return true
	}
	switch getFirstStr(details, "status") {
	case "error", "aborted":
		return true
	}
	return false
}

// primeErrorType names the failure as specifically as the payload allows.
//
// The kernel reports the Python exception class -- KeyError, PermissionError, TimeoutError -- which
// is far more useful to a reader than the generic tool_error every runtime shares, so it is
// preferred when present.
func primeErrorType(details map[string]interface{}) string {
	if ename := getFirstStr(details, "errorEname"); ename != "" {
		return ename
	}
	if err := firstMap(details, "error"); err != nil {
		if ename := getFirstStr(err, "ename"); ename != "" {
			return ename
		}
	}
	return "tool_error"
}

// primeApplyCellOutcome fills the command block's result fields from the kernel's own report.
func primeApplyCellOutcome(fields map[string]interface{}, details map[string]interface{}) {
	if details == nil {
		return
	}
	command := map[string]interface{}{}
	if existing, ok := fields["command"].(map[string]interface{}); ok {
		command = existing
	}
	if ms, ok := jsonInt(details["durationMs"]); ok {
		command["duration_ms"] = ms
	}
	if output := primeCellOutput(details); output != "" {
		command["output"] = output
	}
	if len(command) > 0 {
		fields["command"] = command
	}

	// No exit code is derived. The kernel reports ok/error/aborted, which is not an exit status,
	// and writing 0 or 1 for it would put a number the runtime never produced into a field readers
	// compare against real process exit codes.
	promoted := map[string]interface{}{}
	if status := getFirstStr(details, "status"); status != "" {
		promoted[primeRuntime.rawKey("cell_status")] = status
	}
	if restarted, ok := details["kernelRestarted"].(bool); ok && restarted {
		// The cell ran against a kernel that had just been killed and restarted, so none of the
		// variables, imports or running tasks the agent set up earlier in the session survived into
		// it. A reader reconstructing what the agent was working from needs to know that.
		promoted[primeRuntime.rawKey("kernel_restarted")] = true
	}
	if len(promoted) > 0 {
		fields["raw"] = mergeNested(fields["raw"], promoted)
	}
}

// primeCellOutput is what the cell printed, as one string.
//
// stdout first, then stderr, because that is the order a reader expects and because a cell that
// failed frequently printed nothing else. Both are already under `raw` in full; this is the field a
// command-shaped query reads, so it carries the streams an operator would have watched and not the
// trailing expression value, which is the kernel's answer rather than the cell's output.
func primeCellOutput(details map[string]interface{}) string {
	parts := make([]string, 0, 2)
	for _, key := range []string{"stdout", "stderr"} {
		if value := getFirstStr(details, key); value != "" {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, "\n")
}

// primeKernelDiffEvents records the file edits a cell streamed while it ran.
//
// This is the only structured file telemetry Prime Agent produces. Its `edit` skill emits a display
// payload for every replacement it makes, which the host collects into `details.diffs`; a file
// written by plain Python -- `Path.write_text`, a shell redirect through the kernel's bash helper --
// emits nothing and is visible only as text inside the cell. Beacon records what the runtime
// reports and does not guess at the rest: parsing Python to find file writes would produce
// confident, wrong paths.
//
// Both spellings of each key are read because both occur: the skill emits snake_case from Python
// and the host converts to camelCase on the way to the extension, so a payload that reached Beacon
// without passing through that conversion still maps.
func primeKernelDiffEvents(input, details map[string]interface{}, base map[string]interface{}) []normalizedEvent {
	if details == nil {
		return nil
	}
	items, _ := details["diffs"].([]interface{})
	var events []normalizedEvent
	for _, item := range items {
		diff, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		path := getFirstStr(diff, "path")
		if path == "" {
			continue
		}
		before := getFirstStr(diff, "oldStr", "old_str")
		after := getFirstStr(diff, "newStr", "new_str")
		diffText := ""
		if before != "" || after != "" {
			// The same before/after rendering the opencode mapper uses for the same reason: the
			// runtime reports the two sides of the replacement rather than a unified patch, and a
			// synthesized patch with invented hunk headers would claim line numbers Beacon does not
			// have.
			diffText = fmt.Sprintf("--- before\n%s\n+++ after\n%s", before, after)
		}
		fields := cloneFields(base)
		mergeMap(fields, diffFields(path, diffText))
		applyToolCallID(fields, input)
		events = append(events, primeRuntime.one("file.modified", "file", "info",
			piToolMessageSuffix("file.modified"), fields)...)
	}
	return events
}

// primeAgentMessageEvents records messages this agent sent to another agent.
//
// Prime Agent sessions can discover and message one another, which means one agent's work can be
// directed by another agent rather than by the person at the keyboard. That is a distinct fact from
// an assistant replying to its user, so it gets its own action rather than reusing `agent.message`:
// a query asking "what did this agent tell other agents to do" and a query asking "what did the
// agent say" want different rows, and one action serving both makes neither answerable.
func primeAgentMessageEvents(details map[string]interface{}, base map[string]interface{}) []normalizedEvent {
	if details == nil {
		return nil
	}
	items, _ := details["sentAgentMessages"].([]interface{})
	var events []normalizedEvent
	for _, item := range items {
		sent, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		text := getFirstStr(sent, "message")
		target := firstMap(sent, "target")
		if text == "" && target == nil {
			continue
		}
		fields := cloneFields(base)
		promoted := map[string]interface{}{}
		if text != "" {
			// The message body is promoted rather than left inside the tool result's nested
			// details, for the same reason the refinement summary is: it is the content of this
			// event, and a reader asking what one agent told another should not have to walk into
			// a raw payload to find it.
			promoted[primeRuntime.rawKey("agent_message_text")] = text
			fields["content"] = retainedContentFields(text)
		}
		if id := getFirstStr(sent, "id"); id != "" {
			promoted[primeRuntime.rawKey("agent_message_id")] = id
		}
		if status := getFirstStr(sent, "deliveryStatus", "delivery_status"); status != "" {
			promoted[primeRuntime.rawKey("agent_message_delivery")] = status
		}
		if role := getFirstStr(sent, "receiverRole", "receiver_role"); role != "" {
			// parent, sibling or child: which direction in the agent tree this message travelled.
			promoted[primeRuntime.rawKey("agent_message_receiver_role")] = role
		}
		if target != nil {
			if id := getFirstStr(target, "sessionId", "session_id"); id != "" {
				promoted[primeRuntime.rawKey("agent_message_target_session")] = id
			}
			if name := getFirstStr(target, "sessionName", "session_name"); name != "" {
				promoted[primeRuntime.rawKey("agent_message_target_name")] = name
			}
		}
		if len(promoted) > 0 {
			fields["raw"] = mergeNested(fields["raw"], promoted)
		}
		events = append(events, primeRuntime.one("agent.message.sent", "session", "info",
			"agent message sent", fields)...)
	}
	return events
}

// primeSessionCompactEvents records that the conversation history was rewritten.
//
// Without this row the prompts and tool results before a compaction read as though they are still
// in context when they are not, which changes what a reviewer concludes the agent was working from
// when it made its next decision.
func primeSessionCompactEvents(input map[string]interface{}, fields map[string]interface{}) []normalizedEvent {
	if fromExtension, ok := input["fromExtension"].(bool); ok {
		// A compaction another extension triggered is not one the agent or the operator asked for.
		fields["raw"] = mergeNested(fields["raw"], map[string]interface{}{
			primeRuntime.rawKey("compaction_from_extension"): fromExtension,
		})
	}
	return primeRuntime.one("session.compacted", "session", "info", "session compacted", fields)
}

// primeRefineCompleteEvents records the agent editing its own harness.
//
// This is the self-improving loop Prime Agent is built around: a refinement round rewrites the
// supplemental prompts, memories and skill descriptions the agent starts from. It is the one action
// in the runtime that outlives the session that took it, so it is the change with the longest reach
// and the one an operator is least likely to see any other way.
//
// A global-scope refinement is reported at medium severity because it persists into every future
// session on this machine, while a local one dies with the session that made it. Nothing is
// blocked or judged here -- the severity is what lets a rule or a dashboard sort durable
// self-modification above the routine kind.
func primeRefineCompleteEvents(input map[string]interface{}, fields map[string]interface{}) []normalizedEvent {
	scope := getFirstStr(input, "scope")
	applied, hasApplied := jsonInt(input["appliedEdits"])

	promoted := map[string]interface{}{}
	if id := getFirstStr(input, "id"); id != "" {
		promoted[primeRuntime.rawKey("refinement_id")] = id
	}
	if scope != "" {
		promoted[primeRuntime.rawKey("refinement_scope")] = scope
	}
	if hasApplied {
		promoted[primeRuntime.rawKey("refinement_applied_edits")] = applied
	}
	if len(promoted) > 0 {
		fields["raw"] = mergeNested(fields["raw"], promoted)
	}
	if summary := getFirstStr(input, "summary"); summary != "" {
		// Promoted out of the nested payload as well as marked, because "what did the agent change
		// about itself" is the question this event exists to answer, and an answer three levels
		// down inside raw is one a dashboard column cannot show.
		fields["raw"] = mergeNested(fields["raw"], map[string]interface{}{
			primeRuntime.rawKey("refinement_summary"): summary,
		})
		fields["content"] = retainedContentFields(summary)
	}

	severity := "info"
	if scope == "global" && (!hasApplied || applied > 0) {
		severity = "medium"
	}
	return primeRuntime.one("agent.harness.refined", "session", severity, "harness refined", fields)
}
