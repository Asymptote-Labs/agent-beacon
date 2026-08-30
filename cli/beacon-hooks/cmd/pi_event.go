package cmd

import (
	"github.com/spf13/cobra"
)

// pi-event is the single entry point for every Pi lifecycle payload.
//
// Pi delivers its events to an in-process extension whose handlers all fire in the same process, so
// one command receiving a typed envelope fits better than the command-per-hook shape used for
// runtimes that exec a separate hook per event. This mirrors how opencode and Cline are integrated.
//
// The mapping itself lives in pi_family.go, shared with Oh My Pi, which kept Pi's extension event
// shapes when it forked. What is not shared is identity: the two runtimes are recorded under
// separate harness names and each event carries its own runtime's block in `raw`.
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
	runPiFamilyEvent(piRuntime)
}

// runPiFamilyEvent reads one envelope from stdin and writes the events it justifies.
//
// Shared by pi-event and omp-event because the transport is identical -- the extension spawns the
// hook binary and writes one JSON object to its stdin -- and only the runtime it describes differs.
func runPiFamilyEvent(runtime piFamily) {
	runPiFamilyEventFrom(runtime, runtime.endpointEvents)
}

// runPiFamilyEventFrom is the same transport with a caller-supplied mapper.
//
// Prime Agent is why it is separate. It delivers this exact envelope, so the session lifecycle,
// prompts, operator commands and assistant messages are the shared mapping above -- but its agent
// has one tool, a Python kernel cell, and the shared read/edit/write/bash reading would record its
// whole session as undifferentiated tool calls. So it supplies its own mapper for the tool surface
// and the two events it alone fires, and delegates the rest (see prime_event.go).
func runPiFamilyEventFrom(runtime piFamily, events func(map[string]interface{}, string) []normalizedEvent) {
	input, err := readStdinJSON()
	if err != nil {
		outputJSON(emptyResponse)
		return
	}
	sessionID := resolveSessionID(input, runtime.platform)
	logger := newHookLogger(runtime.platform+"-event", runtime.platform, sessionID)
	for _, event := range events(input, sessionID) {
		if event.action == "" {
			continue
		}
		_ = logger.EndpointEvent(event.action, event.category, event.severity, event.message, event.fields)
	}
	outputJSON(emptyResponse)
}

// supportedPiEventTypes lists every Pi event type this mapper handles.
//
// These strings are the contract between the managed extension's subscription list and the mapper.
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
