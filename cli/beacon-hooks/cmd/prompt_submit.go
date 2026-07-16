package cmd

import (
	"github.com/spf13/cobra"

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
	input, err := readStdinJSON()
	if err != nil {
		outputJSON(hookNoopResponse())
		return
	}

	sessionID := resolveSessionID(input, platformFlag)
	logger := newHookLogger("prompt-submit", platformFlag, sessionID)

	logger.Debug("Prompt submit observed")
	maybeEmitInventoryHeartbeat(logger, input)
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
	if hasPrompt {
		fields["prompt"] = map[string]interface{}{"text": prompt}
	}
	emitHookEvent(logger, "prompt.submitted", "prompt", "info", "Prompt submitted to agent", input, fields)

	if platformFlag == "antigravity" && sessionID != "" && hasPrompt {
		st := state.NewSessionState(sessionID, "antigravity")
		if err := st.SetPromptEmitted(); err != nil {
			logger.Warn("Failed to persist prompt state", "error", err.Error())
		}
	}

	maybeUploadCursorCloudTelemetry(logger)
	outputJSON(hookNoopResponse())
}
