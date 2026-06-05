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
Records local prompt submission telemetry.`,
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

	logger.Debug("Prompt submit observed")
	fields := sessionFields(sessionID, input)
	if isCascadePlatform(platformFlag) {
		fields = cascadeMetadataFields(sessionID, input)
	}
	prompt := getFirstStr(input, "prompt", "user_prompt", "userPrompt", "text", "promptText", "input")
	if platformFlag == "hermes" {
		prompt = hermesFirstString(input, "user_message", "prompt", "input", "text")
	}
	if isCascadePlatform(platformFlag) {
		prompt = cascadePrompt(input)
	}
	hasPrompt := prompt != ""
	if hasPrompt && config.ContentRetentionMode() != config.ContentRetentionMetadata {
		fields["prompt"] = map[string]interface{}{"text": prompt}
	}
	emitHookEvent(logger, "prompt.submitted", "prompt", "info", "Prompt submitted to agent", input, fields)

	if platformFlag == "antigravity" && sessionID != "" && hasPrompt {
		st := state.NewSessionState(sessionID, "antigravity")
		if err := st.SetPromptEmitted(); err != nil {
			logger.Warn("Failed to persist prompt state", "error", err.Error())
		}
	}

	outputJSON(noopResponse)
}
