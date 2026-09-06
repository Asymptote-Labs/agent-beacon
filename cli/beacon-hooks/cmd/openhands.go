package cmd

import (
	"os"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/config"
	hookdiff "github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/diff"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/logging"
)

// OpenHands payload readers and tool taxonomy.
//
// OpenHands documents its hooks format as compatible with Claude Code's, and the six events line
// up one for one with commands this binary already has -- SessionStart, UserPromptSubmit,
// PreToolUse, PostToolUse, Stop, SessionEnd. That is why OpenHands rides the shared commands
// rather than getting a mapper of its own: the shared path already carries the inventory
// heartbeat, log rotation, session state, the policy seam, content retention and the tool call id,
// and a second implementation of those would be the thing that drifts.
//
// What "compatible" does not cover is the envelope's field names, which are the SDK's HookEvent
// model rather than Claude's:
//
//	{"event_type": "PreToolUse", "tool_name": "terminal",
//	 "tool_input": {...}, "tool_response": {...}, "message": "...",
//	 "session_id": "...", "working_dir": "...", "metadata": {}}
//
// Three of those differ from the Claude spelling the shared readers use: the working directory is
// `working_dir` and not `cwd`, the prompt is `message` and not `prompt`, and the event name is
// `event_type` and not `hook_event_name`. Only the first two are read here; the third is not read
// at all, because Beacon binds each OpenHands event to a distinct subcommand in the hooks file it
// writes, so the command that is running already knows which event it is answering.
//
// `session_id` and `tool_name`/`tool_input`/`tool_response` are spelled exactly as Claude spells
// them, so the shared readers find them with no branch, and there are deliberately none here.
//
// `metadata` is deliberately not retained. It is empty on five of the six events and carries
// `{"reason": "agent_finished"}` on Stop -- a constant the SDK passes at its single call site --
// so keeping the payload under `raw.openhands` the way the Qwen and Muse mappings do would cost
// the whole tool_input and tool_response on every event, file contents included, to preserve one
// value that is the same every time.
//
// tool_input and tool_response are pydantic model dumps rather than free-form JSON, which is what
// makes the mapping below precise: every one carries a `kind` discriminator naming the model
// class, an observation carries `content` as a list of typed parts, and an editor observation
// reports the file's content before and after rather than the edit instruction.

const openHandsPlatform = "openhands"

// openHandsCommandTools are the built-ins that execute a shell command.
//
// One entry, and the generic classifier in actionForTool would reach the same answer for it by
// substring ("terminal"). It is stated anyway because this map is what the OpenHands taxonomy is
// read from: a reader checking whether shell execution is captured should find the answer here,
// not have to work out which generic substring rule happens to fire.
var openHandsCommandTools = map[string]bool{
	"terminal": true,
}

// openHandsFileEditorTools are the built-ins that multiplex several file operations onto one tool
// name, selected by the action's `command` argument.
//
// They need their own set because a name alone cannot classify them. `file_editor` with
// command="view" is a read and with command="str_replace" is a write, and both arrive as the same
// tool_name -- so the generic classifier, which only ever sees the name, is wrong on one of the two
// whichever way it answers. planning_file_editor is the same tool over plan files: its action and
// observation subclass FileEditorAction and FileEditorObservation, so it takes the same commands
// and reports the same fields.
var openHandsFileEditorTools = map[string]bool{
	"file_editor":          true,
	"planning_file_editor": true,
}

// openHandsReadTools are the built-ins that read from the filesystem without changing it.
//
// `glob` and `grep` take a `path` that is a directory to search rather than a file that was read.
// Recording that directory under file.path with operation "read" is the same shape Qwen Code's
// `glob` and `grep_search` already produce, and it is the honest one: a search did happen, and the
// directory is what it touched.
var openHandsReadTools = map[string]bool{
	"read_file":      true,
	"list_directory": true,
	"glob":           true,
	"grep":           true,
}

// openHandsEditTools are the built-ins that create or modify a file under a name that says so.
//
// `edit` and `write_file` are the Gemini-compatible tool set OpenHands ships alongside its own
// editor; they are not in the default preset, and they are here because a hook installed once
// receives payloads from whatever tools the agent was configured with. Both report old_content and
// new_content on the observation, so they take the same diff path file_editor does.
//
// `edit` is a short, generic name that an MCP server could also use. That is survivable rather
// than ignored: openHandsToolAction asks whether the observation is an MCP result before it
// consults this map, so a post-tool payload for an MCP `edit` is classified as MCP. A pre-tool
// payload has no observation to ask, so it is classified as a file edit -- which is the same
// exposure the Qwen taxonomy carries for its own `edit`, and it costs a category rather than a
// missing event.
var openHandsEditTools = map[string]bool{
	"apply_patch": true,
	"write_file":  true,
	"edit":        true,
}

// openHandsMCPObservationKind is the `kind` discriminator on an MCP tool result.
//
// OpenHands does not prefix MCP tool names -- an MCP tool is called by whatever name its server
// gave it -- so the generic MCP detection, which looks for "mcp" in the tool name or for an
// mcp_* argument, sees nothing. The observation is what says so: every tool result is a pydantic
// dump carrying `kind` set to its model class name, and an MCP call returns MCPToolObservation.
//
// Matched exactly rather than by substring because `kind` is a class name from a closed set, not
// free text; a substring rule here would claim any future observation class whose name merely
// contained these letters.
const openHandsMCPObservationKind = "MCPToolObservation"

// openHandsWorkingDir resolves the directory the agent is working in.
//
// The payload field is `working_dir`, which the shared resolveCwd does not read. The environment
// fallback is not redundant with it: the SDK types HookEvent.working_dir as optional and a
// HookManager built without one leaves it null, while the executor always exports
// OPENHANDS_PROJECT_DIR from its own working directory. So the payload is preferred as the more
// specific statement and the variable covers the case where there is no statement at all.
//
// This value is what the git helpers resolve a repository and branch from, and it is written into
// every event as session.working_directory and repository, so an empty result costs three fields
// on every OpenHands event rather than one.
func openHandsWorkingDir(input map[string]interface{}) string {
	if cwd := getFirstStr(input, "working_dir", "workingDir", "cwd"); cwd != "" {
		return cwd
	}
	return strings.TrimSpace(os.Getenv("OPENHANDS_PROJECT_DIR"))
}

// openHandsPrompt reads the submitted prompt text.
//
// `message` is deliberately not added to the shared key list in runPromptSubmit. That list is
// consulted for every hook runtime, and `message` is an ordinary key that carries something other
// than the user's prompt in other runtimes' payloads -- a status line, a tool result, an error --
// so widening the shared list would put one of those into prompt.text for a runtime that has
// nothing to do with OpenHands.
func openHandsPrompt(input map[string]interface{}) string {
	return getFirstStr(input, "message")
}

// openHandsIsMCPToolCall reports whether a tool result came back from an MCP server.
func openHandsIsMCPToolCall(toolResponse map[string]interface{}) bool {
	return firstToolString(toolResponse, "kind") == openHandsMCPObservationKind
}

// openHandsFileEditorCommand reads which operation a file_editor call performed.
//
// Both sides are consulted because they answer at different times: the action carries `command` on
// PreToolUse, where there is no observation yet, and the observation echoes it on PostToolUse. The
// action is preferred so that the two phases classify the same call the same way.
//
// Only ever called for the tools in openHandsFileEditorTools. `command` means something entirely
// different on a terminal action -- it is the shell command -- and reading it for any other tool
// would classify a shell string as an editor operation.
func openHandsFileEditorCommand(toolInput, toolResponse map[string]interface{}) string {
	return strings.ToLower(firstToolStringAcross(
		[]map[string]interface{}{toolInput, toolResponse}, "command"))
}

// openHandsToolAction maps an OpenHands tool call onto an endpoint event action, or returns "" to
// let the generic classifier decide.
//
// Returning "" rather than a default is what keeps the tools with no filesystem or shell meaning
// -- think, finish, task_tracker, the browser set, invoke_skill, ask_oracle -- on the shared path,
// where the tool.invoked fallback is already the right answer for them.
//
// MCP is asked first because an MCP tool's name is chosen by whoever wrote the server and can
// collide with any built-in id in the maps below. A tool result that says it came from MCP is a
// statement by the runtime; a name that happens to match is a coincidence.
func openHandsToolAction(toolName string, toolInput, toolResponse map[string]interface{}) string {
	if openHandsIsMCPToolCall(toolResponse) {
		return "mcp.tool_invoked"
	}
	lower := strings.ToLower(strings.TrimSpace(toolName))
	switch {
	case openHandsFileEditorTools[lower]:
		switch openHandsFileEditorCommand(toolInput, toolResponse) {
		case "view":
			return "file.read"
		case "create", "str_replace", "insert", "undo_edit":
			return "file.modified"
		default:
			// A file_editor call whose command did not arrive, or is one this build has not seen.
			// tool.invoked says a tool ran and nothing about what it did to the file, which is
			// exactly what is known. Falling through to the generic classifier instead would let
			// the substring rules answer from the tool's name, and the name is what cannot
			// distinguish a view from a write on this runtime.
			return "tool.invoked"
		}
	case openHandsCommandTools[lower]:
		return "command.executed"
	case openHandsReadTools[lower]:
		return "file.read"
	case openHandsEditTools[lower]:
		return "file.modified"
	default:
		return ""
	}
}

// openHandsFileOperation is the `file.operation` value for an OpenHands tool, or "" to fall
// through to the generic reader.
//
// Separate from openHandsToolAction, as the Qwen pair is, because the two answer different
// questions and disagree in both directions on this runtime. The generic fileOperation matches
// substrings, so `file_editor` reads as an edit whatever command it ran -- a view would carry
// operation "modify" -- while `apply_patch` matches "patch" and gets the right answer by accident.
func openHandsFileOperation(toolName string, toolInput, toolResponse map[string]interface{}) string {
	lower := strings.ToLower(strings.TrimSpace(toolName))
	switch {
	case openHandsFileEditorTools[lower]:
		switch openHandsFileEditorCommand(toolInput, toolResponse) {
		case "view":
			return "read"
		case "create":
			return "create"
		case "str_replace", "insert", "undo_edit":
			return "modify"
		default:
			return ""
		}
	case openHandsReadTools[lower]:
		return "read"
	case lower == "write_file":
		return "create"
	case openHandsEditTools[lower]:
		return "modify"
	default:
		return ""
	}
}

// openHandsToolFailed reports whether a tool result describes a failure.
//
// `is_error` on the observation, and nothing else. In particular not a non-zero exit code: a
// terminal observation reports exit_code separately and leaves is_error false for a command that
// ran and returned non-zero, which is an ordinary outcome the log should record as
// command.executed with the code attached. Reading the code as a failure would replace every
// failing test run and every grep that matched nothing with tool.failed at high severity.
//
// What does set it is the tool itself failing -- a bad argument, a terminal session that could not
// be recovered, an editor that could not read the path.
func openHandsToolFailed(toolResponse map[string]interface{}) bool {
	if toolResponse == nil {
		return false
	}
	switch value := toolResponse["is_error"].(type) {
	case bool:
		return value
	case string:
		return strings.EqualFold(strings.TrimSpace(value), "true")
	default:
		return false
	}
}

// openHandsObservationText joins the text an observation returned to the model.
//
// An observation's `content` is a list of typed parts rather than a string: text parts carry
// `{"type": "text", "text": ...}` and an image part carries urls instead. Only the text is read.
// An image part is skipped rather than described, because the alternative is writing a
// base64 data URL -- often megabytes of it -- into an event field.
func openHandsObservationText(toolResponse map[string]interface{}) string {
	if toolResponse == nil {
		return ""
	}
	parts, ok := toolResponse["content"].([]interface{})
	if !ok {
		return ""
	}
	var out strings.Builder
	for _, part := range parts {
		item, ok := part.(map[string]interface{})
		if !ok {
			continue
		}
		if partType := firstToolString(item, "type"); partType != "" && partType != "text" {
			continue
		}
		text, ok := item["text"].(string)
		if !ok || text == "" {
			continue
		}
		out.WriteString(text)
	}
	return out.String()
}

// applyOpenHandsToolResult attaches what the shared toolFieldsWithResponse cannot reach: the
// operation an editor call performed, and a shell command's exit code and output.
//
// The exit code and output live on the observation rather than in the tool's arguments, which is
// where toolFieldsWithResponse looks for the command itself, so without this a terminal event
// records what was run and nothing about how it went.
func applyOpenHandsToolResult(fields map[string]interface{}, toolName string, toolInput, toolResponse map[string]interface{}) {
	if operation := openHandsFileOperation(toolName, toolInput, toolResponse); operation != "" {
		if file, ok := fields["file"].(map[string]interface{}); ok {
			file["operation"] = operation
		}
	}
	if !openHandsCommandTools[strings.ToLower(strings.TrimSpace(toolName))] {
		return
	}
	command := mutableChild(fields["command"])
	// exit_code is optional on the observation and null while a command is still running or was
	// interrupted, so jsonInt's second result is what separates "absent" from a real 0. Writing a
	// zero for an absent code would report every unfinished command as a clean success.
	if exitCode, ok := jsonInt(toolResponse["exit_code"]); ok {
		command["exit_code"] = exitCode
	}
	if output := openHandsObservationText(toolResponse); output != "" {
		command["output"] = output
		// The retention marker describes the command output specifically, so it is only set when
		// there is output to describe -- and it is not overwritten, because a caller that already
		// retained something (a diff) has the more specific claim.
		if _, exists := fields["content"]; !exists {
			fields["content"] = retainedContentFields(output)
		}
	}
	if len(command) > 0 {
		fields["command"] = command
	}
}

// parseOpenHandsEdits turns one post-tool payload into the file edits it performed.
//
// A slice rather than the single *evaluationParams the Claude path returns, because one
// apply_patch call legitimately rewrites several files and reports them together. Returning only
// the first would record one edit and silently drop the rest, which is worse than recording none:
// the event would assert that a patch touched one file when it touched four.
//
// An empty result is not a failure. It means this payload is not a file edit -- a view, a
// terminal call, a tool whose observation reports no content change -- and the caller falls
// through to the observing path, which still records the tool call.
func parseOpenHandsEdits(input map[string]interface{}, logger *logging.Logger) []*evaluationParams {
	toolName := getFirstStr(input, "tool_name")
	toolInput := resolveToolInput(input)
	toolResponse := resolveToolResponse(input)

	// A failure is not an edit. The editor reports a failed str_replace with the same tool name
	// and the same arguments as a successful one; without this guard a diff would be built from
	// the content of a write that never landed, and the log would assert a file changed when it
	// did not. Same guard, same reason, as the Qwen branch in parseClaudeCopilotInput.
	if openHandsToolFailed(toolResponse) {
		return nil
	}

	lower := strings.ToLower(strings.TrimSpace(toolName))
	switch {
	case lower == "apply_patch":
		return openHandsPatchEdits(input, toolName, toolResponse, logger)
	case openHandsFileEditorTools[lower], openHandsEditTools[lower]:
		if openHandsToolAction(toolName, toolInput, toolResponse) != "file.modified" {
			return nil
		}
		path := firstToolStringAcross(
			[]map[string]interface{}{toolResponse, toolInput}, "path", "file_path")
		oldContent := firstToolString(toolResponse, "old_content")
		newContent := firstToolString(toolResponse, "new_content")
		if edit := openHandsContentEdit(input, toolName, path, oldContent, newContent, logger); edit != nil {
			return []*evaluationParams{edit}
		}
		return nil
	default:
		return nil
	}
}

// openHandsPatchEdits reads the per-file changes an apply_patch call committed.
//
// The patch body in the action is not parsed. The observation already reports what was applied,
// keyed by path, with each file's content before and after -- so reading the commit answers with
// what landed on disk, while parsing the request would answer with what was asked for, and the two
// differ whenever the patch applied with fuzz.
func openHandsPatchEdits(input map[string]interface{}, toolName string, toolResponse map[string]interface{}, logger *logging.Logger) []*evaluationParams {
	commit := firstMap(toolResponse, "commit")
	changes := firstMap(commit, "changes")
	if len(changes) == 0 {
		return nil
	}
	var edits []*evaluationParams
	for path, raw := range changes {
		change, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		// A rename reports the source path as the key and the destination under move_path. The
		// edit is recorded against the destination, because that is the file that exists
		// afterwards and the one an investigator would look for.
		target := path
		if movePath := firstToolString(change, "move_path"); movePath != "" {
			target = movePath
		}
		edit := openHandsContentEdit(
			input, toolName, target,
			firstToolString(change, "old_content"),
			firstToolString(change, "new_content"),
			logger)
		if edit != nil {
			edits = append(edits, edit)
		}
	}
	return edits
}

// openHandsContentEdit builds one recordable edit from a path and the file's content on either
// side of it, or nil when there is nothing to record.
func openHandsContentEdit(input map[string]interface{}, toolName, path, oldContent, newContent string, logger *logging.Logger) *evaluationParams {
	path = hookdiff.NormalizePath(path)
	if path == "" {
		return nil
	}
	if !config.IsScannableFile(path) {
		logger.Debug("Skipping non-scannable file: " + path)
		return nil
	}
	diffStr := hookdiff.FromContentChange(path, oldContent, newContent)
	if diffStr == "" {
		logger.Debug("Could not construct diff, skipping", "tool_name", toolName, "file_path", path)
		return nil
	}
	return &evaluationParams{
		sessionID: resolveSessionID(input, openHandsPlatform),
		toolName:  toolName,
		filePath:  path,
		diffStr:   diffStr,
	}
}
