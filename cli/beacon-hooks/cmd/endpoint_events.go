package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/logging"
)

func emitHookEvent(logger *logging.Logger, action, category, severity, message string, input map[string]interface{}, fields map[string]interface{}) {
	if fields == nil {
		fields = map[string]interface{}{}
	}
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
	if cwd := resolveCwd(input, platformFlag); cwd != "" {
		fields["session"] = mergeNested(fields["session"], map[string]interface{}{"working_directory": cwd})
		fields["repository"] = cwd
	}
	if branch := getFirstStr(input, "branch", "git_branch"); branch != "" {
		fields["branch"] = branch
	}
	if err := logger.EndpointEvent(action, category, severity, message, fields); err != nil {
		logger.Error("Failed to write endpoint event", "error", err.Error(), "action", action)
	}
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
	if server := firstToolString(toolInput, "server", "server_name", "mcp_server"); server != "" || strings.Contains(strings.ToLower(toolName), "mcp") {
		fields["mcp"] = map[string]interface{}{
			"server": server,
			"tool":   firstToolString(toolInput, "tool", "tool_name", "name"),
		}
	}
	return fields
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
		if config.ContentRetentionMode() != config.ContentRetentionMetadata {
			file["diff"] = diffStr
		}
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
