package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// readStdinJSON decodes JSON from stdin into a map.
func readStdinJSON() (map[string]interface{}, error) {
	var input map[string]interface{}
	err := json.NewDecoder(os.Stdin).Decode(&input)
	return input, err
}

// outputJSON writes a JSON object to stdout.
func outputJSON(data map[string]interface{}) {
	json.NewEncoder(os.Stdout).Encode(data)
}

// outputJSONAndExit writes a JSON object to stdout and exits.
func outputJSONAndExit(data map[string]interface{}) {
	json.NewEncoder(os.Stdout).Encode(data)
	os.Exit(0)
}

// emptyResponse is a reusable empty JSON response.
var emptyResponse = map[string]interface{}{}

func isDevinLikePlatform(platform string) bool {
	return platform == "devin" || platform == "devin-cli"
}

func isCascadePlatform(platform string) bool {
	return platform == "devin-desktop"
}

// getFirstStr returns the first non-empty string value from input for the given keys.
func getFirstStr(input map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if v, ok := input[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// resolveSessionID extracts the session ID from input based on the platform.
// For Copilot, it prefers the transcript filename UUID over the VS Code sessionId.
// For Claude, it reads session_id directly.
func resolveSessionID(input map[string]interface{}, platform string) string {
	switch platform {
	case "antigravity":
		return getFirstStr(input, "conversationId", "conversation_id", "session_id", "sessionId")
	case "copilot":
		transcriptPath := getFirstStr(input, "transcriptPath", "transcript_path")
		if id := sessionIDFromTranscriptPath(transcriptPath); id != "" {
			return id
		}
		return getFirstStr(input, "sessionId", "session_id")
	case "cursor":
		return getFirstStr(input, "conversation_id")
	case "vscode":
		return getFirstStr(input, "sessionId", "session_id", "conversation_id", "gen_ai.conversation.id")
	case "devin", "devin-cli":
		return getFirstStr(input, "session_id", "sessionId", "conversation_id")
	case "devin-desktop":
		return getFirstStr(input, "trajectory_id", "session_id", "sessionId", "conversation_id")
	case "grok":
		return getFirstStr(input, "sessionId", "session_id", "sessionID")
	case "hermes":
		return hermesFirstString(input, "session_id", "sessionId", "session_key", "task_id")
	case "opencode":
		return getFirstStr(input, "session_id", "sessionID")
	default:
		id, _ := input["session_id"].(string)
		return id
	}
}

// resolveSessionIDWithTranscript extracts both session ID and transcript path.
// Used by commands that need the transcript path for upload.
func resolveSessionIDWithTranscript(input map[string]interface{}, platform string) (sessionID, transcriptPath string) {
	switch platform {
	case "antigravity":
		sessionID = getFirstStr(input, "conversationId", "conversation_id", "session_id", "sessionId")
		transcriptPath = getFirstStr(input, "transcriptPath", "transcript_path")
		return
	case "copilot":
		transcriptPath = getFirstStr(input, "transcriptPath", "transcript_path")
		sessionID = sessionIDFromTranscriptPath(transcriptPath)
		if sessionID == "" {
			sessionID = getFirstStr(input, "sessionId", "session_id")
		}
		return
	case "cursor":
		sessionID = getFirstStr(input, "conversation_id")
		transcriptPath = getFirstStr(input, "transcript_path")
		return
	case "vscode":
		sessionID = getFirstStr(input, "sessionId", "session_id", "conversation_id", "gen_ai.conversation.id")
		transcriptPath = getFirstStr(input, "transcript_path", "transcriptPath")
		return
	case "devin", "devin-cli":
		sessionID = getFirstStr(input, "session_id", "sessionId", "conversation_id")
		transcriptPath = getFirstStr(input, "transcript_path", "transcriptPath")
		return
	case "devin-desktop":
		sessionID = getFirstStr(input, "trajectory_id", "session_id", "sessionId", "conversation_id")
		transcriptPath = getFirstStr(input, "transcript_path", "transcriptPath")
		return
	case "grok":
		sessionID = getFirstStr(input, "sessionId", "session_id", "sessionID")
		return
	case "hermes":
		sessionID = hermesFirstString(input, "session_id", "sessionId", "session_key", "task_id")
		transcriptPath = hermesFirstString(input, "transcript_path", "transcriptPath")
		return
	case "opencode":
		sessionID = getFirstStr(input, "session_id", "sessionID")
		return
	default:
		sessionID, _ = input["session_id"].(string)
		transcriptPath, _ = input["transcript_path"].(string)
		return
	}
}

// sessionIDFromTranscriptPath extracts the UUID from a transcript filename.
// Example: ".../transcripts/ff2d7803-5799-4f18-83f0-3633b2c11809.jsonl" -> "ff2d7803-..."
func sessionIDFromTranscriptPath(transcriptPath string) string {
	if transcriptPath == "" {
		return ""
	}
	base := filepath.Base(transcriptPath)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// resolveCwd extracts the working directory from hook input based on platform.
// For Cursor: tries input["cwd"], then workspace_roots[0], then CURSOR_PROJECT_DIR env var.
// For other platforms: reads input["cwd"] directly.
func resolveCwd(input map[string]interface{}, platform string) string {
	if platform == "antigravity" {
		if cwd := getFirstStr(input, "cwd", "workingDirectoryPath"); cwd != "" {
			return cwd
		}
		if toolInput := resolveToolInput(input); toolInput != nil {
			if cwd := firstToolString(toolInput, "Cwd", "cwd", "workingDirectoryPath"); cwd != "" {
				return cwd
			}
		}
		if roots, ok := input["workspacePaths"].([]interface{}); ok && len(roots) > 0 {
			if cwd, ok := roots[0].(string); ok && cwd != "" {
				return cwd
			}
		}
		if roots, ok := input["workspace_paths"].([]interface{}); ok && len(roots) > 0 {
			if cwd, ok := roots[0].(string); ok && cwd != "" {
				return cwd
			}
		}
		return ""
	}
	if platform == "cursor" || platform == "vscode" {
		if cwd := getFirstStr(input, "cwd"); cwd != "" {
			return cwd
		}
		if roots, ok := input["workspace_roots"].([]interface{}); ok && len(roots) > 0 {
			if cwd, ok := roots[0].(string); ok && cwd != "" {
				return cwd
			}
		}
		if roots, ok := input["workspaceFolders"].([]interface{}); ok && len(roots) > 0 {
			if cwd, ok := roots[0].(string); ok && cwd != "" {
				return cwd
			}
		}
		if platform == "vscode" {
			return os.Getenv("VSCODE_CWD")
		}
		if cwd := os.Getenv("CURSOR_PROJECT_DIR"); cwd != "" {
			return cwd
		}
		return ""
	}
	if platform == "opencode" {
		if cwd := getFirstStr(input, "cwd", "directory", "worktree"); cwd != "" {
			return cwd
		}
	}
	if isDevinLikePlatform(platform) {
		if cwd := getFirstStr(input, "cwd", "project_dir", "projectDir"); cwd != "" {
			return cwd
		}
		return os.Getenv("DEVIN_PROJECT_DIR")
	}
	if isCascadePlatform(platform) {
		if cwd := getFirstStr(input, "cwd", "workspace_path", "project_path"); cwd != "" {
			return cwd
		}
		if info := cascadeToolInfo(input); info != nil {
			if cwd := firstToolString(info, "cwd", "workspace_path", "project_path", "directory", "working_directory"); cwd != "" {
				return cwd
			}
		}
		return os.Getenv("DEVIN_PROJECT_DIR")
	}
	if platform == "grok" {
		if cwd := getFirstStr(input, "workspaceRoot", "workspace_root", "cwd"); cwd != "" {
			return cwd
		}
		return os.Getenv("GROK_WORKSPACE_ROOT")
	}
	if platform == "hermes" {
		if cwd := getFirstStr(input, "cwd", "working_directory", "workingDirectory"); cwd != "" {
			return cwd
		}
		if extra := hermesExtra(input); extra != nil {
			if cwd := firstToolString(extra, "cwd", "working_directory", "workingDirectory"); cwd != "" {
				return cwd
			}
		}
		return os.Getenv("HERMES_WORKSPACE_ROOT")
	}
	cwd, _ := input["cwd"].(string)
	return cwd
}

func hermesExtra(input map[string]interface{}) map[string]interface{} {
	if extra, ok := input["extra"].(map[string]interface{}); ok {
		return extra
	}
	return nil
}

func hermesFirstString(input map[string]interface{}, keys ...string) string {
	if value := getFirstStr(input, keys...); value != "" {
		return value
	}
	if extra := hermesExtra(input); extra != nil {
		return firstToolString(extra, keys...)
	}
	return ""
}
