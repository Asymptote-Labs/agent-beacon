package cmd

import (
	"github.com/spf13/cobra"

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
		// The same two fields the Cline, opencode and Pi prompt mappers already
		// write. This path serves Claude Code, Cursor and the rest of the hook
		// runtimes, and wrote neither -- so a prompt captured here was readable only
		// as prompt.text, with nothing recording that the text was retained and
		// nothing in the semconv shape a message-oriented reader looks for.
		fields["gen_ai"] = mergeNested(fields["gen_ai"], map[string]interface{}{
			"input": map[string]interface{}{"messages": asymptoteobserve.TextInputMessages(prompt)},
		})
		fields["content"] = retainedContentFields(prompt)
	}
	emitHookEvent(logger, "prompt.submitted", "prompt", "info", "Prompt submitted to agent", input, fields)

	if platformFlag == "antigravity" && sessionID != "" && hasPrompt {
		st := state.NewSessionState(sessionID, "antigravity")
		if err := st.SetPromptEmitted(); err != nil {
			logger.Warn("Failed to persist prompt state", "error", err.Error())
		}
	}

	// Consult MDR after the prompt.submitted event is recorded, so the prompt is
	// captured even when the verdict steers the turn.
	var mdrResponse map[string]interface{}
	if hasPrompt {
		if resp, actioned := evaluateMDR(logger, input, sessionID, prompt); actioned {
			mdrResponse = resp
		}
	}

	maybeUploadCursorCloudTelemetry(logger)
	if mdrResponse != nil {
		outputJSON(mdrResponse)
		return
	}
	outputJSON(hookNoopResponse())
}
