package cmd

import (
	"encoding/json"
	"path"
	"path/filepath"
	"strings"

	hookdiff "github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/diff"
	"github.com/spf13/cobra"
)

// cline-event is the single entry point for every Cline lifecycle payload.
//
// Cline delivers its hooks through an in-process plugin whose handlers all fire in the same
// process, so one command that receives a typed envelope is a better fit than the
// command-per-hook shape used for runtimes that exec a separate hook per event. This mirrors how
// opencode is integrated.
var clineEventCmd = &cobra.Command{
	Use:   "cline-event",
	Short: "Record Cline hook telemetry",
	Long:  `cline-event receives raw Beacon Cline plugin payloads and writes local endpoint telemetry.`,
	Run:   runClineEvent,
}

func init() {
	rootCmd.AddCommand(clineEventCmd)
}

func runClineEvent(cmd *cobra.Command, args []string) {
	input, err := readStdinJSON()
	if err != nil {
		outputJSON(emptyResponse)
		return
	}
	sessionID := resolveSessionID(input, "cline")
	logger := newHookLogger("cline-event", "cline", sessionID)
	for _, event := range clineEndpointEvents(input, sessionID) {
		if event.action == "" {
			continue
		}
		_ = logger.EndpointEvent(event.action, event.category, event.severity, event.message, event.fields)
	}
	outputJSON(emptyResponse)
}

// The lifecycle points Beacon records, independent of which name Cline used to announce them.
const (
	clineStageTaskStart  = "task_start"
	clineStagePrompt     = "prompt"
	clineStageToolBefore = "tool_before"
	clineStageToolAfter  = "tool_after"
	clineStageTaskEnd    = "task_end"
	clineStageTaskCancel = "task_cancel"
	clineStageTaskError  = "task_error"
)

// clineStage decides which lifecycle point a payload describes.
//
// Cline names the same points more than one way, and Beacon has to accept all of them because two
// different surfaces produce these payloads. The plugin SDK exposes handlers (beforeRun,
// beforeTool, afterTool, afterRun) over a documented stage list that spells them differently
// (run_start, tool_call_before, tool_call_after, run_end), and Cline's file-based hooks name the
// script after the hook itself (PreToolUse, PostToolUse, TaskStart) and pass the same name back in
// the payload's `hookName` base field. Recognizing every spelling is what lets one mapper serve
// both surfaces instead of two mappers disagreeing about the same task.
//
// Matching is done on letters and digits only, lowercased, so PreToolUse, pre_tool_use and
// tool_call_before all reduce to a single comparison instead of a case list per spelling.
func clineStage(input map[string]interface{}) string {
	var normalized strings.Builder
	for _, r := range strings.ToLower(getFirstStr(input, "type", "hookName", "hook_name", "stage", "event")) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			normalized.WriteRune(r)
		}
	}
	switch normalized.String() {
	case "beforerun", "runstart", "sessionstart", "taskstart", "taskresume":
		return clineStageTaskStart
	case "userpromptsubmit", "promptsubmit", "input":
		return clineStagePrompt
	case "beforetool", "toolcallbefore", "pretooluse":
		return clineStageToolBefore
	case "aftertool", "toolcallafter", "posttooluse":
		return clineStageToolAfter
	case "afterrun", "runend", "sessionshutdown", "taskcomplete":
		return clineStageTaskEnd
	case "taskcancel":
		return clineStageTaskCancel
	case "stoperror", "error":
		return clineStageTaskError
	default:
		return ""
	}
}

// clineEndpointEvents maps one Cline payload onto the endpoint events it justifies.
//
// An unrecognized stage returns nothing rather than a generic event. Cline streams runtime events
// well beyond the lifecycle points above, and turning each into an undifferentiated
// "something happened" record would fill the runtime log with rows no query asks for.
func clineEndpointEvents(input map[string]interface{}, sessionID string) []normalizedEvent {
	fields := clineBaseFields(input, sessionID)
	one := func(action, category, severity, message string, values map[string]interface{}) []normalizedEvent {
		return []normalizedEvent{{action: action, category: category, severity: severity, message: message, fields: values}}
	}

	switch clineStage(input) {
	case clineStageTaskStart:
		events := one("session.started", "session", "info", "Cline task started", fields)
		// Cline's run-start context carries the message that started the task, so the prompt is
		// recorded from the same payload rather than waiting for a prompt hook that only the
		// file-based surface has.
		if prompt := clinePromptText(input); prompt != "" {
			events = append(events, clinePromptEvent(cloneFields(fields), prompt))
		}
		return events
	case clineStagePrompt:
		prompt := clinePromptText(input)
		if prompt == "" {
			return nil
		}
		return []normalizedEvent{clinePromptEvent(fields, prompt)}
	case clineStageToolBefore:
		mergeMap(fields, clineToolFields(input, false))
		return one("tool.invoked", "tool", "info", "Cline tool invoked", fields)
	case clineStageToolAfter:
		return clineToolAfterEvents(input, fields)
	case clineStageTaskEnd:
		// A task end does not imply a task success. The plugin surface has one run-completion
		// handler for all three outcomes, so a cancel or a failure arrives here rather than at
		// the cancel and error stages, which only the file-based surface names separately.
		switch clineTaskOutcome(input) {
		case clineOutcomeCancelled:
			return clineCancelEvents(input, fields, one)
		case clineOutcomeFailed:
			return clineErrorEvents(input, fields, one)
		}
		if usage := clineUsage(input); len(usage) > 0 {
			fields["gen_ai"] = mergeNested(fields["gen_ai"], map[string]interface{}{"usage": usage})
		}
		return one("session.ended", "session", "info", "Cline task completed", fields)
	case clineStageTaskCancel:
		return clineCancelEvents(input, fields, one)
	case clineStageTaskError:
		return clineErrorEvents(input, fields, one)
	default:
		return nil
	}
}

// supportedClineEventTypes lists every spelling clineStage recognizes.
//
// Exists so a test can assert that each one still maps to an event: these strings are the contract
// between this mapper and the managed plugin, and a silent typo on either side produces no
// telemetry rather than an error.
func supportedClineEventTypes() []string {
	return []string{
		"PostToolUse",
		"PreToolUse",
		"TaskCancel",
		"TaskComplete",
		"TaskResume",
		"TaskStart",
		"UserPromptSubmit",
		"afterRun",
		"afterTool",
		"beforeRun",
		"beforeTool",
		"error",
		"input",
		"run_end",
		"run_start",
		"session_shutdown",
		"session_start",
		"stop_error",
		"tool_call_after",
		"tool_call_before",
	}
}

// clineBaseFields builds the fields every Cline event carries.
//
// The workspace is resolved for "cline" by name rather than through platformFlag. The shared
// helpers default to the flag, which made this depend on how the binary was invoked: without
// --platform cline the default reader found none of Cline's keys and the event carried no working
// directory, repository or branch, while the file path beside it resolved correctly because that
// path already named the runtime.
func clineBaseFields(input map[string]interface{}, sessionID string) map[string]interface{} {
	fields := sessionFieldsForPlatform(sessionID, input, "cline")
	applyWorkspaceFieldsForPlatform(fields, input, "", "cline")
	fields["raw"] = map[string]interface{}{"cline": input}
	if model := clineModel(input); model != "" {
		fields["model"] = model
	}
	return fields
}

func clinePromptEvent(fields map[string]interface{}, prompt string) normalizedEvent {
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
	return normalizedEvent{
		action: "prompt.submitted", category: "prompt", severity: "info",
		message: "Prompt submitted to Cline", fields: fields,
	}
}

// clinePromptText finds the user's message in a run-start or prompt payload.
//
// Reads a nested "input" key, which on a tool payload would be the tool's arguments instead. That
// is safe because only the two prompt-bearing stages reach here, and stated because the collision
// is not obvious to the next reader.
func clinePromptText(input map[string]interface{}) string {
	if text := getFirstStr(input, "prompt", "userMessage", "user_message", "message", "text"); text != "" {
		return text
	}
	for _, key := range []string{"input", "message", "prompt", "run", "context"} {
		nested := firstMap(input, key)
		if nested == nil {
			continue
		}
		if text := getFirstStr(nested, "text", "content", "message", "prompt"); text != "" {
			return text
		}
		if text := clineTextParts(nested["content"]); text != "" {
			return text
		}
	}
	return clineTextParts(input["content"])
}

// clineTextParts joins the text of a structured message body, skipping non-text parts.
func clineTextParts(value interface{}) string {
	parts, ok := value.([]interface{})
	if !ok {
		return ""
	}
	var texts []string
	for _, part := range parts {
		partMap, ok := part.(map[string]interface{})
		if !ok {
			if text, ok := part.(string); ok && text != "" {
				texts = append(texts, text)
			}
			continue
		}
		if partType := getFirstStr(partMap, "type"); partType != "" && partType != "text" {
			continue
		}
		if text := getFirstStr(partMap, "text", "content"); text != "" {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, "\n")
}

// clineModel reports the model behind a payload, qualified by its provider when both are present.
//
// Provider-qualified because a bare model id is ambiguous once a runtime can reach the same model
// through more than one provider, and because opencode already writes provider/model -- one shape
// for this field across runtimes is worth more than matching each runtime's own spelling.
func clineModel(input map[string]interface{}) string {
	model := getFirstStr(input, "model", "modelId", "model_id")
	provider := getFirstStr(input, "apiProvider", "api_provider", "provider", "providerId", "provider_id")
	if model == "" {
		info := firstMap(input, "modelInfo", "model_info", "model", "api", "apiConfiguration")
		model = getFirstStr(info, "modelId", "model_id", "model", "id", "name")
		provider = firstNonEmpty(provider, getFirstStr(info, "apiProvider", "api_provider", "provider", "providerId", "provider_id"))
	}
	if model == "" {
		return ""
	}
	if provider != "" && !strings.Contains(model, "/") {
		return provider + "/" + model
	}
	return model
}

func clineToolName(input map[string]interface{}) string {
	if tool := getFirstStr(input, "toolName", "tool_name", "tool"); tool != "" {
		return tool
	}
	call := firstMap(input, "toolCall", "tool_call")
	if tool := getFirstStr(call, "name", "toolName", "tool_name", "tool"); tool != "" {
		return tool
	}
	return getFirstStr(input, "name")
}

func clineToolInput(input map[string]interface{}) map[string]interface{} {
	if args := firstMap(input, "input", "toolInput", "tool_input", "arguments", "args", "params"); args != nil {
		return args
	}
	call := firstMap(input, "toolCall", "tool_call")
	return firstMap(call, "input", "arguments", "args", "params")
}

func clineToolResponse(input map[string]interface{}) map[string]interface{} {
	if response := firstMap(input, "result", "toolResult", "tool_result", "output", "response"); response != nil {
		return response
	}
	// A tool that returns a bare string still has a result worth recording, so it is wrapped in the
	// same shape the map case produces rather than being dropped.
	if text := getFirstStr(input, "result", "output", "response"); text != "" {
		return map[string]interface{}{"output": text}
	}
	return nil
}

// clineToolFailure reports whether a completed tool call failed, and the text describing it.
//
// Failure and its description are separate answers because a payload can carry one without the
// other. Reading the text and treating "" as success left the reported bug half-open: an error
// object of {"code": 2} has no string field at all, and an empty {} has no field, so both were
// still recorded as successful tool calls -- the quietest way to be wrong, since the event writes
// and reads as ordinary activity. The presence of the error object is the failure signal; its
// contents only describe it.
//
// The response is read only for keys that name an error explicitly. It deliberately does not read
// `message`: Cline results carry status text there on success ("File written successfully"), and
// reading it turned every such completion into a high-severity failure.
func clineToolFailure(input map[string]interface{}) (string, bool) {
	if text := getFirstStr(input, "error", "errorMessage", "error_message"); text != "" {
		return text, true
	}
	if errMap := firstMap(input, "error"); errMap != nil {
		return getFirstStr(errMap, "message", "error", "errorMessage", "error_message", "name", "type", "code"), true
	}
	if response := clineToolResponse(input); response != nil {
		if text := getFirstStr(response, "error", "errorMessage", "error_message"); text != "" {
			return text, true
		}
	}
	return "", false
}

// clineToolErrorType names a failed tool call's error for the error.type field.
//
// Reads the same keys clineErrorType reads for session errors, which were inconsistent: a session
// error reported its name while a tool error was always "tool_error", discarding the one detail an
// investigator would filter on.
func clineToolErrorType(input map[string]interface{}) string {
	if errMap := firstMap(input, "error"); errMap != nil {
		if name := getFirstStr(errMap, "name", "type", "code"); name != "" {
			return name
		}
	}
	return "tool_error"
}

func clineErrorType(input map[string]interface{}) string {
	if errorInfo := firstMap(input, "error"); errorInfo != nil {
		if name := getFirstStr(errorInfo, "name", "type", "code"); name != "" {
			return name
		}
	}
	return firstNonEmpty(getFirstStr(input, "errorType", "error_type", "reason"), "task_error")
}

// clineToolAction classifies a Cline tool call into an endpoint action and category.
//
// Cline's built-in tool names are snake_case verbs -- read_file, write_to_file, execute_command,
// use_mcp_tool -- so the token rules at the bottom classify almost all of them correctly, and they
// also cover tools a plugin registers, whose names Beacon cannot know in advance. The explicit
// cases above exist only for the built-ins those rules would get wrong: three read tools whose
// names contain no reading verb at all.
func clineToolAction(name string) (string, string) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "list_files", "search_files", "list_code_definition_names":
		return "file.read", "file"
	}
	switch {
	case toolNameHasToken(name, "mcp"):
		return "mcp.tool_invoked", "mcp"
	case toolNameHasToken(name, "command") || toolNameHasToken(name, "commands") ||
		toolNameHasToken(name, "bash") || toolNameHasToken(name, "shell") || toolNameHasToken(name, "terminal"):
		return "command.executed", "command"
	case toolNameHasToken(name, "read"):
		return "file.read", "file"
	case toolNameHasToken(name, "write") || toolNameHasToken(name, "edit") ||
		toolNameHasToken(name, "replace") || toolNameHasToken(name, "patch") || toolNameHasToken(name, "create"):
		return "file.modified", "file"
	default:
		return "tool.completed", "tool"
	}
}

// clineFileOperation reports what a tool did to a file, for the file.operation field.
func clineFileOperation(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "list_files", "search_files", "list_code_definition_names":
		return "read"
	}
	switch {
	case toolNameHasToken(name, "read") || toolNameHasToken(name, "view") || toolNameHasToken(name, "list"):
		return "read"
	case toolNameHasToken(name, "write") || toolNameHasToken(name, "create"):
		return "create"
	case toolNameHasToken(name, "edit") || toolNameHasToken(name, "replace") || toolNameHasToken(name, "patch"):
		return "modify"
	default:
		return ""
	}
}

func clineToolMessage(action string) string {
	switch action {
	case "command.executed":
		return "Cline shell command executed"
	case "file.read":
		return "Cline file read"
	case "file.modified":
		return "Cline file modified"
	case "mcp.tool_invoked":
		return "Cline MCP tool executed"
	default:
		return "Cline tool completed"
	}
}

// clineWorkspacePath resolves a Cline tool path against the workspace root.
//
// Cline addresses files relative to the workspace -- read_file receives "src/app.ts", not an
// absolute path -- while every other runtime Beacon supports reports absolute paths. Left relative,
// a Cline file path is not comparable with the same file seen through Cursor or Claude Code, and
// threat rules that match on absolute paths never fire on Cline activity.
//
// Joining is skipped when the path is already absolute, and when no workspace root was resolved:
// a wrong root would be worse than a relative path, because it names a file that was never touched.
//
// Deliberately not implemented with filepath.IsAbs and filepath.Join, which answer for the host
// Beacon is running on rather than for the path in the payload. Those are different questions here:
// the hook binary runs on the host while Cline may be addressing a workspace somewhere else, which
// is the ordinary case for VS Code Remote and WSL. On Windows, filepath.IsAbs("/home/u/repo/a.ts")
// is false, so an absolute POSIX path was joined onto the workspace root and recorded as
// "\\tmp\\project\\home\\u\\repo\\a.ts" -- a file nothing touched. Answering for the
// path itself also makes the result identical on every host, which is what lets one test pin it.
func clineWorkspacePath(path, root string) string {
	path = hookdiff.NormalizePath(path)
	if path == "" || root == "" || isRootedPath(path) {
		return path
	}
	return joinWorkspacePath(root, path)
}

// isRootedPath reports whether a path names a location without needing a base, under either
// platform's convention.
//
// Both conventions, always, because the payload decides the spelling and the host does not. A
// Windows path treated as relative and a POSIX path treated as relative fail the same way: the
// workspace root is prepended and the event names a file that was never touched.
func isRootedPath(path string) bool {
	switch {
	case path == "":
		return false
	// POSIX absolute, a UNC share, and a Windows path rooted on the current volume.
	case path[0] == '/' || path[0] == '\\':
		return true
	// A drive-qualified Windows path: C:\repo or C:/repo. "C:repo" is deliberately excluded --
	// it is relative to that drive's working directory, not rooted.
	case len(path) >= 3 && path[1] == ':' && (path[2] == '\\' || path[2] == '/'):
		letter := path[0] | 0x20
		return letter >= 'a' && letter <= 'z'
	default:
		return false
	}
}

// joinWorkspacePath joins a relative path under a root using the root's own separator, then
// collapses . and .. segments so the result is canonical.
//
// filepath.Join would use the host's separator, which mangles the result whenever the two disagree:
// a POSIX workspace root on a Windows host produced "\\tmp\\project\\src\\app.ts", a path that
// matches nothing and belongs to no filesystem. Taking the separator from the root keeps the
// recorded path in the same shape as the workspace it came from.
func joinWorkspacePath(root, rel string) string {
	separator := "/"
	if strings.Contains(root, "\\") && !strings.Contains(root, "/") {
		separator = "\\"
	}
	// Cleaning happens in slash space with the volume held aside, then reattached. A volume prefix
	// cannot go through path.Clean: a leading "//" collapses, turning a UNC share into a directory,
	// and "C:" is an ordinary segment that ".." can walk past -- which turned "C:\repo" plus
	// "..\..\..\x.ts" into the bare relative "x.ts", an absolute Windows path with no drive left
	// on it.
	volume, rootRest := splitPathVolume(root)
	// A root of "C:" carries no separator for the rule above to read, but a drive letter is
	// unambiguously Windows, so the recorded path should be Windows-shaped rather than defaulting to
	// slashes. Only a bare drive reaches this: a UNC root always contains separators.
	if separator == "/" && volume != "" && !strings.ContainsAny(root, "/\\") {
		separator = "\\"
	}
	// A bare volume -- "\\\\server\\share" or "C:" with nothing after it -- leaves an empty remainder,
	// and path.Join drops empty elements, so the separator between root and relative path
	// disappeared: the share and the path ran together as "\\\\server\\sharesrc\\app.ts", naming a
	// file nothing touched. Substituting the root directory is also the right reading of the value:
	// a workspace root of "C:" means that drive's root, not a path relative to its working
	// directory.
	if volume != "" && rootRest == "" {
		rootRest = "/"
	}
	joined := path.Join(toSlashPath(rootRest), toSlashPath(rel))
	if separator != "/" {
		joined = strings.ReplaceAll(joined, "/", separator)
	}
	return volume + joined
}

// splitPathVolume separates a Windows volume prefix -- a drive letter or a UNC share -- from the
// rest of a path. Returns an empty volume for anything else, including every POSIX path.
func splitPathVolume(p string) (string, string) {
	if len(p) >= 2 && p[1] == ':' {
		if letter := p[0] | 0x20; letter >= 'a' && letter <= 'z' {
			return p[:2], p[2:]
		}
	}
	if len(p) >= 2 && isPathSeparator(p[0]) && isPathSeparator(p[1]) {
		rest := p[2:]
		server := indexPathSeparator(rest)
		if server < 0 {
			return p, ""
		}
		share := indexPathSeparator(rest[server+1:])
		if share < 0 {
			return p, ""
		}
		end := 2 + server + 1 + share
		return p[:end], p[end:]
	}
	return "", p
}

func toSlashPath(p string) string {
	return strings.ReplaceAll(p, "\\", "/")
}

func isPathSeparator(c byte) bool {
	return c == '/' || c == '\\'
}

func indexPathSeparator(p string) int {
	return strings.IndexAny(p, "/\\")
}

func clineToolPath(toolInput map[string]interface{}, root string) string {
	return clineWorkspacePath(
		firstToolString(toolInput, "path", "file_path", "filePath", "target_file", "targetFile"),
		root,
	)
}

func clineToolFields(input map[string]interface{}, completed bool) map[string]interface{} {
	toolName := clineToolName(input)
	toolInput := clineToolInput(input)
	toolResponse := clineToolResponse(input)
	fields := toolFieldsWithResponse(toolName, toolInput, toolResponse)

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
	if callID := toolCallIDFromEnvelope(input); callID != "" {
		call["id"] = callID
	}
	fields["gen_ai"] = map[string]interface{}{
		"operation": map[string]interface{}{"name": "execute_tool"},
		"tool": map[string]interface{}{
			"name": toolName,
			"call": call,
		},
	}
	if duration, ok := firstToolIntAcross([]map[string]interface{}{input, toolResponse}, "durationMs", "duration_ms"); ok {
		fields["tool"] = mergeNested(fields["tool"], map[string]interface{}{"duration_ms": duration})
	}

	// resolveCwd is asked for the cline shape explicitly rather than through platformFlag: this
	// function only ever runs on Cline payloads, and reading the flag would make the resolved path
	// depend on how the binary happened to be invoked.
	root := resolveCwd(input, "cline")
	path := clineToolPath(toolInput, root)
	if path != "" {
		operation := clineFileOperation(toolName)
		if operation == "" {
			// A path on a tool that does nothing to files -- a search root, a project directory --
			// is not file activity, and recording it as such would put unread files in the log.
			delete(fields, "file")
			if tool, ok := fields["tool"].(map[string]interface{}); ok {
				delete(tool, "path")
			}
		} else {
			fields["file"] = map[string]interface{}{
				"path":      path,
				"operation": operation,
				"language":  strings.TrimPrefix(filepath.Ext(path), "."),
			}
			fields["tool"] = mergeNested(fields["tool"], map[string]interface{}{"name": toolName, "path": path})
		}
	}

	if completed {
		action, _ := clineToolAction(toolName)
		if action == "file.modified" && path != "" {
			mergeMap(fields, clineDiffFields(toolName, toolInput, toolResponse, path))
			if file, ok := fields["file"].(map[string]interface{}); ok {
				// diffFields records every diff as a modification; a create stays a create.
				file["operation"] = clineFileOperation(toolName)
			}
		}
		if action == "command.executed" {
			fields["command"] = clineCommandFields(input, toolInput, toolResponse)
		}
	}

	if encoded, err := json.Marshal(map[string]interface{}{"input": toolInput, "response": toolResponse}); err == nil && len(encoded) > 0 {
		fields["content"] = retainedContentFields(string(encoded))
	}
	return fields
}

// clineDiffFields builds the file diff for a mutation.
//
// write_to_file goes through the shared builder, which turns its `content` argument into an
// added-lines diff. replace_in_file deliberately does not: its `diff` argument is already a diff,
// so passing it through records what Cline actually applied instead of an approximation
// reconstructed from the pieces.
//
// The path is only used for the event field. The shared builder names files by base name in diff
// headers, the same for every runtime, so it needs nothing resolved.
func clineDiffFields(toolName string, toolInput, toolResponse map[string]interface{}, path string) map[string]interface{} {
	diffText := hookdiff.FromToolResponse(toolName, toolInput, toolResponse)
	if diffText == "" {
		diffText = firstToolString(toolInput, "diff", "content", "new_string", "newString")
	}
	return diffFields(path, diffText)
}

func clineCommandFields(input, toolInput, toolResponse map[string]interface{}) map[string]interface{} {
	fields := map[string]interface{}{"command": firstToolString(toolInput, "command", "cmd")}
	if output := firstToolString(toolResponse, "output", "stdout", "text", "result"); output != "" {
		fields["output"] = output
	}
	if exitCode, ok := firstToolIntAcross([]map[string]interface{}{toolResponse, input}, "exit_code", "exitCode"); ok {
		fields["exit_code"] = exitCode
	}
	if duration, ok := firstToolIntAcross([]map[string]interface{}{input, toolResponse}, "durationMs", "duration_ms"); ok {
		fields["duration_ms"] = duration
	}
	return fields
}

func clineToolAfterEvents(input map[string]interface{}, fields map[string]interface{}) []normalizedEvent {
	mergeMap(fields, clineToolFields(input, true))
	if errText, failed := clineToolFailure(input); failed {
		fields["error"] = map[string]interface{}{"type": clineToolErrorType(input)}
		if errText != "" {
			fields["content"] = retainedContentFields(errText)
		}
		return []normalizedEvent{{
			action: "tool.failed", category: "tool", severity: "high",
			message: "Cline tool failed", fields: fields,
		}}
	}
	action, category := clineToolAction(clineToolName(input))
	// A file action with no file is not a file action. Cline's read tools accept a directory or a
	// pattern instead of a path, and reporting those as file.read with no file field produces a row
	// that every file-scoped query matches and none can explain.
	if strings.HasPrefix(action, "file.") {
		if _, ok := fields["file"]; !ok {
			action, category = "tool.completed", "tool"
		}
	}
	return []normalizedEvent{{
		action: action, category: category, severity: "info",
		message: clineToolMessage(action), fields: fields,
	}}
}

// Outcomes a run-completion payload can report, beyond plain success.
const (
	clineOutcomeCancelled = "cancelled"
	clineOutcomeFailed    = "failed"
)

// clineTaskOutcome reads how a Cline task actually finished.
//
// This exists because the two Cline surfaces disagree about where the outcome lives. The
// file-based hooks name it in the stage itself -- TaskCancel, error -- so the stage alone is
// enough. The plugin SDK, which is the surface Beacon installs, has a single run-completion
// handler for every outcome and reports the difference as a field on the context. Without this
// read, an aborted or failed task reaches the log as a clean session.ended, and the cancel and
// error paths below would be unreachable through Beacon's own plugin.
//
// The documented values are Cline's own: AgentRunResult.status is completed | aborted | failed.
// Those three are what the plugin surface sends. The extra spellings accepted below are tolerance
// for the file-based surface, whose status-like fields are free-form -- its stage is literally
// named TaskCancel -- and they cost nothing to accept.
//
// Returns "" when nothing says otherwise, so a payload that reports no outcome stays a success --
// the same behavior as before this read existed. An unrecognized value is also a success rather
// than a guess: mislabelling a completed task as failed is worse than missing a label, and a
// status Cline adds later must not turn every finished task into an incident.
func clineTaskOutcome(input map[string]interface{}) string {
	status := getFirstStr(input, "status", "outcome", "runStatus", "run_status", "completionStatus", "completion_status")
	if status == "" {
		for _, key := range []string{"result", "run", "task", "taskMetadata", "task_metadata", "metadata"} {
			if nested := firstMap(input, key); nested != nil {
				status = getFirstStr(nested, "status", "outcome", "runStatus", "run_status", "completionStatus", "completion_status")
				if status != "" {
					break
				}
			}
		}
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	// "aborted" and "failed" are the documented enum; the rest are file-hook tolerance.
	case "aborted", "cancelled", "canceled":
		return clineOutcomeCancelled
	case "failed", "failure", "error":
		return clineOutcomeFailed
	}
	// Deliberately no fallback to "there is an error object on the payload". clineToolFailure
	// answers that question for a tool call, where the error belongs to the call itself; a run
	// context is not the same thing, and a run that carried an error field for any other reason
	// would be reported as a failed task. A false session.error is worse than a missed one in a
	// log people alert on, so the status field is the only signal until a capture shows another.
	return ""
}

// clineCancelEvents and clineErrorEvents keep the cancel and failure shapes in one place, since
// both the stage-named surface and the outcome-carrying one have to produce them identically.
func clineCancelEvents(input, fields map[string]interface{}, one func(action, category, severity, message string, values map[string]interface{}) []normalizedEvent) []normalizedEvent {
	if usage := clineUsage(input); len(usage) > 0 {
		fields["gen_ai"] = mergeNested(fields["gen_ai"], map[string]interface{}{"usage": usage})
	}
	fields["session"] = mergeNested(fields["session"], map[string]interface{}{"cancel_reason": clineCancelReason(input)})
	return one("session.ended", "session", "info", "Cline task cancelled", fields)
}

func clineErrorEvents(input, fields map[string]interface{}, one func(action, category, severity, message string, values map[string]interface{}) []normalizedEvent) []normalizedEvent {
	// Usage is recorded on a failed task too: the tokens were spent whether or not the task
	// finished, and dropping them would understate the task's cost.
	if usage := clineUsage(input); len(usage) > 0 {
		fields["gen_ai"] = mergeNested(fields["gen_ai"], map[string]interface{}{"usage": usage})
	}
	fields["error"] = map[string]interface{}{"type": clineErrorType(input)}
	return one("session.error", "session", "high", "Cline task ended with an error", fields)
}

// clineCancelReason reports why a task ended without completing: cancelled, abandoned, or whatever
// else the payload says.
//
// Searched across the nestings Cline's file-based hook payload is reported to use -- notably
// taskCancel.taskMetadata.completionStatus -- as well as the top level, because the two hook
// surfaces do not agree on shape. Those paths come from a review of this change rather than from a
// payload captured from a running Cline, so the search is additive: a miss costs the
// cancelled-versus-abandoned distinction and falls back to "cancelled", never to a wrong reason.
//
// A named function rather than a loop inside the event switch so it can be tested directly, which
// is what a set of guessed field paths most needs.
func clineCancelReason(input map[string]interface{}) string {
	if reason := getFirstStr(input, "completionStatus", "completion_status", "reason"); reason != "" {
		return reason
	}
	outers := []map[string]interface{}{input}
	for _, key := range []string{"taskCancel", "task_cancel", "taskMetadata", "task_metadata", "result", "metadata"} {
		if nested := firstMap(input, key); nested != nil {
			outers = append(outers, nested)
		}
	}
	for _, outer := range outers {
		if reason := getFirstStr(outer, "completionStatus", "completion_status", "reason"); reason != "" {
			return reason
		}
		inner := firstMap(outer, "taskMetadata", "task_metadata", "metadata")
		if reason := getFirstStr(inner, "completionStatus", "completion_status", "reason"); reason != "" {
			return reason
		}
	}
	// A cancel recognized from the run status rather than the stage name has that status as its
	// only stated reason, and "aborted" says more than the generic default does.
	for _, outer := range outers {
		if status := getFirstStr(outer, "status", "outcome", "runStatus", "run_status"); status != "" {
			return status
		}
	}
	return "cancelled"
}

// clineUsage normalizes Cline's reported token counts into gen_ai.usage.
//
// Read from the task-end payload only, and deliberately not from any per-model-call stage. Beacon's
// token rollups sum gen_ai.usage across events, so emitting usage at both granularities would count
// every token twice. Cline's documented usage lives on the run result, which makes task-end the one
// place it can be read without guessing at a field path.
//
// Cost is taken only as Cline reports it. Beacon never derives cost from a local price table.
func clineUsage(input map[string]interface{}) map[string]interface{} {
	usage := firstMap(input, "usage", "tokens")
	if usage == nil {
		for _, key := range []string{"result", "run", "output", "metrics"} {
			if nested := firstMap(input, key); nested != nil {
				if usage = firstMap(nested, "usage", "tokens"); usage != nil {
					break
				}
			}
		}
	}
	if usage == nil {
		return nil
	}
	sources := []map[string]interface{}{usage}
	out := map[string]interface{}{}
	if value, ok := firstToolIntAcross(sources, "inputTokens", "input_tokens", "tokensIn", "tokens_in", "promptTokens", "prompt_tokens"); ok {
		out["input_tokens"] = value
	}
	if value, ok := firstToolIntAcross(sources, "outputTokens", "output_tokens", "tokensOut", "tokens_out", "completionTokens", "completion_tokens"); ok {
		out["output_tokens"] = value
	}
	if value, ok := firstToolIntAcross(sources, "cacheReadTokens", "cache_read_tokens", "cachedTokens", "cached_tokens"); ok {
		out["cache_read"] = map[string]interface{}{"input_tokens": value}
	}
	if value, ok := firstToolIntAcross(sources, "cacheWriteTokens", "cache_write_tokens", "cacheCreationTokens", "cache_creation_tokens"); ok {
		out["cache_creation"] = map[string]interface{}{"input_tokens": value}
	}
	if value, ok := clineCost(usage, input); ok {
		out["cost_usd"] = value
	}
	return out
}

func clineCost(sources ...map[string]interface{}) (float64, bool) {
	for _, source := range sources {
		if source == nil {
			continue
		}
		for _, key := range []string{"totalCost", "total_cost", "costUsd", "cost_usd", "cost"} {
			if value, ok := jsonFloat(source[key]); ok {
				return value, true
			}
		}
	}
	return 0, false
}
