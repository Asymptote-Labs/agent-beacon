package cmd

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	hookdiff "github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/diff"
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
	for _, event := range opencodeEndpointEvents(input, sessionID) {
		if event.action == "" {
			continue
		}
		_ = logger.EndpointEvent(event.action, event.category, event.severity, event.message, event.fields)
	}
	outputJSON(emptyResponse)
}

type opencodeNormalizedEvent struct {
	action   string
	category string
	severity string
	message  string
	fields   map[string]interface{}
}

func opencodeEndpointEvent(input map[string]interface{}, sessionID string) (string, string, string, string, map[string]interface{}) {
	events := opencodeEndpointEvents(input, sessionID)
	if len(events) == 0 {
		return "", "", "", "", nil
	}
	event := events[0]
	return event.action, event.category, event.severity, event.message, event.fields
}

func opencodeEndpointEvents(input map[string]interface{}, sessionID string) []opencodeNormalizedEvent {
	eventType := getFirstStr(input, "type", "event_type", "hook")
	fields := opencodeBaseFields(input, sessionID)
	one := func(action, category, severity, message string, values map[string]interface{}) []opencodeNormalizedEvent {
		return []opencodeNormalizedEvent{{action: action, category: category, severity: severity, message: message, fields: values}}
	}

	switch eventType {
	case "chat.message":
		if prompt := opencodePromptText(input); prompt != "" {
			fields["prompt"] = map[string]interface{}{"text": prompt}
			fields["gen_ai"] = map[string]interface{}{
				"input": map[string]interface{}{
					"messages": []interface{}{map[string]interface{}{
						"role":  "user",
						"parts": []interface{}{map[string]interface{}{"type": "text", "content": prompt}},
					}},
				},
			}
			fields["content"] = retainedContentFields(prompt)
		}
		return one("prompt.submitted", "prompt", "info", "Prompt submitted to opencode", fields)
	case "tool.execute.before":
		mergeMap(fields, opencodeToolFields(input, false))
		return one("tool.invoked", "tool", "info", "opencode tool invoked", fields)
	case "tool.execute.after":
		mergeMap(fields, opencodeToolFields(input, true))
		action, category := opencodeToolAction(input)
		if action == "file.modified" {
			if _, ok := fields["file"]; !ok {
				action, category = "tool.completed", "tool"
			}
		}
		return one(action, category, "info", opencodeToolMessage(action), fields)
	case "message.updated":
		info := opencodeMap(input, "message_info", "info")
		if len(info) == 0 {
			info = opencodeProperties(input)
			info = opencodeMap(info, "info")
		}
		if getFirstStr(info, "role") != "assistant" {
			return nil
		}
		mergeMap(fields, opencodeAssistantFields(info))
		severity := "info"
		if opencodeMap(info, "error") != nil {
			severity = "high"
		}
		return one("agent.response.completed", "session", severity, "opencode assistant response completed", fields)
	case "message.part.updated":
		part := opencodeMap(input, "part")
		if len(part) == 0 {
			part = opencodeMap(opencodeProperties(input), "part")
		}
		return opencodePartEvents(fields, part)
	case "session.created":
		return one("session.started", "session", "info", "opencode session started", fields)
	case "session.status":
		status := opencodeMap(opencodeProperties(input), "status")
		statusType := getFirstStr(status, "type")
		switch statusType {
		case "idle":
			return one("session.ended", "session", "info", "opencode session idle", fields)
		case "retry":
			fields["error"] = map[string]interface{}{"type": "retry"}
			return one("model.retry", "session", "medium", "opencode model retry", fields)
		default:
			return one("session.status", "session", "info", "opencode session "+firstNonEmpty(statusType, "status updated"), fields)
		}
	case "session.idle":
		return one("session.ended", "session", "info", "opencode session ended", fields)
	case "session.error":
		errorInfo := opencodeMap(opencodeProperties(input), "error")
		if len(errorInfo) > 0 {
			fields["error"] = map[string]interface{}{"type": firstNonEmpty(getFirstStr(errorInfo, "name", "type"), "session_error")}
		}
		return one("session.error", "session", "high", "opencode session error", fields)
	case "session.diff":
		return opencodeDiffEvents(input, fields)
	case "file.edited", "file.watcher.updated":
		properties := opencodeProperties(input)
		path := getFirstStr(properties, "file", "path", "file_path", "filePath")
		if path == "" || sessionID == "" {
			return nil
		}
		operation := "modify"
		if getFirstStr(properties, "event") == "unlink" {
			operation = "delete"
		}
		fields["file"] = map[string]interface{}{
			"path":      path,
			"operation": operation,
			"language":  strings.TrimPrefix(filepath.Ext(path), "."),
		}
		return one("file.modified", "file", "info", "opencode file change observed", fields)
	case "command.execute.before":
		if command := opencodeCommand(input); command != "" {
			fields["command"] = map[string]interface{}{"command": command}
			fields["tool"] = map[string]interface{}{"name": "command", "command": command}
		}
		return one("command.invoked", "command", "info", "opencode command invoked", fields)
	case "command.executed":
		if command := opencodeCommand(input); command != "" {
			fields["command"] = map[string]interface{}{"command": command}
			fields["tool"] = map[string]interface{}{"name": "command", "command": command}
		}
		return one("command.executed", "command", "info", "opencode command executed", fields)
	case "permission.asked", "permission.v2.asked":
		properties := opencodeProperties(input)
		fields["approval"] = map[string]interface{}{"required": true, "decision": "requested"}
		if permission := getFirstStr(properties, "permission"); permission != "" {
			fields["approval"].(map[string]interface{})["reason"] = permission
		}
		if tool := opencodeToolName(input); tool != "" {
			fields["tool"] = map[string]interface{}{"name": tool}
		}
		mergeMap(fields, opencodePermissionCorrelation(properties))
		return one("approval.requested", "approval", "info", "opencode permission requested", fields)
	case "permission.replied", "permission.updated", "permission.v2.replied":
		properties := opencodeProperties(input)
		decision := opencodeDecision(input)
		if decision == "" {
			decision = "unknown"
		}
		fields["approval"] = map[string]interface{}{"required": true, "decision": decision}
		if tool := opencodeToolName(input); tool != "" {
			fields["tool"] = map[string]interface{}{"name": tool}
		}
		mergeMap(fields, opencodePermissionCorrelation(properties))
		action := opencodeApprovalAction(decision)
		return one(action, "approval", "info", "opencode permission "+decision, fields)
	default:
		return nil
	}
}

func supportedOpenCodeEventTypes() []string {
	return []string{
		"chat.message",
		"command.execute.before",
		"command.executed",
		"file.edited",
		"file.watcher.updated",
		"message.part.delta",
		"message.part.updated",
		"message.updated",
		"permission.asked",
		"permission.replied",
		"permission.updated",
		"permission.v2.asked",
		"permission.v2.replied",
		"session.created",
		"session.diff",
		"session.error",
		"session.idle",
		"session.status",
		"tool.execute.after",
		"tool.execute.before",
	}
}

func opencodeBaseFields(input map[string]interface{}, sessionID string) map[string]interface{} {
	fields := sessionFields(sessionID, input)
	applyWorkspaceFields(fields, input, "")
	fields["raw"] = map[string]interface{}{"opencode": input}
	if model := opencodeModel(input); model != "" {
		fields["model"] = model
	}
	return fields
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
	if provider, model := getFirstStr(input, "providerID", "provider_id"), getFirstStr(input, "modelID", "model_id"); provider != "" && model != "" {
		return provider + "/" + model
	}
	model, _ := input["model_info"].(map[string]interface{})
	if model == nil {
		model, _ = input["modelInfo"].(map[string]interface{})
	}
	if model == nil {
		model = opencodeMap(opencodeProperties(input), "info")
	}
	provider := getFirstStr(model, "providerID", "provider_id", "provider")
	name := getFirstStr(model, "modelID", "model_id")
	if name == "" {
		nested := opencodeMap(model, "model")
		provider = firstNonEmpty(provider, getFirstStr(nested, "providerID", "provider_id", "provider"))
		name = getFirstStr(nested, "modelID", "model_id", "id", "name")
	}
	if provider != "" && name != "" {
		return provider + "/" + name
	}
	return name
}

func opencodeToolFields(input map[string]interface{}, completed bool) map[string]interface{} {
	toolName := opencodeToolName(input)
	toolInput := opencodeMap(input, "tool_input", "toolInput")
	toolResponse := opencodeMap(input, "tool_response", "toolResponse")
	fields := toolFieldsWithResponse(toolName, toolInput, toolResponse)
	callID := getFirstStr(input, "call_id", "callID")
	call := map[string]interface{}{}
	if len(toolInput) > 0 {
		call["arguments"] = toolInput
	}
	if completed && len(toolResponse) > 0 {
		if output, ok := toolResponse["output"]; ok {
			call["result"] = output
		} else {
			call["result"] = toolResponse
		}
	}
	if callID != "" {
		call["id"] = callID
	}
	genAI := map[string]interface{}{
		"operation": map[string]interface{}{"name": "execute_tool"},
		"tool": map[string]interface{}{
			"name": toolName,
			"call": call,
		},
	}
	fields["gen_ai"] = genAI
	if duration, ok := firstToolIntAcross([]map[string]interface{}{input}, "duration_ms", "durationMs"); ok {
		fields["tool"] = mergeNested(fields["tool"], map[string]interface{}{"duration_ms": duration})
	}

	if path := opencodeToolPath(toolInput); path != "" {
		operation := fileOperation(toolName)
		if operation == "" {
			lower := strings.ToLower(toolName)
			if strings.Contains(lower, "glob") || strings.Contains(lower, "grep") || strings.Contains(lower, "search") {
				operation = "read"
			}
		}
		fields["file"] = map[string]interface{}{
			"path":      path,
			"operation": operation,
			"language":  strings.TrimPrefix(filepath.Ext(path), "."),
		}
		fields["tool"] = mergeNested(fields["tool"], map[string]interface{}{"name": toolName, "path": path})
	}

	if completed {
		if action, _ := opencodeToolAction(input); action == "file.modified" {
			path := opencodeToolPath(toolInput)
			diffText := hookdiff.FromToolResponse(toolName, toolInput, toolResponse)
			if diffText == "" {
				diffText = firstToolString(toolInput, "content", "new_string", "newString")
			}
			if path != "" {
				mergeMap(fields, diffFields(path, diffText))
				file := fields["file"].(map[string]interface{})
				file["operation"] = fileOperation(toolName)
			}
		}
		if action, _ := opencodeToolAction(input); action == "command.executed" {
			command := firstToolString(toolInput, "command", "cmd")
			commandFields := map[string]interface{}{"command": command}
			if output, ok := toolResponse["output"]; ok {
				commandFields["output"] = output
			}
			if duration, ok := firstToolIntAcross([]map[string]interface{}{input}, "duration_ms", "durationMs"); ok {
				commandFields["duration_ms"] = duration
			}
			if metadata := opencodeMap(toolResponse, "metadata"); len(metadata) > 0 {
				if exitCode, ok := firstToolIntAcross([]map[string]interface{}{metadata}, "exit_code", "exitCode", "status"); ok {
					commandFields["exit_code"] = exitCode
				}
			}
			fields["command"] = commandFields
		}
	}
	if encoded, err := json.Marshal(map[string]interface{}{"input": toolInput, "response": toolResponse}); err == nil && len(encoded) > 0 {
		fields["content"] = retainedContentFields(string(encoded))
	}
	return fields
}

func opencodeCommand(input map[string]interface{}) string {
	if command := getFirstStr(input, "command", "cmd", "command_name"); command != "" {
		if args := getFirstStr(input, "arguments"); args != "" {
			return strings.TrimSpace(command + " " + args)
		}
		return command
	}
	properties := opencodeProperties(input)
	command := getFirstStr(properties, "command", "cmd", "name")
	if args := getFirstStr(properties, "arguments"); args != "" {
		command = strings.TrimSpace(command + " " + args)
	}
	return command
}

func opencodeToolName(input map[string]interface{}) string {
	if tool := getFirstStr(input, "tool", "tool_name", "toolName"); tool != "" {
		return tool
	}
	properties := opencodeProperties(input)
	if tool := getFirstStr(properties, "tool", "tool_name", "toolName", "permission"); tool != "" {
		return tool
	}
	part := opencodeMap(input, "part")
	return getFirstStr(part, "tool")
}

func opencodeDecision(input map[string]interface{}) string {
	if decision := getFirstStr(input, "decision", "status", "reply"); decision != "" {
		return decision
	}
	properties := opencodeProperties(input)
	return getFirstStr(properties, "decision", "status", "reply")
}

func opencodeToolAction(input map[string]interface{}) (string, string) {
	name := strings.ToLower(opencodeToolName(input))
	toolInput := opencodeMap(input, "tool_input", "toolInput")
	switch {
	case strings.Contains(name, "mcp") || strings.HasPrefix(name, "list_mcp_") || strings.HasPrefix(name, "read_mcp_"):
		return "mcp.tool_invoked", "mcp"
	case name == "bash" || strings.Contains(name, "shell") || strings.Contains(name, "terminal"):
		return "command.executed", "command"
	case strings.Contains(name, "read") ||
		((strings.Contains(name, "glob") || strings.Contains(name, "grep") || strings.Contains(name, "search")) && opencodeToolPath(toolInput) != ""):
		return "file.read", "file"
	case strings.Contains(name, "edit") || strings.Contains(name, "write") || strings.Contains(name, "patch"):
		return "file.modified", "file"
	default:
		return "tool.completed", "tool"
	}
}

func opencodeToolMessage(action string) string {
	switch action {
	case "command.executed":
		return "opencode shell command executed"
	case "file.read":
		return "opencode file read"
	case "file.modified":
		return "opencode file modified"
	case "mcp.tool_invoked":
		return "opencode MCP tool executed"
	default:
		return "opencode tool completed"
	}
}

func opencodeToolPath(input map[string]interface{}) string {
	path := firstToolString(input, "file_path", "filePath", "path", "target", "destination")
	if path == "" {
		path = firstToolString(input, "pattern")
	}
	return hookdiff.NormalizePath(path)
}

func opencodeAssistantFields(info map[string]interface{}) map[string]interface{} {
	fields := map[string]interface{}{}
	if model := opencodeModel(info); model != "" {
		fields["model"] = model
	}
	genAI := map[string]interface{}{}
	response := map[string]interface{}{}
	if finish := getFirstStr(info, "finish"); finish != "" {
		response["finish_reasons"] = []interface{}{finish}
	}
	if id := getFirstStr(info, "id"); id != "" {
		response["id"] = id
	}
	if len(response) > 0 {
		genAI["response"] = response
	}
	if usage := opencodeUsage(info); len(usage) > 0 {
		genAI["usage"] = usage
	}
	if len(genAI) > 0 {
		fields["gen_ai"] = genAI
	}
	if errInfo := opencodeMap(info, "error"); len(errInfo) > 0 {
		fields["error"] = map[string]interface{}{"type": firstNonEmpty(getFirstStr(errInfo, "name", "type"), "assistant_error")}
	}
	return fields
}

func opencodeUsage(input map[string]interface{}) map[string]interface{} {
	tokens := opencodeMap(input, "tokens")
	if len(tokens) == 0 {
		return nil
	}
	usage := map[string]interface{}{}
	if value, ok := opencodeInt(tokens["input"]); ok {
		usage["input_tokens"] = value
	}
	if value, ok := opencodeInt(tokens["output"]); ok {
		usage["output_tokens"] = value
	}
	if value, ok := opencodeInt(tokens["reasoning"]); ok {
		usage["reasoning"] = map[string]interface{}{"output_tokens": value}
	}
	cache := opencodeMap(tokens, "cache")
	if value, ok := opencodeInt(cache["read"]); ok {
		usage["cache_read"] = map[string]interface{}{"input_tokens": value}
	}
	if value, ok := opencodeInt(cache["write"]); ok {
		usage["cache_creation"] = map[string]interface{}{"input_tokens": value}
	}
	if value, ok := opencodeFloat(input["cost"]); ok {
		usage["cost_usd"] = value
	}
	return usage
}

func opencodePartEvents(base map[string]interface{}, part map[string]interface{}) []opencodeNormalizedEvent {
	if len(part) == 0 {
		return nil
	}
	fields := cloneOpenCodeFields(base)
	partType := getFirstStr(part, "type")
	switch partType {
	case "text", "reasoning":
		text := getFirstStr(part, "text")
		if text == "" {
			return nil
		}
		fields["gen_ai"] = map[string]interface{}{
			"output": map[string]interface{}{
				"messages": []interface{}{map[string]interface{}{
					"role":  "assistant",
					"parts": []interface{}{map[string]interface{}{"type": partType, "content": text}},
				}},
			},
		}
		fields["content"] = retainedContentFields(text)
		action, message := "agent.response", "opencode assistant response captured"
		if partType == "reasoning" {
			action, message = "agent.reasoning", "opencode agent reasoning captured"
		}
		return []opencodeNormalizedEvent{{action: action, category: "session", severity: "info", message: message, fields: fields}}
	case "tool":
		state := opencodeMap(part, "state")
		status := getFirstStr(state, "status")
		if status != "completed" && status != "error" {
			return nil
		}
		toolInput := opencodeMap(state, "input")
		response := map[string]interface{}{}
		if output, ok := state["output"]; ok {
			response["output"] = output
		}
		if metadata := opencodeMap(state, "metadata"); len(metadata) > 0 {
			response["metadata"] = metadata
		}
		toolPayload := map[string]interface{}{
			"type":          "tool.execute.after",
			"session_id":    getFirstStr(part, "sessionID", "session_id"),
			"tool_name":     getFirstStr(part, "tool"),
			"call_id":       getFirstStr(part, "callID", "call_id"),
			"tool_input":    toolInput,
			"tool_response": response,
		}
		if start, ok := opencodeInt(opencodeMap(state, "time")["start"]); ok {
			if end, ok := opencodeInt(opencodeMap(state, "time")["end"]); ok && end >= start {
				toolPayload["duration_ms"] = end - start
			}
		}
		mergeMap(fields, opencodeToolFields(toolPayload, true))
		if status == "error" {
			errText := getFirstStr(state, "error")
			fields["error"] = map[string]interface{}{"type": "tool_error"}
			if errText != "" {
				fields["raw"] = mergeNested(fields["raw"], map[string]interface{}{"opencode_tool_error": errText})
				fields["content"] = retainedContentFields(errText)
			}
			return []opencodeNormalizedEvent{{action: "tool.failed", category: "tool", severity: "high", message: "opencode tool failed", fields: fields}}
		}
		action, category := opencodeToolAction(toolPayload)
		if action == "file.modified" {
			if _, ok := fields["file"]; !ok {
				action, category = "tool.completed", "tool"
			}
		}
		return []opencodeNormalizedEvent{{action: action, category: category, severity: "info", message: opencodeToolMessage(action), fields: fields}}
	case "retry":
		fields["error"] = map[string]interface{}{"type": "model_retry"}
		return []opencodeNormalizedEvent{{action: "model.retry", category: "session", severity: "medium", message: "opencode model retry", fields: fields}}
	default:
		return nil
	}
}

func opencodeDiffEvents(input map[string]interface{}, base map[string]interface{}) []opencodeNormalizedEvent {
	properties := opencodeProperties(input)
	rawDiff, ok := properties["diff"]
	if !ok {
		rawDiff = input["diff"]
	}
	items, ok := rawDiff.([]interface{})
	if !ok || len(items) == 0 {
		return nil
	}
	var events []opencodeNormalizedEvent
	for _, item := range items {
		diffItem, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		path := getFirstStr(diffItem, "file", "path", "file_path", "filePath")
		before, after := getFirstStr(diffItem, "before"), getFirstStr(diffItem, "after")
		additions, _ := opencodeInt(diffItem["additions"])
		deletions, _ := opencodeInt(diffItem["deletions"])
		if path == "" || (before == after && additions == 0 && deletions == 0) {
			continue
		}
		diffText := ""
		if before != "" || after != "" {
			diffText = fmt.Sprintf("--- before\n%s\n+++ after\n%s", before, after)
		}
		fields := cloneOpenCodeFields(base)
		mergeMap(fields, diffFields(path, diffText))
		events = append(events, opencodeNormalizedEvent{
			action: "file.modified", category: "file", severity: "info",
			message: "opencode session diff observed", fields: fields,
		})
	}
	return events
}

func opencodeApprovalAction(decision string) string {
	switch strings.ToLower(strings.TrimSpace(decision)) {
	case "once", "always", "accepted", "accept", "allow", "allowed", "approve", "approved":
		return "approval.allowed"
	case "reject", "rejected", "deny", "denied", "timeout":
		return "approval.denied"
	default:
		return "approval.requested"
	}
}

func opencodePermissionCorrelation(properties map[string]interface{}) map[string]interface{} {
	toolRef := opencodeMap(properties, "tool")
	callID := getFirstStr(toolRef, "callID", "call_id")
	if callID == "" {
		return nil
	}
	toolName := getFirstStr(properties, "permission")
	fields := map[string]interface{}{
		"gen_ai": map[string]interface{}{
			"operation": map[string]interface{}{"name": "execute_tool"},
			"tool": map[string]interface{}{
				"name": toolName,
				"call": map[string]interface{}{"id": callID},
			},
		},
	}
	if toolName != "" {
		fields["tool"] = map[string]interface{}{"name": toolName}
	}
	return fields
}

func opencodeProperties(input map[string]interface{}) map[string]interface{} {
	if properties := opencodeMap(input, "properties", "data"); len(properties) > 0 {
		return properties
	}
	event := opencodeMap(input, "event")
	return opencodeMap(event, "properties", "data")
}

func opencodeMap(input map[string]interface{}, keys ...string) map[string]interface{} {
	if input == nil {
		return nil
	}
	for _, key := range keys {
		if value, ok := input[key].(map[string]interface{}); ok {
			return value
		}
	}
	return nil
}

func opencodeInt(value interface{}) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		result, err := typed.Int64()
		return int(result), err == nil
	case string:
		result, err := strconv.Atoi(strings.TrimSpace(typed))
		return result, err == nil
	default:
		return 0, false
	}
}

func opencodeFloat(value interface{}) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case int:
		return float64(typed), true
	case json.Number:
		result, err := typed.Float64()
		return result, err == nil
	case string:
		result, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return result, err == nil
	default:
		return 0, false
	}
}

func cloneOpenCodeFields(input map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
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
