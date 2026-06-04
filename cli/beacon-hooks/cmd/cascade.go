package cmd

import (
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/diff"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/logging"
)

func cascadeActionName(input map[string]interface{}) string {
	return getFirstStr(input, "agent_action_name", "hook_event_name", "hookEventName")
}

func cascadeExecutionID(input map[string]interface{}) string {
	return getFirstStr(input, "execution_id", "executionId")
}

func cascadeToolInfo(input map[string]interface{}) map[string]interface{} {
	if info, ok := input["tool_info"].(map[string]interface{}); ok {
		return info
	}
	if info, ok := input["toolInfo"].(map[string]interface{}); ok {
		return info
	}
	return nil
}

func cascadePrompt(input map[string]interface{}) string {
	if info := cascadeToolInfo(input); info != nil {
		if prompt := firstToolString(info, "user_prompt", "userPrompt", "prompt", "text"); prompt != "" {
			return prompt
		}
	}
	return getFirstStr(input, "user_prompt", "userPrompt", "prompt", "text")
}

func cascadeToolName(input map[string]interface{}) string {
	if info := cascadeToolInfo(input); info != nil {
		if name := firstToolString(info, "tool_name", "toolName", "name", "command_name"); name != "" {
			return name
		}
	}
	switch cascadeActionName(input) {
	case "post_run_command", "pre_run_command":
		return "run_command"
	case "post_mcp_tool_use", "pre_mcp_tool_use":
		return "mcp_tool"
	case "post_read_code", "pre_read_code":
		return "read_code"
	case "post_write_code", "pre_write_code":
		return "write_code"
	case "pre_user_prompt":
		return "user_prompt"
	default:
		return cascadeActionName(input)
	}
}

func cascadeMetadataFields(sessionID string, input map[string]interface{}) map[string]interface{} {
	fields := sessionFields(sessionID, input)
	if executionID := cascadeExecutionID(input); executionID != "" {
		fields["session"] = mergeNested(fields["session"], map[string]interface{}{"execution_id": executionID})
	}
	fields["raw"] = mergeNested(fields["raw"], map[string]interface{}{"cascade": input})
	return fields
}

func cascadeToolEventFields(input map[string]interface{}) map[string]interface{} {
	sessionID := resolveSessionID(input, platformFlag)
	fields := cascadeMetadataFields(sessionID, input)
	toolName := cascadeToolName(input)
	toolInfo := cascadeToolInfo(input)
	for key, value := range toolFields(toolName, toolInfo) {
		fields[key] = value
	}
	if action := cascadeActionName(input); action != "" {
		fields["tool"] = mergeNested(fields["tool"], map[string]interface{}{"action": action})
	}
	if executionID := cascadeExecutionID(input); executionID != "" {
		fields["tool"] = mergeNested(fields["tool"], map[string]interface{}{"execution_id": executionID})
	}
	return fields
}

func emitCascadePostToolObserved(logger *logging.Logger, input map[string]interface{}) {
	actionName := cascadeActionName(input)
	action, category, message := cascadeEventClassification(actionName, cascadeToolName(input))
	emitHookEvent(logger, action, category, "info", message, input, cascadeToolEventFields(input))
}

func cascadeEventClassification(actionName, toolName string) (action, category, message string) {
	switch actionName {
	case "post_run_command":
		return "command.executed", "command", "Shell command executed"
	case "post_mcp_tool_use":
		return "mcp.tool_invoked", "mcp", "MCP tool invocation observed"
	case "post_read_code":
		return "file.read", "file", "File read observed"
	case "post_write_code":
		return "file.modified", "file", "File edit observed"
	}
	if strings.Contains(strings.ToLower(toolName), "mcp") {
		return "mcp.tool_invoked", "mcp", "MCP tool invocation observed"
	}
	return actionForTool(actionName, toolName), "tool", "Tool execution observed"
}

func parseCascadeWriteInput(input map[string]interface{}, logger *logging.Logger) *evaluationParams {
	if cascadeActionName(input) != "post_write_code" {
		return nil
	}
	toolInfo := cascadeToolInfo(input)
	if toolInfo == nil {
		return nil
	}
	filePath := firstToolString(toolInfo, "file_path", "filePath", "path")
	filePath = diff.NormalizePath(filePath)
	if filePath == "" || !config.IsScannableFile(filePath) {
		return nil
	}
	edits, _ := toolInfo["edits"].([]interface{})
	if len(edits) == 0 {
		logger.Debug("No Cascade edits in input, skipping", "file_path", filePath)
		return nil
	}
	diffStr := diff.FromCursorEdits(filePath, edits)
	if diffStr == "" {
		logger.Debug("Could not construct diff from Cascade edits, skipping", "file_path", filePath)
		return nil
	}
	return &evaluationParams{
		sessionID:   resolveSessionID(input, platformFlag),
		toolName:    cascadeToolName(input),
		filePath:    filePath,
		diffStr:     diffStr,
		extraFields: cascadeMetadataFields(resolveSessionID(input, platformFlag), input),
	}
}
