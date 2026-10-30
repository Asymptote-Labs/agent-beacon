package asymptoteobserve

import (
	"encoding/json"
	"strings"
)

// shellCommandToolNames are the tools whose call runs a shell command line, normalized by
// normalizeToolName: Claude Code's Bash and PowerShell, Codex CLI's exec_command, shell_command,
// shell and local_shell, DeepSeek Harness's terminal_send, and the shell tools of the other
// supported runtimes.
var shellCommandToolNames = []string{
	"bash", "shell", "shellcommand", "execcommand", "exec", "localshell", "runterminalcmd",
	"runterminalcommand", "runcommand", "runshellcommand", "executebash", "executecommand", "terminal",
	"command", "cmd", "terminalsend", "powershell",
}

var shellCommandTools = func() map[string]bool {
	out := make(map[string]bool, len(shellCommandToolNames))
	for _, name := range shellCommandToolNames {
		out[name] = true
	}
	return out
}()

// IsShellCommandTool reports whether a tool's call runs a shell command line. MCP tools never do:
// their namespaced names (mcp__server__tool) are not in the set.
func IsShellCommandTool(toolName string) bool {
	return shellCommandTools[normalizeToolName(toolName)]
}

// toolCommandLineKeys are the argument names runtimes give a shell command line: `command` (Claude
// Code, Codex shell_command and shell, most others), `cmd` (Codex exec_command), `shell_command`,
// and Antigravity's `CommandLine`.
var toolCommandLineKeys = []string{"command", "cmd", "shell_command", "CommandLine", "commandLine"}

// ToolCommandLine returns the command line in a tool call's arguments, or "". args is the decoded
// argument object or its JSON text. A string value is the command line; an argv array (Codex's
// shell tool) is joined with spaces.
//
// It does not decide whether the tool is a shell: callers that classify a call as a command check
// IsShellCommandTool, and callers that keep the argument for any tool apply their own runtime's
// guards (an editor tool whose `command` names an edit operation, for one). The single name it does
// read is terminal_send, DeepSeek Harness's persistent terminal, which names its command line `text`
// -- an ordinary argument name other tools use for other things.
func ToolCommandLine(toolName string, args interface{}) string {
	input := commandArgs(args)
	if input == nil {
		return ""
	}
	for _, key := range toolCommandLineKeys {
		if command := commandLineValue(input[key]); command != "" {
			return command
		}
	}
	if normalizeToolName(toolName) == "terminalsend" {
		return commandLineValue(input["text"])
	}
	return ""
}

func commandArgs(args interface{}) map[string]interface{} {
	switch typed := args.(type) {
	case map[string]interface{}:
		return typed
	case string:
		var out map[string]interface{}
		if json.Unmarshal([]byte(typed), &out) == nil {
			return out
		}
	}
	return nil
}

func commandLineValue(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []string:
		return strings.TrimSpace(strings.Join(typed, " "))
	case []interface{}:
		parts := make([]string, 0, len(typed))
		for _, part := range typed {
			if s, ok := part.(string); ok {
				parts = append(parts, s)
			}
		}
		return strings.TrimSpace(strings.Join(parts, " "))
	}
	return ""
}
