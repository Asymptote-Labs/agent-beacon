package cmd

import (
	"strings"

	"github.com/spf13/cobra"
)

var opencodeEventCmd = &cobra.Command{
	Use:   "opencode-event",
	Short: "Record opencode plugin telemetry",
	Long:  `opencode-event receives raw Beacon opencode plugin payloads and writes local endpoint telemetry.`,
	Run:   runOpenCodeEvent,
}

func init() {
	rootCmd.AddCommand(opencodeEventCmd)
}

func runOpenCodeEvent(cmd *cobra.Command, args []string) {
	input, err := readStdinJSON()
	if err != nil {
		outputJSON(emptyResponse)
		return
	}
	sessionID := resolveSessionID(input, "opencode")
	logger := newHookLogger("opencode-event", "opencode", sessionID)
	action, category, severity, message, fields := opencodeEndpointEvent(input, sessionID)
	if action == "" {
		outputJSON(emptyResponse)
		return
	}
	logger.EndpointEvent(action, category, severity, message, fields)
	outputJSON(emptyResponse)
}

func opencodeEndpointEvent(input map[string]interface{}, sessionID string) (string, string, string, string, map[string]interface{}) {
	eventType := getFirstStr(input, "type", "event_type", "hook")
	fields := sessionFields(sessionID, input)
	applyWorkspaceFields(fields, input, "")
	fields["raw"] = map[string]interface{}{
		"opencode": input,
	}
	if model := opencodeModel(input); model != "" {
		fields["model"] = model
	}

	switch eventType {
	case "chat.message":
		if prompt := opencodePromptText(input); prompt != "" {
			fields["prompt"] = map[string]interface{}{"text": prompt}
		}
		return "prompt.submitted", "prompt", "info", "Prompt submitted to opencode", fields
	case "session.created":
		return "session.started", "session", "info", "opencode session started", fields
	case "session.idle":
		return "session.ended", "session", "info", "opencode session ended", fields
	case "session.error":
		return "tool.failed", "tool", "high", "opencode session error", fields
	case "session.diff":
		mergeMap(fields, opencodeDiffFields(input))
		return "file.modified", "file", "info", "opencode session diff observed", fields
	case "command.executed":
		if command := opencodeCommand(input); command != "" {
			fields["command"] = map[string]interface{}{"command": command}
			fields["tool"] = map[string]interface{}{"name": "command", "command": command}
		}
		return "command.executed", "command", "info", "opencode command executed", fields
	case "permission.asked":
		fields["approval"] = map[string]interface{}{"required": true, "decision": "requested"}
		if tool := opencodeToolName(input); tool != "" {
			fields["tool"] = map[string]interface{}{"name": tool}
		}
		return "approval.requested", "approval", "info", "opencode permission requested", fields
	case "permission.replied", "permission.updated":
		decision := opencodeDecision(input)
		if decision == "" {
			decision = "unknown"
		}
		fields["approval"] = map[string]interface{}{"required": true, "decision": decision}
		if tool := opencodeToolName(input); tool != "" {
			fields["tool"] = map[string]interface{}{"name": tool}
		}
		return "approval.allowed", "approval", "info", "opencode permission " + decision, fields
	default:
		return "", "", "", "", nil
	}
}

func supportedOpenCodeEventTypes() []string {
	return []string{
		"chat.message",
		"command.executed",
		"permission.asked",
		"permission.replied",
		"permission.updated",
		"session.created",
		"session.diff",
		"session.error",
		"session.idle",
	}
}

func opencodePromptText(input map[string]interface{}) string {
	if prompt := getFirstStr(input, "prompt", "text", "user_prompt"); prompt != "" {
		return prompt
	}
	output, _ := input["output"].(map[string]interface{})
	parts, _ := output["parts"].([]interface{})
	var values []string
	for _, part := range parts {
		partMap, ok := part.(map[string]interface{})
		if !ok {
			continue
		}
		switch getFirstStr(partMap, "type") {
		case "text":
			if text := getFirstStr(partMap, "text"); text != "" {
				values = append(values, text)
			}
		case "file":
			if file := getFirstStr(partMap, "filename", "url"); file != "" {
				values = append(values, file)
			}
		case "agent", "subtask":
			if name := getFirstStr(partMap, "name", "description"); name != "" {
				values = append(values, name)
			}
		}
	}
	return strings.Join(values, "\n")
}

func opencodeModel(input map[string]interface{}) string {
	if model := getFirstStr(input, "model"); model != "" {
		return model
	}
	model, _ := input["model_info"].(map[string]interface{})
	if model == nil {
		model, _ = input["modelInfo"].(map[string]interface{})
	}
	provider := getFirstStr(model, "providerID", "provider_id", "provider")
	name := getFirstStr(model, "modelID", "model_id", "id", "name")
	if provider != "" && name != "" {
		return provider + "/" + name
	}
	return name
}

func opencodeDiffFields(input map[string]interface{}) map[string]interface{} {
	path := getFirstStr(input, "file_path", "filePath", "path")
	diff := getFirstStr(input, "diff")
	if path == "" {
		properties, _ := input["properties"].(map[string]interface{})
		path = getFirstStr(properties, "file_path", "filePath", "path")
		diff = firstNonEmpty(diff, getFirstStr(properties, "diff"))
	}
	return diffFields(path, diff)
}

func opencodeCommand(input map[string]interface{}) string {
	if command := getFirstStr(input, "command", "cmd"); command != "" {
		return command
	}
	properties, _ := input["properties"].(map[string]interface{})
	return getFirstStr(properties, "command", "cmd")
}

func opencodeToolName(input map[string]interface{}) string {
	if tool := getFirstStr(input, "tool", "tool_name", "toolName"); tool != "" {
		return tool
	}
	properties, _ := input["properties"].(map[string]interface{})
	return getFirstStr(properties, "tool", "tool_name", "toolName")
}

func opencodeDecision(input map[string]interface{}) string {
	if decision := getFirstStr(input, "decision", "status"); decision != "" {
		return decision
	}
	properties, _ := input["properties"].(map[string]interface{})
	return getFirstStr(properties, "decision", "status")
}

func mergeMap(dst, src map[string]interface{}) {
	for key, value := range src {
		dst[key] = value
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
