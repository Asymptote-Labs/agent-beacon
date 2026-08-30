package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/logging"
)

// readStdinJSON decodes JSON from stdin into a map.
func readStdinJSON() (map[string]interface{}, error) {
	var input map[string]interface{}
	err := json.NewDecoder(os.Stdin).Decode(&input)
	if err == nil {
		applyPayloadPlatformOverride(input)
	}
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

// hookNoopResponse returns the platform's expected no-op hook reply. Call it
// after readStdinJSON so payload-based platform overrides have been applied.
func hookNoopResponse() map[string]interface{} {
	if platformFlag == "cursor" {
		return map[string]interface{}{"continue": true}
	}
	return emptyResponse
}

// newHookLogger returns a per-session logger when a session ID is known and a
// per-platform logger otherwise.
func newHookLogger(hookName, platform, sessionID string) *logging.Logger {
	if sessionID != "" {
		return logging.NewSessionLogger(hookName, platform, sessionID)
	}
	return logging.NewLoggerForPlatform(hookName, platform)
}

func isDevinLikePlatform(platform string) bool {
	return platform == "devin" || platform == "devin-cli"
}

func isCascadePlatform(platform string) bool {
	return platform == "devin-desktop"
}

func applyPayloadPlatformOverride(input map[string]interface{}) {
	if platformFlag != "claude" || !looksLikeCursorPayload(input) {
		return
	}
	platformFlag = "cursor"
}

func looksLikeCursorPayload(input map[string]interface{}) bool {
	if getFirstStr(input, "conversation_id", "parent_conversation_id", "cursor_version", "generation_id") != "" {
		return true
	}
	if _, ok := input["workspace_roots"]; ok {
		return true
	}
	switch getFirstStr(input, "hook_event_name", "hookEventName") {
	case "beforeSubmitPrompt", "beforeShellExecution", "afterShellExecution", "beforeReadFile", "afterFileEdit", "afterAgentThought", "postToolUse", "postToolUseFailure", "preCompact", "subagentStart", "subagentStop":
		return true
	default:
		return false
	}
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
		return getFirstStr(input, "conversation_id", "parent_conversation_id", "session_id", "sessionId")
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
	// Cline calls this a task, not a session: `taskId` is one of the base fields present on every
	// hook payload it sends. The session_id spellings are kept as a fallback because the same
	// binary receives payloads from Cline's plugin host and its CLI, and only the base fields are
	// documented as common to both.
	case "cline":
		return getFirstStr(input, "taskId", "task_id", "sessionId", "session_id")
	// The Beacon extension lifts the session id onto the envelope as `sessionId`, reading it from
	// the handler context's session manager per event. The snake_case spellings are a fallback for
	// a payload that reached this command by some other route.
	//
	// Oh My Pi shares this reader because it shares the envelope: its extension is built from the
	// same contract. Some of its events -- the approval pair -- also carry a `sessionId` of their
	// own, which lands on the same key and needs no separate spelling.
	case "pi", "omp":
		return getFirstStr(input, "sessionId", "session_id", "sessionID")
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
		sessionID = getFirstStr(input, "conversation_id", "parent_conversation_id", "session_id", "sessionId")
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
	// No transcript path: Cline keeps conversation state under its own data directory and does not
	// hand hooks a path to it, so there is nothing to return here. Left explicit rather than
	// falling through to the default, which would look for `transcript_path` keys that never
	// arrive and read as an oversight.
	case "cline":
		sessionID = getFirstStr(input, "taskId", "task_id", "sessionId", "session_id")
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
	if platform == "pi" || platform == "omp" {
		// The extension lifts the runtime's cwd onto the envelope, preferring the handler context's
		// own cwd and falling back to the session manager's. An event that carried its own cwd --
		// user_bash does, and so does Oh My Pi's user_python -- wins over both, because the
		// extension spreads event fields last.
		if cwd := getFirstStr(input, "cwd", "workingDirectory", "working_directory"); cwd != "" {
			return cwd
		}
	}
	if platform == "cline" {
		if cwd := getFirstStr(input, "cwd", "workingDirectory", "working_directory"); cwd != "" {
			return cwd
		}
		return firstWorkspaceRoot(input, "workspaceRoots", "workspace_roots")
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

// firstWorkspaceRoot reads the first workspace root out of a payload that carries a list of them.
//
// Cline's documented base fields name `workspaceRoots` but not the type of its elements, and Cline
// supports multi-root workspaces, so both shapes are read: a plain path string, and an object whose
// path lives under a "path" or "root" key.
//
// A "uri" key is deliberately not read. A file:// URI is not a filesystem path, and returning one
// would be worse than returning nothing: this value is what the git helpers resolve a repository
// and branch from, and it is written into events, so a URI would produce a path that looks real,
// fails every lookup, and reaches customer logs. An empty result degrades to "no repository
// context" instead, which is the honest answer until a fixture shows what Cline actually sends.
func firstWorkspaceRoot(input map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		roots, ok := input[key].([]interface{})
		if !ok || len(roots) == 0 {
			continue
		}
		switch root := roots[0].(type) {
		case string:
			if root != "" {
				return root
			}
		case map[string]interface{}:
			if path := firstToolString(root, "path", "root"); path != "" {
				return path
			}
		}
	}
	return ""
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

// Runtime-agnostic readers for decoded JSON payloads.
//
// These carried an `opencode` prefix while opencode was the only mapper that needed them. They
// are not opencode-specific -- any runtime whose payloads arrive as decoded JSON needs the same
// four questions answered -- so they live here with the rest of the shared payload helpers, and
// a second mapper can use them without reading as though it were borrowing opencode's code.

// firstMap returns the first of the named keys whose value is a JSON object.
//
// Runtime payloads disagree about where they nest things and about which spelling they use, so
// every mapper needs to ask "whichever of these keys is present". Returns nil rather than an empty
// map so callers can distinguish absent from empty.
func firstMap(input map[string]interface{}, keys ...string) map[string]interface{} {
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

// jsonInt reads an integer out of a decoded JSON value.
//
// Tolerant of the four shapes a number arrives in after encoding/json: float64 for any plain
// number, json.Number under a decoder with UseNumber, and a string for runtimes that quote their
// numerics. The bool result distinguishes "absent or unparseable" from a real zero -- an exit code
// of 0 means success, so conflating the two would report every failed command as a clean one.
func jsonInt(value interface{}) (int, bool) {
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

// jsonFloat reads a float out of a decoded JSON value, with the same shape tolerance as jsonInt.
func jsonFloat(value interface{}) (float64, bool) {
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

// cloneFields deep-copies an event field map so sibling events built from the same base cannot
// alias each other's nested maps.
//
// One payload can expand into several events -- a tool result plus the file mutations it caused --
// and each needs its own copy: mutating a shared nested map would retroactively edit an event that
// was already assembled. Round-trips through JSON because the values are decoded JSON to begin
// with, and falls back to a shallow copy if that fails, which is strictly better than returning
// the original.
func cloneFields(input map[string]interface{}) map[string]interface{} {
	if data, err := json.Marshal(input); err == nil {
		var out map[string]interface{}
		if err := json.Unmarshal(data, &out); err == nil {
			return out
		}
	}
	out := make(map[string]interface{}, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

// mergeMap copies src's top-level keys over dst, replacing rather than merging nested values. Use
// mergeNested when a nested map has to survive.
func mergeMap(dst, src map[string]interface{}) {
	for key, value := range src {
		dst[key] = value
	}
}

// firstNonEmpty returns the first value that is not empty or whitespace.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// toolNameHasToken reports whether a tool name contains the given word as a whole token.
//
// Tool names are compound and inconsistently punctuated across runtimes -- write_to_file,
// applyPatch, run-terminal-command -- so this splits on every non-alphanumeric boundary and on
// nothing else, then compares whole tokens. Substring matching is what it exists to avoid: "read"
// is inside "spreadsheet" and "thread", so a Contains rule classifies unrelated tools as file
// reads.
func toolNameHasToken(name, token string) bool {
	for _, part := range strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	}) {
		if part == token {
			return true
		}
	}
	return false
}
