package cmd

import (
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/logging"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/state"
)

var preToolCmd = &cobra.Command{
	Use:   "pre-tool",
	Short: "Gate file writes to inject security policy context",
	Long: `PreToolUse hook - triggered before a Write tool execution in Cursor.
On the first Write of a prompt cycle, denies the operation to inject cached
security policies into the agent's context. Subsequent writes are allowed.`,
	Run: runPreTool,
}

func init() {
	rootCmd.AddCommand(preToolCmd)
}

// allowResponse is the standard allow response for preToolUse.
var allowResponse = map[string]interface{}{"permission": "allow"}

func runPreTool(cmd *cobra.Command, args []string) {
	start := time.Now()

	input, err := readStdinJSON()
	if err != nil {
		outputJSON(allowResponse)
		return
	}

	sessionID := resolveSessionID(input, platformFlag)
	var logger *logging.Logger
	if sessionID != "" {
		logger = logging.NewSessionLogger("pre-tool", platformFlag, sessionID)
	} else {
		logger = logging.NewLoggerForPlatform("pre-tool", platformFlag)
	}

	// Short-circuit: SbD disabled → no-op
	if !config.IsSecureByDesignEnabled(platformFlag) {
		logger.Debug("Secure by design is disabled, allowing")
		outputJSON(allowResponse)
		return
	}

	if sessionID == "" {
		logger.Debug("No session ID, allowing")
		outputJSON(allowResponse)
		return
	}

	// Read generation_id from input to validate against stored state
	inputGenerationID, _ := input["generation_id"].(string)

	// Check SbD state for this conversation
	st := state.NewSessionState(sessionID, platformFlag)
	policies, storedGenerationID, injected := st.GetSbdState()

	// No policies cached → no-op
	if policies == "" {
		logger.Debug("No policies cached, allowing")
		outputJSON(allowResponse)
		return
	}

	// Generation mismatch → stale state, allow through
	if inputGenerationID != "" && storedGenerationID != "" && inputGenerationID != storedGenerationID {
		logger.Debug("Generation ID mismatch, allowing Write (stale state)",
			"input_generation_id", inputGenerationID,
			"stored_generation_id", storedGenerationID)
		outputJSON(allowResponse)
		return
	}

	// Already injected for this generation → allow
	if injected {
		logger.Debug("Policy already injected, allowing Write",
			"generation_id", storedGenerationID)
		outputJSON(allowResponse)
		return
	}

	// First Write of this generation: deny once to inject policy context
	st.MarkSbdInjected()

	message := policies + "You must comply with these policies. Retry your file write with policy-compliant code, or inform the user if the requested change would violate a policy."

	elapsed := time.Since(start)
	logger.Info("Policy gate: denying Write for context injection",
		"generation_id", storedGenerationID,
		"policy_length", len(policies),
		"duration_ms", elapsed.Milliseconds())

	outputJSON(map[string]interface{}{
		"permission":    "deny",
		"user_message":  message,
		"agent_message": message,
	})
}
