package cmd

import (
	"github.com/spf13/cobra"
)

// omp-event is the single entry point for every Oh My Pi lifecycle payload.
//
// Oh My Pi (`omp`) loads Beacon as a TypeScript extension whose handlers all fire in the same
// process, so one command receiving a typed envelope fits the runtime the way it does for Pi,
// opencode and Cline, rather than the command-per-hook shape used by runtimes that exec a separate
// hook per event.
//
// It is a separate command from pi-event, not a flag on it, because the two runtimes are separate
// products: they install independently, they are recorded under separate harness names, and a
// running install must not change which runtime it attributes events to. The event *mapping* is
// shared (see pi_family.go) precisely because Oh My Pi forked Pi and kept its payload shapes.
var ompEventCmd = &cobra.Command{
	Use:   "omp-event",
	Short: "Record Oh My Pi hook telemetry",
	Long:  `omp-event receives raw Beacon Oh My Pi extension payloads and writes local endpoint telemetry.`,
	Run:   runOmpEvent,
}

func init() {
	rootCmd.AddCommand(ompEventCmd)
}

func runOmpEvent(cmd *cobra.Command, args []string) {
	runPiFamilyEvent(ompRuntime)
}

// supportedOmpEventTypes lists every Oh My Pi event type this mapper handles.
//
// Like supportedPiEventTypes, this is the contract between the managed extension's subscription
// list and the mapper: a typo on either side produces no telemetry rather than an error, so both
// sides pin the list and a test asserts each entry still maps to an event.
//
// Oh My Pi publishes considerably more than this -- provider request/response internals, streaming
// message updates, compaction and retry signals, TUI plumbing. Those describe how the runtime got
// its work done rather than what the agent did, and subscribing to them would fill the runtime log
// with rows no investigation asks for while putting Beacon in the path of every streaming token.
//
// The three entries Pi's list does not have are the ones Oh My Pi exposes and Pi does not:
// `tool_approval_requested`/`tool_approval_resolved` are real operator decisions, which is the
// signal Beacon has always refused to synthesize on runtimes that do not report it; `user_python`
// is the operator's own `$` code, which no tool event covers.
//
// `mcp_notification` is deliberately absent. It fires for every JSON-RPC notification a connected
// server sends, most of them routine tools/resources list refreshes, and it describes MCP transport
// plumbing rather than an action the agent took. The MCP activity worth recording is the agent
// calling an MCP tool, which arrives as an ordinary tool_call/tool_result pair and is attributed to
// its server by piMCPServerTool.
func supportedOmpEventTypes() []string {
	return []string{
		"session_start",
		"session_shutdown",
		"input",
		"tool_call",
		"tool_result",
		"user_bash",
		"user_python",
		"tool_approval_requested",
		"tool_approval_resolved",
		"message_end",
	}
}
