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
The public Beacon build writes local endpoint telemetry using the configured content retention mode.`,
	Run: runPostTool,
}

func init() {
	rootCmd.AddCommand(postToolCmd)
}

// evaluationParams holds the platform-independent fields needed for local hook handling.
type evaluationParams struct {
	sessionID   string
	toolName    string
	filePath    string
	diffStr     string
	extraFields map[string]interface{}
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

	if isCascadePlatform(platformFlag) {
		params = parseCascadeWriteInput(input, logger)
		if params == nil {
			emitCascadePostToolObserved(logger, input)
			outputJSON(emptyResponse)
			return
		}
	} else if platformFlag == "cursor" {
		// Cursor fires two hook types through post-tool:
		//   - afterFileEdit: has "edits" array and top-level "file_path" (no output supported)
		//   - postToolUse: has "tool_name" and "tool_input" (supports additional_context/followup via stop)
		// We use hook_event_name (present in all Cursor hook inputs) to distinguish them.
		hookEvent, _ := input["hook_event_name"].(string)
		if hookEvent != "afterFileEdit" {
			emitPostToolObserved(logger, input)
			outputJSON(emptyResponse)
			return
		}
		// afterFileEdit exposes file-edit metadata and diffs; retention controls raw diff inclusion.
		params = parseCursorInput(input, logger)
	} else {
		params = parseClaudeCopilotInput(input, logger)
	}

	if params == nil {
		if platformFlag != "cursor" {
			emitPostToolObserved(logger, input)
		}
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

// parseClaudeCopilotInput extracts evaluation params from Claude/Copilot-compatible PostToolUse format.
func parseClaudeCopilotInput(input map[string]interface{}, logger *logging.Logger) *evaluationParams {
	var sessionID, toolName string
	var toolInput, toolResponse map[string]interface{}

	if platformFlag == "antigravity" || platformFlag == "copilot" || isDevinLikePlatform(platformFlag) || platformFlag == "grok" || platformFlag == "hermes" || platformFlag == "vscode" {
		sessionID = resolveSessionID(input, platformFlag)
		toolName = getFirstStr(input, "toolName", "tool_name")
		if platformFlag == "antigravity" {
			toolName = antigravityToolName(input)
		}
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

	if platformFlag == "antigravity" && getFirstStr(input, "error") != "" {
		return nil
	}

	filePath := diff.GetStringFromMaps("file_path", toolInput, toolResponse)
	if filePath == "" {
		filePath = diff.GetStringFromMaps("filePath", toolInput, toolResponse)
	}
	if filePath == "" {
		filePath = diff.GetStringFromMaps("path", toolInput, toolResponse)
	}
	if filePath == "" {
		filePath = diff.GetStringFromMaps("Path", toolInput, toolResponse)
	}
	if filePath == "" {
		filePath = diff.GetStringFromMaps("AbsolutePath", toolInput, toolResponse)
	}
	filePath = diff.NormalizePath(filePath)

	if (sessionID == "" && !isDevinLikePlatform(platformFlag)) || toolName == "" || filePath == "" {
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
	for key, value := range params.extraFields {
		fields[key] = value
	}
	for key, value := range diffFields(params.filePath, params.diffStr) {
		fields[key] = value
	}
	fields["tool"] = mergeNested(fields["tool"], map[string]interface{}{"name": params.toolName, "path": params.filePath})
	if params.sessionID != "" {
		fields["session"] = mergeNested(fields["session"], map[string]interface{}{"id": params.sessionID})
	}
	logger.EndpointEvent("file.modified", "file", "info", "File edit observed", fields)
}

// resolveToolInput extracts tool input from Copilot's various formats.
func resolveToolInput(input map[string]interface{}) map[string]interface{} {
	if toolCall, ok := input["toolCall"].(map[string]interface{}); ok {
		if args, ok := toolCall["args"].(map[string]interface{}); ok {
			return args
		}
	}
	if m, ok := input["tool_input"].(map[string]interface{}); ok {
		return m
	}
	if m, ok := input["toolInput"].(map[string]interface{}); ok {
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
	if platformFlag == "antigravity" {
		response := map[string]interface{}{}
		if errMsg := getFirstStr(input, "error"); errMsg != "" {
			response["error"] = errMsg
		}
		if result, ok := input["toolResult"]; ok {
			response["result"] = result
		}
		if result, ok := input["toolResult"].(map[string]interface{}); ok {
			for key, value := range result {
				response[key] = value
			}
		}
		if len(response) > 0 {
			return response
		}
	}
	if m, ok := input["tool_response"].(map[string]interface{}); ok {
		return m
	}
	if m, ok := input["toolResponse"].(map[string]interface{}); ok {
		return m
	}
	if m, ok := input["toolResult"].(map[string]interface{}); ok {
		return m
	}
	if platformFlag == "hermes" {
		if extra := hermesExtra(input); extra != nil {
			if result, ok := extra["result"].(map[string]interface{}); ok {
				return result
			}
			if result, ok := extra["result"].(string); ok && result != "" {
				return map[string]interface{}{"result": result}
			}
		}
	}
	// If tool_response is a plain string, wrap it for downstream compatibility
	if respStr, ok := input["tool_response"].(string); ok && respStr != "" {
		return map[string]interface{}{"result": respStr}
	}
	return nil
}

func emitPostToolObserved(logger *logging.Logger, input map[string]interface{}) {
	toolName := getFirstStr(input, "tool_name", "toolName")
	if platformFlag == "antigravity" {
		toolName = antigravityToolName(input)
	}
	hookEvent := getFirstStr(input, "hook_event_name", "hookEventName")
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
	if hookEvent == "PostToolUseFailure" || hookEvent == "postToolUseFailure" || hookEvent == "post_tool_use_failure" || getFirstStr(input, "error") != "" {
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
	if platform == "copilot" || platform == "vscode" {
		lower := strings.ToLower(toolName)
		return strings.Contains(lower, "edit") ||
			strings.Contains(lower, "write") ||
			strings.Contains(lower, "create") ||
			strings.Contains(lower, "patch")
	}
	if platform == "factory" {
		return toolName == "Write" || toolName == "Edit" || toolName == "MultiEdit" || toolName == "Create"
	}
	if isDevinLikePlatform(platform) {
		lower := strings.ToLower(toolName)
		return lower == "edit" || lower == "write"
	}
	if isCascadePlatform(platform) {
		lower := strings.ToLower(toolName)
		return lower == "post_write_code" || lower == "write_code"
	}
	if platform == "grok" {
		lower := strings.ToLower(toolName)
		return lower == "search_replace" || lower == "write_file" || strings.Contains(lower, "edit") || strings.Contains(lower, "write")
	}
	if platform == "hermes" {
		lower := strings.ToLower(toolName)
		return strings.Contains(lower, "edit") ||
			strings.Contains(lower, "write") ||
			strings.Contains(lower, "create") ||
			strings.Contains(lower, "patch")
	}
	if platform == "antigravity" {
		lower := strings.ToLower(toolName)
		return strings.Contains(lower, "edit") ||
			strings.Contains(lower, "write") ||
			strings.Contains(lower, "create") ||
			strings.Contains(lower, "patch")
	}
	return toolName == "Write" || toolName == "Edit" || toolName == "MultiEdit"
}

func antigravityToolName(input map[string]interface{}) string {
	if toolCall, ok := input["toolCall"].(map[string]interface{}); ok {
		if name, ok := toolCall["name"].(string); ok {
			return name
		}
	}
	return getFirstStr(input, "tool_name", "toolName")
}
