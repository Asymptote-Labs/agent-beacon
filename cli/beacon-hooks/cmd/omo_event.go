package cmd

import (
	"github.com/spf13/cobra"
)

// omo-event is the single entry point for every Senpi (oh-my-openagent standalone edition)
// lifecycle payload.
//
// Senpi loads Beacon as a TypeScript extension whose handlers all fire in the same process, so one
// command receiving a typed envelope fits the runtime the way it does for Pi, Oh My Pi, Prime
// Agent, opencode and Cline, rather than the command-per-hook shape used by runtimes that exec a
// separate hook per event.
//
// It is a separate command from pi-event, not a flag on it, for the reason omp-event and
// prime-event are: the runtimes are separately installed products, they are recorded under
// separate harness names, and a running install must not change which runtime it attributes events
// to. The event *mapping* is shared (see pi_family.go) precisely because Senpi is an in-flight fork
// of pi-mono that kept its payload shapes.
var omoEventCmd = &cobra.Command{
	Use:   "omo-event",
	Short: "Record Senpi (oh-my-openagent) hook telemetry",
	Long:  `omo-event receives raw Beacon Senpi extension payloads and writes local endpoint telemetry.`,
	Run:   runOmoEvent,
}

func init() {
	rootCmd.AddCommand(omoEventCmd)
}

func runOmoEvent(cmd *cobra.Command, args []string) {
	runPiFamilyEvent(omoRuntime)
}

// supportedOmoEventTypes lists every Senpi event type this mapper handles.
//
// Like supportedPiEventTypes and supportedPrimeEventTypes, this is the contract between the
// managed extension's subscription list and the mapper: a typo on either side produces no
// telemetry rather than an error, so both sides pin the list and a test asserts each entry still
// maps to an event.
//
// The list is Pi's exactly, and that is a finding rather than a copy. Senpi's ExtensionEvent union
// has more than thirty members -- provider-request and response internals, streaming message and
// tool-execution updates, compaction, model-select, turn bookkeeping and TUI plumbing among them --
// and of the ones that describe an agent action rather than how the runtime got its work done,
// these seven are what Beacon's extension subscribes to.
//
// Two absences are Senpi's own rather than a choice made here. It has no `user_python`: that is Oh
// My Pi's own addition, a `$` operator prefix Senpi's fork never added. And it has no approval
// event at all -- its `tool_call` handler can block, but that is an extension deciding rather than
// an operator being asked, and its permission-system builtin owns the real approval prompt without
// publishing it as an extension event -- so Beacon records tool activity and leaves approval
// telemetry empty, exactly as it does for Pi and Prime Agent.
func supportedOmoEventTypes() []string {
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
