package cmd

import (
	"strings"

	"github.com/spf13/cobra"
)

// pi-event is the single entry point for every Pi lifecycle payload.
//
// Pi delivers its events to an in-process extension whose handlers all fire in the same process, so
// one command receiving a typed envelope fits better than the command-per-hook shape used for
// runtimes that exec a separate hook per event. This mirrors how opencode and Cline are integrated.
//
// The envelope itself is mapped by the shared pi-family code; what is below is Pi's tool surface,
// which is where it and Prime Agent genuinely differ.
var piEventCmd = &cobra.Command{
	Use:   "pi-event",
	Short: "Record Pi hook telemetry",
	Long:  `pi-event receives raw Beacon Pi extension payloads and writes local endpoint telemetry.`,
	Run:   runPiEvent,
}

// runPiEvent is named rather than inlined into the command so tests can drive the whole path --
// stdin envelope to written event -- without building a cobra command.
var runPiEvent = piFamilyEventRunner(piMapping, piEndpointEvents)

var piMapping = piFamilyMapping{platform: "pi", displayName: "Pi"}

func init() {
	rootCmd.AddCommand(piEventCmd)
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
	fields := piMapping.baseFields(input, sessionID)

	switch getFirstStr(input, "type") {
	case "session_start":
		return piMapping.sessionStartEvents(input, fields)

	case "session_shutdown":
		return piMapping.sessionShutdownEvents(input, fields)

	case "input":
		return piMapping.inputEvents(input, fields)

	case "tool_call":
		// The pre-execution half of a tool call: Pi has decided to run it and named its arguments,
		// but nothing has happened yet. Recorded as tool.invoked to match the Cline mapper's
		// tool_before stage, and deliberately not as an approval -- Pi's tool_call handler can
		// block, but that is an extension deciding rather than an operator being asked, so there is
		// no approval decision here to observe.
		mergeMap(fields, piToolFields(input, false))
		return piFamilyOneEvent("tool.invoked", "tool", "info", piMapping.message("tool invoked"), fields)

	case "tool_result":
		return piToolResultEvents(input, fields)

	case "user_bash":
		return piMapping.userBashEvents(input, fields)

	case "message_end":
		return piMapping.messageEndEvents(input, fields)

	default:
		return nil
	}
}

// piToolFields builds the tool, command, and file blocks for one Pi tool event.
//
// Pi's built-in tools are read, edit, write and bash, all with fixed documented argument shapes, so
// the shared reader handles them by name. A custom tool registered by another extension carries an
// arbitrary shape, and gets tool.name plus its raw arguments without a command or file block
// invented for it.
func piToolFields(input map[string]interface{}, withResult bool) map[string]interface{} {
	name := piFamilyToolName(input)
	fields := piFamilyBuiltinToolFields(name, piFamilyToolInput(input))
	if name != "" {
		fields["tool"] = mergeNested(fields["tool"], map[string]interface{}{"name": name})
	}
	if withResult {
		if usage := piFamilyUsage(firstMap(input, "usage")); len(usage) > 0 {
			fields["gen_ai"] = mergeNested(fields["gen_ai"], map[string]interface{}{"usage": usage})
		}
	}
	return fields
}

// piToolResultEvents maps a completed tool call onto its outcome event.
func piToolResultEvents(input map[string]interface{}, fields map[string]interface{}) []normalizedEvent {
	mergeMap(fields, piToolFields(input, true))
	name := piFamilyToolName(input)

	if isErr, ok := input["isError"].(bool); ok && isErr {
		fields["error"] = map[string]interface{}{"type": "tool_error"}
		return piFamilyOneEvent("tool.failed", "tool", "high", piMapping.toolMessage("tool.failed"), fields)
	}

	if diff := piEditDiff(input); diff != "" {
		fields["content"] = retainedContentFields(diff)
	}

	action, category := piToolAction(name)
	action, category = piFamilyDowngradeUnsupportedAction(action, category, fields)
	return piFamilyOneEvent(action, category, "info", piMapping.toolMessage(action), fields)
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
