package cmd

import (
	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/logging"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/state"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
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
	if platformFlag == "antigravity" && !antigravityTurnStart(input) {
		outputJSON(hookNoopResponse())
		return
	}

	sessionID := resolveSessionID(input, platformFlag)
	logger := newHookLogger("prompt-submit", platformFlag, sessionID)

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
	if platformFlag == openHandsPlatform {
		prompt = openHandsPrompt(input)
	}
	if platformFlag == goosePlatform {
		prompt = goosePrompt(input)
	}
	if platformFlag == "antigravity" {
		// The same normalization the transcript fallback and the session-store sync apply, so
		// the prompt hashes the same on every path and a sync recognizes it as recorded.
		prompt = asymptoteobserve.AntigravityPrompt(getFirstStr(input, "lastUserInput"))
	}
	hasPrompt := prompt != ""
	if hasPrompt {
		setPromptFields(fields, prompt)
	}
	emitPromptSubmitted(logger, input, fields, prompt)

	if platformFlag == "antigravity" && sessionID != "" && hasPrompt {
		st := state.NewSessionState(sessionID, "antigravity")
		if err := st.SetPromptEmitted(); err != nil {
			logger.Warn("Failed to persist prompt state", "error", err.Error())
		}
	}

	maybeUploadCursorCloudTelemetry(logger)
	outputJSON(hookNoopResponse())
}

// emitPromptSubmitted writes the prompt.submitted event for fields, and the session.handoff link
// when prompt carries a handoff marker.
func emitPromptSubmitted(logger *logging.Logger, input, fields map[string]interface{}, prompt string) {
	emitHookEvent(logger, "prompt.submitted", "prompt", "info", "Prompt submitted to agent", input, fields)
	if link, ok := handoffLinkEvent(fields, prompt); ok {
		emitHookEvent(logger, link.action, link.category, link.severity, link.message, input, link.fields)
	}
}

// antigravityTurnStart reports whether an Antigravity PreInvocation payload is the first model
// call of a turn. Antigravity has no prompt-submit hook: PreInvocation runs before every model
// call, carrying the turn's prompt as `lastUserInput` each time, and `invocationNum` counts the
// calls from 0 again at the start of every turn, resumed conversations included. Recording only
// invocation 0 records each prompt once. A payload without the counter is recorded, because a
// duplicate prompt beats a lost one.
func antigravityTurnStart(input map[string]interface{}) bool {
	n, ok := jsonInt(input["invocationNum"])
	return !ok || n == 0
}
