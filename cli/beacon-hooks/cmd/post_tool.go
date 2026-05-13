package cmd

import (
	"encoding/json"
	"strings"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/diff"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/logging"
)

var postToolCmd = &cobra.Command{
	Use:   "post-tool",
	Short: "Record file-edit telemetry",
	Long: `PostToolUse hook - triggered after Write, Edit, or MultiEdit operations.
The public Beacon build records local metadata only.`,
	Run: runPostTool,
}

func init() {
	rootCmd.AddCommand(postToolCmd)
}

// evaluationParams holds the platform-independent fields needed for local hook handling.
type evaluationParams struct {
	sessionID string
	toolName  string
	filePath  string
	diffStr   string
}

func runPostTool(cmd *cobra.Command, args []string) {
	input, err := readStdinJSON()
	if err != nil {
		outputJSON(emptyResponse)
		return
	}

	// Resolve session ID early so we can use per-session logger
	sessionID := resolveSessionID(input, platformFlag)
	var logger *logging.Logger
	if sessionID != "" {
		logger = logging.NewSessionLogger("post-tool-async-scan", platformFlag, sessionID)
	} else {
		logger = logging.NewLoggerForPlatform("post-tool-async-scan", platformFlag)
	}

	var params *evaluationParams

	emitPostToolObserved(logger, input)

	if platformFlag == "cursor" {
		// Cursor fires two hook types through post-tool:
		//   - afterFileEdit: has "edits" array and top-level "file_path" (no output supported)
		//   - postToolUse: has "tool_name" and "tool_input" (supports additional_context/followup via stop)
		// We use hook_event_name (present in all Cursor hook inputs) to distinguish them.
		hookEvent, _ := input["hook_event_name"].(string)
		if hookEvent != "afterFileEdit" {
			outputJSON(emptyResponse)
			return
		}
		// afterFileEdit — records file-edit metadata only (no output supported).
		params = parseCursorInput(input, logger)
	} else {
		params = parseClaudeCopilotInput(input, logger)
	}

	if params == nil {
		outputJSON(emptyResponse)
		return
	}

	recordLocalEdit(params, logger)
	outputJSON(emptyResponse)
}

// parseCursorInput extracts evaluation params from Cursor's afterFileEdit format.
func parseCursorInput(input map[string]interface{}, logger *logging.Logger) *evaluationParams {
	sessionID := resolveSessionID(input, "cursor")
	filePath, _ := input["file_path"].(string)
	if sessionID == "" || filePath == "" {
		return nil
	}

	if !config.IsScannableFile(filePath) {
		logger.Debug("Skipping non-scannable file: " + filePath)
		return nil
	}

	// Construct diff from Cursor's edits array
	edits, _ := input["edits"].([]interface{})
	if len(edits) == 0 {
		logger.Debug("No edits in input, skipping")
		return nil
	}

	diffStr := diff.FromCursorEdits(filePath, edits)
	if diffStr == "" {
		logger.Debug("Could not construct diff from edits, skipping")
		return nil
	}

	logger.Debug("Constructed diff from cursor edits", "file_path", filePath, "num_edits", len(edits))

	return &evaluationParams{
		sessionID: sessionID,
		toolName:  "afterFileEdit",
		filePath:  filePath,
		diffStr:   diffStr,
	}
}

// parseClaudeCopilotInput extracts evaluation params from Claude/Copilot PostToolUse format.
func parseClaudeCopilotInput(input map[string]interface{}, logger *logging.Logger) *evaluationParams {
	var sessionID, toolName string
	var toolInput, toolResponse map[string]interface{}

	if platformFlag == "copilot" {
		sessionID = resolveSessionID(input, platformFlag)
		toolName = getFirstStr(input, "toolName", "tool_name")
		toolInput = resolveToolInput(input)
		toolResponse = resolveToolResponse(input)
	} else {
		sessionID, _ = input["session_id"].(string)
		toolName, _ = input["tool_name"].(string)
		toolInput, _ = input["tool_input"].(map[string]interface{})
		toolResponse, _ = input["tool_response"].(map[string]interface{})
	}

	if !isFileEditTool(platformFlag, toolName) {
		return nil
	}

	filePath := diff.GetStringFromMaps("file_path", toolInput, toolResponse)
	if filePath == "" {
		filePath = diff.GetStringFromMaps("filePath", toolInput, toolResponse)
	}

	if sessionID == "" || toolName == "" || filePath == "" {
		return nil
	}

	if !config.IsScannableFile(filePath) {
		logger.Debug("Skipping non-scannable file: " + filePath)
		return nil
	}

	logger.Debug("Constructing diff", "tool_name", toolName, "file_path", filePath,
		"has_tool_input", toolInput != nil, "has_tool_response", toolResponse != nil)

	diffStr := diff.FromToolResponse(toolName, toolInput, toolResponse)
	if diffStr == "" {
		logger.Debug("Could not construct diff, skipping", "tool_name", toolName)
		return nil
	}

	return &evaluationParams{
		sessionID: sessionID,
		toolName:  toolName,
		filePath:  filePath,
		diffStr:   diffStr,
	}
}

// recordLocalEdit logs file-edit metadata without sending code or diffs to a hosted service.
func recordLocalEdit(params *evaluationParams, logger *logging.Logger) {
	logger.Info("File edit observed", "file_path", params.filePath, "tool_name", params.toolName)
	fields := map[string]interface{}{}
	for key, value := range diffFields(params.filePath, params.diffStr) {
		fields[key] = value
	}
	fields["tool"] = map[string]interface{}{"name": params.toolName, "path": params.filePath}
	fields["session"] = map[string]interface{}{"id": params.sessionID}
	logger.EndpointEvent("file.modified", "file", "info", "File edit observed", fields)
}

// resolveToolInput extracts tool input from Copilot's various formats.
func resolveToolInput(input map[string]interface{}) map[string]interface{} {
	if m, ok := input["tool_input"].(map[string]interface{}); ok {
		return m
	}
	// Fallback: some Copilot versions send toolArgs as stringified JSON
	if argsStr, ok := input["toolArgs"].(string); ok && argsStr != "" {
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(argsStr), &parsed); err == nil {
			return parsed
		}
	}
	if m, ok := input["toolArgs"].(map[string]interface{}); ok {
		return m
	}
	return nil
}

// resolveToolResponse extracts tool response from Copilot's various formats.
func resolveToolResponse(input map[string]interface{}) map[string]interface{} {
	if m, ok := input["tool_response"].(map[string]interface{}); ok {
		return m
	}
	if m, ok := input["toolResult"].(map[string]interface{}); ok {
		return m
	}
	// If tool_response is a plain string, wrap it for downstream compatibility
	if respStr, ok := input["tool_response"].(string); ok && respStr != "" {
		return map[string]interface{}{"result": respStr}
	}
	return nil
}

func emitPostToolObserved(logger *logging.Logger, input map[string]interface{}) {
	toolName := getFirstStr(input, "tool_name", "toolName")
	hookEvent := getFirstStr(input, "hook_event_name")
	toolInput := resolveToolInput(input)
	if toolInput == nil {
		if nested, ok := input["tool_input"].(map[string]interface{}); ok {
			toolInput = nested
		}
	}
	sessionID := resolveSessionID(input, platformFlag)
	fields := sessionFields(sessionID, input)
	for key, value := range toolFields(toolName, toolInput) {
		fields[key] = value
	}
	if hookEvent == "postToolUseFailure" {
		emitHookEvent(logger, "tool.failed", "tool", "high", "Tool execution failed", input, fields)
		return
	}
	action := actionForTool(hookEvent, toolName)
	category := "tool"
	if strings.HasPrefix(action, "file.") {
		category = "file"
	} else if strings.HasPrefix(action, "command.") {
		category = "command"
	} else if strings.HasPrefix(action, "mcp.") {
		category = "mcp"
	}
	message := "Tool execution observed"
	if action == "command.executed" {
		message = "Shell command executed"
	}
	emitHookEvent(logger, action, category, "info", message, input, fields)
}

// isFileEditTool returns true if the tool name represents a file edit operation.
func isFileEditTool(platform, toolName string) bool {
	if platform == "copilot" {
		lower := strings.ToLower(toolName)
		return strings.Contains(lower, "edit") ||
			strings.Contains(lower, "write") ||
			strings.Contains(lower, "create") ||
			strings.Contains(lower, "patch")
	}
	if platform == "factory" {
		return toolName == "Write" || toolName == "Edit" || toolName == "MultiEdit" || toolName == "Create"
	}
	return toolName == "Write" || toolName == "Edit" || toolName == "MultiEdit"
}
