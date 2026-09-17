package cmd

import (
	"github.com/spf13/cobra"
)

// prime-event is the single entry point for every Prime Agent lifecycle payload.
//
// Prime Agent (Prime Intellect) loads Beacon as a TypeScript extension whose handlers all fire in
// the same process, so one command receiving a typed envelope fits the runtime the way it does for
// Pi, Oh My Pi, opencode and Cline, rather than the command-per-hook shape used by runtimes that
// exec a separate hook per event.
//
// It is a separate command from pi-event, not a flag on it, for the reason omp-event is: the two
// are separately installed products, they are recorded under separate harness names, and a running
// install must not change which runtime it attributes events to. The event *mapping* is shared (see
// pi_family.go) precisely because Prime Agent is a hard fork of pi-mono that kept its payload
// shapes.
var primeEventCmd = &cobra.Command{
	Use:   "prime-event",
	Short: "Record Prime Agent hook telemetry",
	Long:  `prime-event receives raw Beacon Prime Agent extension payloads and writes local endpoint telemetry.`,
	Run:   runPrimeEvent,
}

func init() {
	rootCmd.AddCommand(primeEventCmd)
}

func runPrimeEvent(cmd *cobra.Command, args []string) {
	runPiFamilyEvent(primeRuntime)
}

// supportedPrimeEventTypes lists every Prime Agent event type this mapper handles.
//
// Like supportedPiEventTypes, this is the contract between the managed extension's subscription
// list and the mapper: a typo on either side produces no telemetry rather than an error, so both
// sides pin the list and a test asserts each entry still maps to an event.
//
// The list is Pi's exactly, and that is a finding rather than a copy. Prime Agent publishes more
// than forty event types -- provider request and response internals, streaming message updates,
// compaction, refinement and tree navigation signals, TUI plumbing -- and of the ones that describe
// an agent action rather than how the runtime got its work done, these seven are what it exposes.
//
// Two absences are Prime Agent's own rather than a choice made here. It has no `user_python`: its
// operator surface is `!` for bash, and Python is the agent's tool rather than a second operator
// prefix, so there is nothing to subscribe to. And it has no approval event at all -- its
// `tool_call` handler can block, but that is an extension deciding rather than an operator being
// asked -- so Beacon records tool activity and leaves approval telemetry empty, exactly as it does
// for Pi and Cline. Oh My Pi's three extra subscriptions are the ones Oh My Pi added; Prime Agent
// forked pi-mono separately and did not.
func supportedPrimeEventTypes() []string {
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
