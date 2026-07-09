package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/cloudshuttle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/git"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/logging"
)

var runInventoryHeartbeatCommand = exec.CommandContext

func emitHookEvent(logger *logging.Logger, action, category, severity, message string, input map[string]interface{}, fields map[string]interface{}) {
	if fields == nil {
		fields = map[string]interface{}{}
	}
	seedCursorCloudRunID(input)
	if platformFlag == "grok" {
		fields["raw"] = mergeNested(fields["raw"], map[string]interface{}{"grok": input})
	}
	if platformFlag == "hermes" {
		fields["raw"] = mergeNested(fields["raw"], map[string]interface{}{"hermes": input})
	}
	if platformFlag == "vscode" {
		fields["raw"] = mergeNested(fields["raw"], map[string]interface{}{"vscode": input})
	}
	if isCascadePlatform(platformFlag) {
		fields["raw"] = mergeNested(fields["raw"], map[string]interface{}{"cascade": input})
	}
	if model := getFirstStr(input, "model"); model != "" {
		fields["model"] = model
	}
	cwd := resolveCwd(input, platformFlag)
	if cwd != "" {
		fields["session"] = mergeNested(fields["session"], map[string]interface{}{"working_directory": cwd})
		fields["repository"] = cwd
	}
	if branch := resolveBranch(input, cwd); branch != "" {
		fields["branch"] = branch
	}
	if err := logger.EndpointEvent(action, category, severity, message, fields); err != nil {
		logger.Error("Failed to write endpoint event", "error", err.Error(), "action", action)
	}
}

// resolveBranch prefers a runtime-provided branch and otherwise derives the
// checked-out branch from the event's working directory. Runtime-provided
// values pass through even when local git metadata enrichment is disabled.
func resolveBranch(input map[string]interface{}, cwd string) string {
	if branch := getFirstStr(input, "branch", "git_branch"); branch != "" {
		return branch
	}
	if cwd == "" || gitMetadataDisabled() {
		return ""
	}
	return git.CurrentBranch(cwd)
}

// applyBranchField fills fields["branch"] for emitters that write endpoint
// events without going through emitHookEvent. fallbackDir seeds branch
// resolution when the payload carries no working directory, such as a file
// edit identified only by its path.
func applyBranchField(fields map[string]interface{}, input map[string]interface{}, fallbackDir string) {
	if existing, ok := fields["branch"]; ok && existing != "" {
		return
	}
	cwd := resolveCwd(input, platformFlag)
	if cwd == "" {
		cwd = fallbackDir
	}
	if branch := resolveBranch(input, cwd); branch != "" {
		fields["branch"] = branch
	}
}

// gitMetadataDisabled accepts 1/true/yes, matching other Beacon disable flags.
func gitMetadataDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("BEACON_DISABLE_GIT_METADATA"))) {
	case "1", "true", "yes":
		return true
	}
	return false
}

func uploadCloudTelemetry(logger *logging.Logger, force bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := cloudshuttle.MaybeUpload(ctx, force); err != nil {
		logger.Warn("Cloud telemetry upload failed", "error", err.Error(), "force", force)
	}
}

func isCursorCloudMode() bool {
	return strings.TrimSpace(os.Getenv("BEACON_ORIGIN")) == "cloud" &&
		strings.TrimSpace(os.Getenv("BEACON_RUN_PROVIDER")) == "cursor_cloud"
}

func seedCursorCloudRunID(input map[string]interface{}) {
	if platformFlag != "cursor" || !isCursorCloudMode() || strings.TrimSpace(os.Getenv("BEACON_RUN_ID")) != "" {
		return
	}
	if runID := getFirstStr(input, "conversation_id", "parent_conversation_id"); runID != "" {
		_ = os.Setenv("BEACON_RUN_ID", runID)
	}
}

func maybeUploadCursorCloudTelemetry(logger *logging.Logger) {
	if platformFlag != "cursor" {
		return
	}
	if !isCursorCloudMode() {
		return
	}
	uploadCloudTelemetry(logger, true)
}

func maybeEmitInventoryHeartbeat(logger *logging.Logger, input map[string]interface{}) {
	cliPath := strings.TrimSpace(os.Getenv("BEACON_ENDPOINT_CLI"))
	if cliPath == "" {
		return
	}
	args := []string{"endpoint", "inventory", "heartbeat", "--trigger", "hook", "--trigger-harness", platformFlag}
	if configPath := strings.TrimSpace(os.Getenv("BEACON_ENDPOINT_CONFIG")); configPath != "" {
		args = append(args, "--config", configPath)
	}
	if logPath := inventoryEndpointLogPath(); logPath != "" {
		args = append(args, "--log-path", logPath)
	}
	if cwd := resolveCwd(input, platformFlag); cwd != "" {
		args = append(args, "--working-dir", cwd)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := runInventoryHeartbeatCommand(ctx, cliPath, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		logger.Warn("Inventory heartbeat failed", "error", err.Error(), "output", strings.TrimSpace(string(out)))
	}
}

func inventoryEndpointLogPath() string {
	if path := firstHookEnv("BEACON_ENDPOINT_LOG", "BEACON_CLOUD_LOG_PATH", "BEACON_LOG_PATH", "BEACON_RUNTIME_LOG"); path != "" {
		return path
	}
	if os.Getenv("BEACON_ENDPOINT_MODE") == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".beacon", "endpoint", "logs", "runtime.jsonl")
}

func firstHookEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func sessionFields(sessionID string, input map[string]interface{}) map[string]interface{} {
	fields := map[string]interface{}{}
	session := map[string]interface{}{}
	if sessionID != "" {
		session["id"] = sessionID
	}
	if cwd := resolveCwd(input, platformFlag); cwd != "" {
		session["working_directory"] = cwd
		fields["repository"] = cwd
	}
	if len(session) > 0 {
		fields["session"] = session
	}
	return fields
}

func toolFields(toolName string, toolInput map[string]interface{}) map[string]interface{} {
	return toolFieldsWithResponse(toolName, toolInput, nil)
}

func toolFieldsWithResponse(toolName string, toolInput, toolResponse map[string]interface{}) map[string]interface{} {
	fields := map[string]interface{}{}
	if toolName != "" {
		fields["tool"] = map[string]interface{}{"name": toolName}
	}
	if command := firstToolString(toolInput, "command", "cmd", "shell_command", "CommandLine", "commandLine"); command != "" {
		fields["command"] = map[string]interface{}{"command": command}
		fields["tool"] = mergeNested(fields["tool"], map[string]interface{}{"name": toolName, "command": command})
	}
	if path := firstToolString(toolInput, "file_path", "filePath", "path", "Path", "AbsolutePath", "DirectoryPath", "SearchPath", "searchPath"); path != "" {
		fields["file"] = map[string]interface{}{
			"path":      path,
			"operation": fileOperation(toolName),
			"language":  strings.TrimPrefix(filepath.Ext(path), "."),
		}
		fields["tool"] = mergeNested(fields["tool"], map[string]interface{}{"path": path})
	}
	if mcp := mcpToolFields(toolName, toolInput, toolResponse); len(mcp) > 0 {
		fields["mcp"] = mcp
		tool := firstNestedString(mcp, "tool")
		fields["gen_ai"] = genAIToolFields(tool, toolInput, toolResponse)
		for key, value := range mcpStandardFields(toolInput, toolResponse) {
			fields[key] = value
		}
	}
	return fields
}

func mcpToolFields(toolName string, toolInput, toolResponse map[string]interface{}) map[string]interface{} {
	maps := []map[string]interface{}{toolInput, toolResponse}
	mcpServer := firstToolStringAcross(maps, "mcp_server", "mcp_server_name", "mcp.server", "mcp.server.name")
	mcpTool := firstToolStringAcross(maps, "mcp_tool", "mcp_tool_name", "mcp.tool", "mcp.tool.name")
	mcpMethod := firstToolStringAcross(maps, "mcp.method.name", "mcp_method_name", "mcp_method")
	mcpProtocol := firstToolStringAcross(maps, "mcp.protocol.version", "mcp_protocol_version")
	mcpResource := firstToolStringAcross(maps, "mcp.resource.uri", "mcp_resource_uri")
	mcpSession := firstToolStringAcross(maps, "mcp.session.id", "mcp_session_id")
	hasCascadeServerToolPair := isCascadePlatform(platformFlag) && firstToolStringAcross(maps, "server_name") != "" && firstToolStringAcross(maps, "tool_name") != ""

	server := firstToolStringAcross([]map[string]interface{}{toolInput, toolResponse}, "server", "server_name", "mcp_server", "mcp_server_name", "mcp.server", "mcp.server.name")
	tool := firstToolStringAcross([]map[string]interface{}{toolInput, toolResponse}, "tool", "tool_name", "function_name", "mcp_tool", "mcp_tool_name", "mcp.tool", "mcp.tool.name", "gen_ai.tool.name")
	genericToolName := firstToolStringAcross([]map[string]interface{}{toolInput, toolResponse}, "name")
	method := firstToolStringAcross([]map[string]interface{}{toolInput, toolResponse}, "mcp.method.name", "mcp_method_name", "mcp_method", "method_name", "method")
	protocol := firstToolStringAcross([]map[string]interface{}{toolInput, toolResponse}, "mcp.protocol.version", "mcp_protocol_version", "protocol_version")
	resource := firstToolStringAcross([]map[string]interface{}{toolInput, toolResponse}, "mcp.resource.uri", "mcp_resource_uri", "resource_uri", "uri")
	session := firstToolStringAcross([]map[string]interface{}{toolInput, toolResponse}, "mcp.session.id", "mcp_session_id")

	if derivedServer, derivedTool := deriveMCPServerTool(toolName); derivedServer != "" || derivedTool != "" {
		if server == "" {
			server = derivedServer
		}
		if tool == "" {
			tool = derivedTool
		}
	}

	isMCP := mcpServer != "" || mcpTool != "" || mcpMethod != "" || mcpProtocol != "" || mcpResource != "" || mcpSession != "" || hasCascadeServerToolPair || strings.Contains(strings.ToLower(toolName), "mcp")
	if !isMCP {
		return nil
	}
	if tool == "" {
		tool = genericToolName
	}
	if method == "" {
		method = "tools/call"
	}
	out := map[string]interface{}{"server": server, "tool": tool}
	if method != "" {
		out["method"] = map[string]interface{}{"name": method}
	}
	if protocol != "" {
		out["protocol"] = map[string]interface{}{"version": protocol}
	}
	if resource != "" {
		out["resource"] = map[string]interface{}{"uri": resource}
	}
	if session != "" {
		out["session"] = map[string]interface{}{"id": session}
	}
	return out
}

func deriveMCPServerTool(toolName string) (string, string) {
	trimmed := strings.TrimSpace(toolName)
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "mcp:") {
		return "", strings.TrimSpace(trimmed[len("MCP:"):])
	}
	if strings.HasPrefix(lower, "mcp__") {
		parts := strings.Split(trimmed, "__")
		if len(parts) >= 3 {
			return parts[1], strings.Join(parts[2:], "__")
		}
	}
	return "", ""
}

func genAIToolFields(tool string, toolInput, toolResponse map[string]interface{}) map[string]interface{} {
	genAI := map[string]interface{}{
		"operation": map[string]interface{}{"name": "execute_tool"},
	}
	toolInfo := map[string]interface{}{}
	if tool != "" {
		toolInfo["name"] = tool
	}
	call := map[string]interface{}{}
	if len(toolInput) > 0 {
		call["arguments"] = toolInput
	}
	if len(toolResponse) > 0 {
		call["result"] = toolResponse
	}
	if len(call) > 0 {
		toolInfo["call"] = call
	}
	if len(toolInfo) > 0 {
		genAI["tool"] = toolInfo
	}
	return genAI
}

func mcpStandardFields(toolInput, toolResponse map[string]interface{}) map[string]interface{} {
	fields := map[string]interface{}{}
	maps := []map[string]interface{}{toolInput, toolResponse}
	if errorType := mcpErrorType(toolResponse); errorType != "" {
		fields["error"] = map[string]interface{}{"type": errorType}
	} else if errorType := firstToolStringAcross(maps, "error.type", "error_type"); errorType != "" {
		fields["error"] = map[string]interface{}{"type": errorType}
	}
	jsonrpc := map[string]interface{}{}
	if id := firstToolStringAcross(maps, "jsonrpc.request.id", "jsonrpc_request_id", "request_id"); id != "" {
		jsonrpc["request"] = map[string]interface{}{"id": id}
	}
	if version := firstToolStringAcross(maps, "jsonrpc.protocol.version", "jsonrpc_protocol_version", "jsonrpc"); version != "" {
		jsonrpc["protocol"] = map[string]interface{}{"version": version}
	}
	if len(jsonrpc) > 0 {
		fields["jsonrpc"] = jsonrpc
	}
	network := map[string]interface{}{}
	networkProtocol := map[string]interface{}{}
	if name := firstToolStringAcross(maps, "network.protocol.name", "network_protocol_name"); name != "" {
		networkProtocol["name"] = strings.ToLower(name)
	}
	if version := firstToolStringAcross(maps, "network.protocol.version", "network_protocol_version"); version != "" {
		networkProtocol["version"] = version
	}
	if len(networkProtocol) > 0 {
		network["protocol"] = networkProtocol
	}
	if transport := firstToolStringAcross(maps, "network.transport", "network_transport", "transport"); transport != "" {
		network["transport"] = strings.ToLower(transport)
	}
	if len(network) > 0 {
		fields["network"] = network
	}
	if status := firstToolStringAcross(maps, "rpc.response.status_code", "rpc_response_status_code", "status_code"); status != "" {
		fields["rpc"] = map[string]interface{}{"response": map[string]interface{}{"status_code": status}}
	}
	server := map[string]interface{}{}
	if address := firstToolStringAcross(maps, "server.address", "server_address", "serverAddress", "address"); address != "" {
		server["address"] = address
	}
	if port, ok := firstToolIntAcross(maps, "server.port", "server_port", "serverPort", "port"); ok {
		server["port"] = port
	}
	if len(server) > 0 {
		fields["server"] = server
	}
	return fields
}

func mcpErrorType(toolResponse map[string]interface{}) string {
	if toolResponse == nil {
		return ""
	}
	if errorType := firstToolString(toolResponse, "error.type", "error_type"); errorType != "" {
		return errorType
	}
	if isError := firstToolString(toolResponse, "isError", "is_error"); strings.EqualFold(isError, "true") {
		return "tool_error"
	}
	if mcpHasErrorValue(toolResponse, "error") {
		return "tool_error"
	}
	return ""
}

func mcpHasErrorValue(toolResponse map[string]interface{}, key string) bool {
	value, ok := toolResponse[key]
	if !ok || value == nil {
		return false
	}
	if boolValue, ok := value.(bool); ok {
		return boolValue
	}
	return normalizeToolString(value) != ""
}

func diffFields(filePath, diffStr string) map[string]interface{} {
	if filePath == "" {
		return nil
	}
	file := map[string]interface{}{
		"path":      filePath,
		"operation": "modify",
		"language":  strings.TrimPrefix(filepath.Ext(filePath), "."),
	}
	if diffStr != "" {
		sum := sha256.Sum256([]byte(diffStr))
		file["diff_hash"] = hex.EncodeToString(sum[:])
		file["diff_bytes"] = len(diffStr)
		file["diff"] = diffStr
	}
	return map[string]interface{}{"file": file}
}

func mergeNested(existing interface{}, values map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	if current, ok := existing.(map[string]interface{}); ok {
		for key, value := range current {
			out[key] = value
		}
	}
	for key, value := range values {
		if value != "" && value != nil {
			out[key] = value
		}
	}
	return out
}

func firstToolString(input map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := input[key]; ok {
			if str := normalizeToolString(value); str != "" {
				return str
			}
		}
	}
	return ""
}

func firstToolStringAcross(inputs []map[string]interface{}, keys ...string) string {
	for _, input := range inputs {
		if input == nil {
			continue
		}
		if value := firstToolString(input, keys...); value != "" {
			return value
		}
	}
	return ""
}

func firstToolIntAcross(inputs []map[string]interface{}, keys ...string) (int, bool) {
	for _, input := range inputs {
		if input == nil {
			continue
		}
		for _, key := range keys {
			value, ok := input[key]
			if !ok {
				continue
			}
			switch typed := value.(type) {
			case int:
				return typed, true
			case int64:
				return int(typed), true
			case float64:
				return int(typed), true
			case string:
				parsed, err := strconv.Atoi(strings.TrimSpace(typed))
				if err == nil {
					return parsed, true
				}
			}
		}
	}
	return 0, false
}

func firstNestedString(input map[string]interface{}, key string) string {
	if input == nil {
		return ""
	}
	value, ok := input[key]
	if !ok {
		return ""
	}
	return normalizeToolString(value)
}

func normalizeToolString(value interface{}) string {
	str := strings.TrimSpace(fmt.Sprint(value))
	if str == "" || str == "<nil>" {
		return ""
	}
	return strings.Trim(strings.TrimSpace(str), `"`)
}

func fileOperation(toolName string) string {
	lower := strings.ToLower(toolName)
	switch {
	case strings.Contains(lower, "read") || strings.Contains(lower, "view") || strings.Contains(lower, "list") || strings.Contains(lower, "grep") || strings.Contains(lower, "search"):
		return "read"
	case strings.Contains(lower, "write") || strings.Contains(lower, "create"):
		return "create"
	case strings.Contains(lower, "edit") || strings.Contains(lower, "patch"):
		return "modify"
	default:
		return ""
	}
}

func actionForTool(hookEvent, toolName string) string {
	lower := strings.ToLower(toolName)
	if platformFlag == "grok" {
		if hookEvent == "post_tool_use_failure" {
			return "tool.failed"
		}
		switch lower {
		case "run_terminal_command":
			return "command.executed"
		case "read_file":
			return "file.read"
		case "search_replace", "write_file":
			return "file.modified"
		}
	}
	if isDevinLikePlatform(platformFlag) {
		switch {
		case strings.HasPrefix(lower, "mcp__"):
			return "mcp.tool_invoked"
		case lower == "exec":
			return "command.executed"
		case lower == "read":
			return "file.read"
		case lower == "edit" || lower == "write":
			return "file.modified"
		}
	}
	if platformFlag == "antigravity" {
		switch lower {
		case "run_command":
			return "command.executed"
		case "view_file", "list_dir", "grep_search", "find_by_name":
			return "file.read"
		case "edit_file", "write_file", "apply_patch":
			return "file.modified"
		}
	}
	switch {
	case strings.Contains(lower, "mcp"):
		return "mcp.tool_invoked"
	case lower == "bash" || strings.Contains(lower, "shell") || strings.Contains(lower, "terminal") || strings.Contains(lower, "command"):
		return "command.executed"
	case strings.Contains(lower, "read"):
		return "file.read"
	case isFileEditTool(platformFlag, toolName) || hookEvent == "afterFileEdit":
		return "file.modified"
	default:
		return "tool.invoked"
	}
}
