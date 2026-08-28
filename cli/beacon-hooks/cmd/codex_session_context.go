package cmd

import (
	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/logging"
)

var codexSessionContextCmd = &cobra.Command{
	Use:    "codex-session-context",
	Short:  "Record local user context for a Codex session",
	Hidden: true,
	Run:    runCodexSessionContext,
}

func init() {
	rootCmd.AddCommand(codexSessionContextCmd)
}

func runCodexSessionContext(cmd *cobra.Command, args []string) {
	input, err := readStdinJSON()
	if err != nil || platformFlag != "codex" {
		outputJSON(emptyResponse)
		return
	}

	sessionID := resolveSessionID(input, "codex")
	if sessionID == "" {
		outputJSON(emptyResponse)
		return
	}
	logger := logging.NewSessionLogger("codex-session-context", "codex", sessionID)
	fields := sessionFieldsForPlatform(sessionID, input, "codex")
	fields["raw"] = map[string]interface{}{"source": "codex_session_start_hook"}
	emitHookEvent(logger, "session.context", "session", "info", "Codex session context observed", input, fields)
	outputJSON(emptyResponse)
}
