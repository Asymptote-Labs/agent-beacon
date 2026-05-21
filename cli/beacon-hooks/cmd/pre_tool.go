package cmd

import (
	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/logging"
)

var preToolCmd = &cobra.Command{
	Use:   "pre-tool",
	Short: "Observe pre-tool events for local endpoint telemetry",
	Long: `PreToolUse hook - triggered before a Write tool execution in Cursor.
Records local telemetry for the tool request and allows the runtime to continue.`,
	Run: runPreTool,
}

func init() {
	rootCmd.AddCommand(preToolCmd)
}

// allowResponse is the standard allow response for preToolUse.
var allowResponse = map[string]interface{}{"permission": "allow"}

func runPreTool(cmd *cobra.Command, args []string) {
	input, err := readStdinJSON()
	if err != nil {
		outputJSON(preToolResponse())
		return
	}

	sessionID := resolveSessionID(input, platformFlag)
	var logger *logging.Logger
	if sessionID != "" {
		logger = logging.NewSessionLogger("pre-tool", platformFlag, sessionID)
	} else {
		logger = logging.NewLoggerForPlatform("pre-tool", platformFlag)
	}

	logger.Debug("Pre-tool observed")
	if platformFlag == "devin" || platformFlag == "grok" {
		emitPreToolObserved(logger, input, sessionID)
	} else {
		emitPreToolDecision(logger, input, sessionID, "approval.allowed", "allow", "Pre-tool observed")
	}
	outputJSON(preToolResponse())
}

func emitPreToolDecision(logger *logging.Logger, input map[string]interface{}, sessionID, action, decision, reason string) {
	toolName := getFirstStr(input, "tool_name", "toolName")
	toolInput := resolveToolInput(input)
	fields := sessionFields(sessionID, input)
	for key, value := range toolFields(toolName, toolInput) {
		fields[key] = value
	}
	fields["approval"] = map[string]interface{}{
		"required": true,
		"decision": decision,
		"reason":   reason,
	}
	emitHookEvent(logger, action, "approval", "info", reason, input, fields)
}

func preToolResponse() map[string]interface{} {
	if platformFlag == "grok" {
		return map[string]interface{}{"decision": "allow"}
	}
	if platformFlag == "devin" {
		return emptyResponse
	}
	return allowResponse
}

func emitPreToolObserved(logger *logging.Logger, input map[string]interface{}, sessionID string) {
	toolName := getFirstStr(input, "tool_name", "toolName")
	toolInput := resolveToolInput(input)
	fields := sessionFields(sessionID, input)
	for key, value := range toolFields(toolName, toolInput) {
		fields[key] = value
	}
	emitHookEvent(logger, "tool.invoked", "tool", "info", "Tool invocation observed", input, fields)
}
