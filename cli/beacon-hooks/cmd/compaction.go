package cmd

import (
	"github.com/spf13/cobra"
)

// Context compaction, as two commands over one implementation.
//
// Shaped like subagent.go rather than like cursor-event: the runtime binds a distinct command per
// lifecycle event, so argv already says which event fired and the payload never has to be consulted
// to find out. That matters here because pre and post are the same payload plus a verb -- reading
// `hook_event_name` to tell them apart would make the mapping depend on a field whose spelling
// varies between runtimes, for a question the install already answered.
//
// Compaction is worth recording at all because it is the point where an agent's own history stops
// being a complete account of the session. An investigator reading a Muse Code session that
// compacted mid-run needs to see that the earlier turns were summarized away rather than never
// happening, and a `token.usage` drop across a compaction boundary is expected rather than
// anomalous.
var preCompactCmd = &cobra.Command{
	Use:   "pre-compact",
	Short: "Record the start of context compaction",
	Run: func(cmd *cobra.Command, args []string) {
		runCompaction("session.compacting", "Context compaction started")
	},
}

var postCompactCmd = &cobra.Command{
	Use:   "post-compact",
	Short: "Record the completion of context compaction",
	Run: func(cmd *cobra.Command, args []string) {
		runCompaction("session.compacted", "Context compaction completed")
	},
}

func init() {
	rootCmd.AddCommand(preCompactCmd)
	rootCmd.AddCommand(postCompactCmd)
}

func runCompaction(action, message string) {
	input, err := readStdinJSON()
	if err != nil {
		outputJSON(hookNoopResponse())
		return
	}
	sessionID := resolveSessionID(input, platformFlag)
	logger := newHookLogger("compaction", platformFlag, sessionID)
	emitHookEvent(logger, action, "session", "info", message, input, sessionFields(sessionID, input))
	outputJSON(hookNoopResponse())
}
