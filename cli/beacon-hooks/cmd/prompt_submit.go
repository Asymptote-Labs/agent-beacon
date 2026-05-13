package cmd

import (
	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/logging"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/state"
)

var promptSubmitCmd = &cobra.Command{
	Use:   "prompt-submit",
	Short: "Handle prompt submission for local endpoint telemetry",
	Long: `UserPromptSubmit hook - triggered when the user submits a prompt.
The public Beacon build does not fetch hosted policies or inject remote context.`,
	Run: runPromptSubmit,
}

func init() {
	rootCmd.AddCommand(promptSubmitCmd)
}

func runPromptSubmit(cmd *cobra.Command, args []string) {
	noopResponse := emptyResponse
	if platformFlag == "cursor" {
		noopResponse = map[string]interface{}{"continue": true}
	}

	input, err := readStdinJSON()
	if err != nil {
		outputJSON(noopResponse)
		return
	}

	sessionID := resolveSessionID(input, platformFlag)
	var logger *logging.Logger
	if sessionID != "" {
		logger = logging.NewSessionLogger("prompt-submit", platformFlag, sessionID)
	} else {
		logger = logging.NewLoggerForPlatform("prompt-submit", platformFlag)
	}

	if sessionID != "" {
		state.NewSessionState(sessionID, platformFlag).ClearSbdPolicies()
	}
	if config.IsSecureByDesignEnabled(platformFlag) {
		logger.Warn("Secure by Design policy injection is unavailable in the local-only Beacon build")
	} else {
		logger.Debug("Prompt submit observed")
	}
	fields := sessionFields(sessionID, input)
	if config.ContentRetentionMode() != config.ContentRetentionMetadata {
		if prompt := getFirstStr(input, "prompt", "user_prompt", "text"); prompt != "" {
			fields["raw"] = map[string]interface{}{"prompt": prompt}
		}
	}
	emitHookEvent(logger, "prompt.submitted", "prompt", "info", "Prompt submitted to agent", input, fields)

	outputJSON(noopResponse)
}
