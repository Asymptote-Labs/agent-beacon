package cmd

import (
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/config"
	hookdiff "github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/diff"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/logging"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// Kiro payload readers and tool taxonomy.
//
// Kiro (AWS) runs one agent harness behind the Kiro IDE, the Kiro CLI, Web, Mobile and Crew, and
// the local surfaces all load the same `.kiro/hooks/*.json` files. Its hook envelope is spelled
// exactly the way Claude Code spells its own:
//
//	{"hook_event_name": "preToolUse", "cwd": "...", "session_id": "...",
//	 "tool_name": "read", "tool_input": {...}, "tool_response": {...},
//	 "prompt": "...", "assistant_response": "..."}
//
// So Kiro rides the shared subcommands, the way OpenHands does and for the same reason: the shared
// path already carries the inventory heartbeat, log rotation, session state, the policy seam,
// content retention and the tool call id, and a second implementation of those would be the thing
// that drifts. There is not one envelope reader in this file, because there is nothing to
// translate -- `session_id`, `cwd` and `prompt` all resolve through the shared default cases.
//
// What is here is everything the envelope does not settle:
//
//   - The tool taxonomy. Kiro's tool names are published and its argument schemas are not, so the
//     classification below is keyed on names only. Every entry in the table comes from Kiro's own
//     tool catalog and its CLI tool reference, including the aliases, because a hook payload
//     carries whichever spelling the surface used.
//
//   - The read tool's arguments. `read`/`fs_read` takes an `operations` array rather than a `path`,
//     which is the one argument shape Kiro documents, and the shared path reader cannot see into
//     it.
//
//   - MCP. Kiro names an MCP tool `@server/tool`, which contains none of the signals the shared
//     MCP detection looks for.
//
//   - Stdout. On Kiro a hook's stdout is not a response object, it is text handed to the model.
//     See hookStdoutIsConsumedAsAgentContext.
//
// Deliberately not read: `hook_event_name`. Beacon binds each Kiro event to a distinct subcommand
// in the hooks file it writes, so the command that is running already knows which event it is
// answering.
//
// Deliberately not retained under `raw`: the envelope has no field left over once the readers
// below have run, so keeping the whole payload would duplicate `tool_input` and `tool_response`
// -- file contents included -- to preserve nothing.

const kiroPlatform = "kiro"

// kiroToolKind is what one Kiro tool does, as far as the endpoint schema is concerned.
//
// One table of names rather than the four sets the OpenHands taxonomy uses. Kiro publishes an
// explicit catalog of about thirty tools, most of them with one or two aliases, and four
// overlapping sets over that many names invites the failure where a name lands in two of them and
// the answer depends on which set is consulted first. A single map cannot express that.
type kiroToolKind int

const (
	// kiroToolOther is a tool with no filesystem or shell meaning. It is not absent from the
	// table: an entry saying "this is a tool call and nothing more" is what keeps a future
	// substring rule from guessing something else about it, and it documents that the tool was
	// considered.
	kiroToolOther kiroToolKind = iota
	kiroToolRead
	kiroToolCreate
	kiroToolModify
	kiroToolDelete
	kiroToolShell
)

// kiroToolKinds maps every published Kiro tool name and alias onto what it does.
//
// Two catalogs are merged here because Kiro ships two and they disagree. The unified tool catalog
// lists file writes split across `fs_write`, `fs_append`, `str_replace` and `delete_file`, while
// the CLI's own tool reference lists a single `write` tool with `fs_write`/`fsWrite` as its
// aliases. Rather than pick one, both are present: a hook installed once receives payloads from
// whatever surface and whatever version the operator is running, and a name that is not in this
// table falls through to the generic classifier, which is wrong about most of them.
//
// The names are matched lowercased and exactly. There is no prefix or substring rule, and the
// generic classifier below is the reason: `execute_bash` contains none of the words that classifier
// looks for ("bash" only as a whole name, "shell", "terminal", "command"), so Kiro's shell tool
// would be recorded as an unclassified `tool.invoked` without an entry here -- a shell execution
// missing from every query that looks for one. `list_processes` and `get_process_output` are the
// mirror case: they read process state rather than run anything, and are entered as `other` so a
// later substring rule cannot promote them to command executions.
var kiroToolKinds = map[string]kiroToolKind{
	// Reads. `glob`, `grep`, `file_search` and `grep_search` take a directory to search rather
	// than a file that was read; recording that directory under file.path with operation "read" is
	// the same shape the OpenHands and Qwen taxonomies already produce, and it is the honest one:
	// a search did happen, and the directory is what it touched.
	"read":           kiroToolRead,
	"fs_read":        kiroToolRead,
	"fsread":         kiroToolRead,
	"read_file":      kiroToolRead,
	"read_files":     kiroToolRead,
	"list_directory": kiroToolRead,
	"file_search":    kiroToolRead,
	"glob":           kiroToolRead,
	"grep_search":    kiroToolRead,
	"grep":           kiroToolRead,
	// `code` and `read_code` parse source into symbols and signatures. They read files and change
	// nothing, so they are reads: the CLI's tree-sitter/LSP tool and the IDE's editor-native
	// equivalent.
	"code":      kiroToolRead,
	"read_code": kiroToolRead,

	// Writes. `write`/`fs_write` creates or overwrites, which is "create"; the rest change a file
	// that already exists, which is "modify". A `write` call that carries an editor command
	// argument is reclassified by kiroWriteOperation, since the Amazon Q Developer CLI lineage
	// multiplexed several operations onto that one tool.
	"write":       kiroToolCreate,
	"fs_write":    kiroToolCreate,
	"fswrite":     kiroToolCreate,
	"fs_append":   kiroToolModify,
	"str_replace": kiroToolModify,
	"delete_file": kiroToolDelete,

	// Shell. `control_bash_process` starts and stops background processes, which is command
	// execution; the two tools that inspect those processes are not.
	"shell":                kiroToolShell,
	"execute_bash":         kiroToolShell,
	"execute_cmd":          kiroToolShell,
	"control_bash_process": kiroToolShell,
	"get_process_output":   kiroToolOther,
	"list_processes":       kiroToolOther,

	// Everything else Kiro publishes. `aws`/`use_aws` is deliberately not shell: it does run the
	// AWS CLI, but its arguments are a service, an operation and a parameter map rather than a
	// command string, so classifying it as command.executed would produce a command event with no
	// command in it. Recorded as the tool call it is, with the tool name intact.
	"aws":              kiroToolOther,
	"use_aws":          kiroToolOther,
	"web_search":       kiroToolOther,
	"web_fetch":        kiroToolOther,
	"invoke_subagent":  kiroToolOther,
	"subagent":         kiroToolOther,
	"use_subagent":     kiroToolOther,
	"delegate":         kiroToolOther,
	"disclose_context": kiroToolOther,
	"introspect":       kiroToolOther,
	"knowledge":        kiroToolOther,
	"thinking":         kiroToolOther,
	"todo":             kiroToolOther,
	"todo_list":        kiroToolOther,
	"goal":             kiroToolOther,
	"session":          kiroToolOther,
	"session_settings": kiroToolOther,
	"tool_search":      kiroToolOther,
	"create_hook":      kiroToolOther,
	"kiro_powers":      kiroToolOther,
	"report":           kiroToolOther,
}

// kiroWriteCommands are the editor operations a multiplexed write tool selects between.
//
// Kiro does not publish its write tool's arguments. What it does publish is that `fs_write` and
// `fsWrite` are aliases of one `write` tool, which is the shape Amazon Q Developer CLI shipped --
// Kiro CLI's own docs carry an "Upgrading from Q CLI" guide -- and there that tool took a
// `command` argument naming the operation. The newer unified catalog instead lists the operations
// as separate tools. Both shapes are handled, because a hook installed once sees whatever the
// installed version sends.
//
// This set has a second job, and it is the one that would bite: `command` is also the argument
// name Kiro's shell tool uses for the command line. Without knowing that a write's `command` names
// an operation rather than a shell command, the shared field extraction would write "str_replace"
// into command.command and let the policy seam upgrade the call from tool.invoked to
// command.executed. That is the OpenHands `file_editor` defect exactly, guarded the same way.
var kiroWriteCommands = map[string]bool{
	"create":      true,
	"str_replace": true,
	"insert":      true,
	"append":      true,
	"delete":      true,
	"undo_edit":   true,
}

// kiroMultiplexedWriteTools are the tool names whose `command` argument may name an editor
// operation rather than a shell command.
//
// Only the write tool and its aliases. Deliberately not every Kiro tool: `command` means a shell
// command line on `shell`/`execute_bash`, and a set that reached those would drop the one field
// that makes a shell event worth recording.
var kiroMultiplexedWriteTools = map[string]bool{
	"write":    true,
	"fs_write": true,
	"fswrite":  true,
}

// kiroMCPToolPrefix is what Kiro puts in front of an MCP tool's name.
//
// Kiro names an MCP tool `@server/tool` -- the documented example is `@postgres/query`. None of
// the shared MCP signals see that: the name contains no "mcp", the arguments are the server's own
// and carry no mcp_* key, and the result is whatever the server returned. Without this the call
// would be recorded as an ordinary tool.invoked with a tool name starting with a punctuation mark.
const kiroMCPToolPrefix = "@"

// hookStdoutIsConsumedAsAgentContext reports whether a runtime feeds a hook's stdout to the model
// rather than parsing it as a response object.
//
// Kiro does. Its contract is exit codes, not JSON: on exit 0 the hook's stdout is *added to the
// agent's context* for SessionStart and UserPromptSubmit and ignored for everything else, and a
// block is exit code 2 with the reason on stderr. There is no response object to return.
//
// Beacon's shared commands all finish by writing a JSON object to stdout -- `{}` for most of them,
// `{"permission":"allow"}` for pre-tool -- which every other runtime reads as "no opinion". On
// Kiro those same bytes are not read at all on the tool events, and are pasted into the model's
// context on the two events that are. A hook whose job is to observe would be putting `{}` in
// front of the model at the start of every session and before every prompt: a small amount of
// unexplained text in the context window, contributed by a component the user installed to watch
// rather than to speak.
//
// So on Kiro nothing is written to stdout at all. Silence is a valid and complete answer under an
// exit-code contract, which is what makes this safe rather than merely tidy.
//
// Written as a question about the runtime rather than as `platform == kiroPlatform` inline,
// because it is a property other runtimes have -- Claude Code's SessionStart additionalContext
// works the same way -- and the next one belongs in this function, not in a second copy of the
// condition next to each writer.
func hookStdoutIsConsumedAsAgentContext(platform string) bool {
	return platform == kiroPlatform
}

// kiroToolKindFor classifies a Kiro tool by name, and reports whether the name is known at all.
//
// The second result matters: an unknown name is a tool this build has not seen -- an MCP tool, a
// tool added after this table was written -- and the caller falls through to the shared
// classifier rather than being told the tool does nothing.
func kiroToolKindFor(toolName string) (kiroToolKind, bool) {
	kind, ok := kiroToolKinds[strings.ToLower(strings.TrimSpace(toolName))]
	return kind, ok
}

// kiroWriteOperation reads the editor operation a multiplexed write call performed, or "" when the
// call is not one.
//
// Both the arguments and the result are consulted, because they answer at different times: the
// arguments carry the operation on PreToolUse, where there is no result yet, and a result that
// echoes it is available on PostToolUse. The arguments are preferred so the two phases classify
// the same call the same way.
//
// Guarded on the tool name and on the value being a known editor operation, in that order. Both
// guards are load-bearing: the name keeps a shell command line out of here, and the value set
// keeps a shell command that happened to be run through a mis-named tool from being read as an
// operation.
func kiroWriteOperation(toolName string, toolInput, toolResponse map[string]interface{}) string {
	if !kiroMultiplexedWriteTools[strings.ToLower(strings.TrimSpace(toolName))] {
		return ""
	}
	value := strings.ToLower(strings.TrimSpace(firstToolStringAcross(
		[]map[string]interface{}{toolInput, toolResponse}, "command", "mode")))
	if !kiroWriteCommands[value] {
		return ""
	}
	return value
}

// kiroWriteCommandIsEditorOperation reports whether a call's `command` argument names an editor
// operation rather than a shell command, so the shared field extraction can leave it alone.
func kiroWriteCommandIsEditorOperation(toolName string, toolInput map[string]interface{}) bool {
	return kiroWriteOperation(toolName, toolInput, nil) != ""
}

// kiroIsMCPToolName reports whether a tool name came from an MCP server.
//
// A leading `@` and a `/`. Both are required: `@builtin`, `@mcp` and `@powers` are matcher
// prefixes an operator writes in a hooks file, not tool names, and none of them carries a slash.
// Requiring the separator is what keeps one of those from being recorded as an MCP call with an
// empty tool.
func kiroIsMCPToolName(toolName string) bool {
	trimmed := strings.TrimSpace(toolName)
	if !strings.HasPrefix(trimmed, kiroMCPToolPrefix) {
		return false
	}
	server, tool, found := strings.Cut(strings.TrimPrefix(trimmed, kiroMCPToolPrefix), "/")
	// Both halves, not just the separator. `@/tool` and `@server/` are malformed rather than
	// namespaced, and accepting either would record an MCP call with a blank server or a blank
	// tool -- a row that looks like MCP activity and names nothing.
	return found && strings.TrimSpace(server) != "" && strings.TrimSpace(tool) != ""
}

// kiroMCPServerTool splits `@server/tool` into its two halves.
//
// The tool half keeps any further slashes rather than being split again, because the separator is
// documented between the server and the tool and a server is free to name a tool with a slash in
// it. Splitting greedily would rename the tool.
func kiroMCPServerTool(toolName string) (server, tool string, ok bool) {
	if !kiroIsMCPToolName(toolName) {
		return "", "", false
	}
	server, tool, _ = strings.Cut(strings.TrimPrefix(strings.TrimSpace(toolName), kiroMCPToolPrefix), "/")
	return strings.TrimSpace(server), strings.TrimSpace(tool), true
}

// kiroToolAction maps a Kiro tool call onto an endpoint event action, or returns "" to let the
// generic classifier decide.
//
// MCP is asked first, before the name table, because an MCP server chooses its own tool names and
// nothing stops one from being called `read`. A name that says it came from a server is a
// statement by the runtime; a name that happens to match a built-in is a coincidence. Kiro is the
// easy case for this -- its MCP names carry a prefix a built-in cannot have -- but the ordering is
// the same one the OpenHands taxonomy needed for a harder version of the question.
func kiroToolAction(toolName string, toolInput, toolResponse map[string]interface{}) string {
	if kiroIsMCPToolName(toolName) {
		return "mcp.tool_invoked"
	}
	kind, known := kiroToolKindFor(toolName)
	if !known {
		return ""
	}
	if operation := kiroWriteOperation(toolName, toolInput, toolResponse); operation != "" {
		kind = kiroWriteKindForOperation(operation)
	}
	switch kind {
	case kiroToolRead:
		return "file.read"
	case kiroToolCreate, kiroToolModify, kiroToolDelete:
		return "file.modified"
	case kiroToolShell:
		return "command.executed"
	default:
		// A known tool with no filesystem or shell meaning. "" hands it to the shared classifier,
		// whose tool.invoked fallback is the right answer -- and which is also where an
		// MCP-flavored name would still be caught if one ever reached here.
		return ""
	}
}

// kiroWriteKindForOperation maps a multiplexed write's operation onto the write kind it is.
func kiroWriteKindForOperation(operation string) kiroToolKind {
	switch operation {
	case "create":
		return kiroToolCreate
	case "delete":
		return kiroToolDelete
	default:
		return kiroToolModify
	}
}

// kiroFileOperation is the `file.operation` value for a Kiro tool, or "" to fall through to the
// generic reader.
//
// Separate from kiroToolAction, as the Qwen and OpenHands pairs are, because the two answer
// different questions: an action says which event this is, an operation says what happened to the
// file. They also disagree here in a way that matters. `delete_file` is a file event, so its
// action belongs in the file family, but "delete" is not a modification and forcing it into
// "create" or "modify" would make a deletion unreadable as one. fx settled that the same way:
// the action stays in the family and the operation keeps its own word.
func kiroFileOperation(toolName string, toolInput, toolResponse map[string]interface{}) string {
	if kiroIsMCPToolName(toolName) {
		return ""
	}
	kind, known := kiroToolKindFor(toolName)
	if !known {
		return ""
	}
	if operation := kiroWriteOperation(toolName, toolInput, toolResponse); operation != "" {
		kind = kiroWriteKindForOperation(operation)
	}
	switch kind {
	case kiroToolRead:
		return "read"
	case kiroToolCreate:
		return "create"
	case kiroToolModify:
		return "modify"
	case kiroToolDelete:
		return "delete"
	default:
		return ""
	}
}

// isKiroFileEditTool reports whether a Kiro tool changes a file, which is what routes a post-tool
// payload down the diff path.
//
// A delete is excluded on purpose. It changes a file and it is recorded as a file event, but there
// is no content on either side of it to build a diff from, and sending it down that path would
// only make the diff builder return nothing after the payload had already been read as an edit.
func isKiroFileEditTool(toolName string, toolInput map[string]interface{}) bool {
	if kiroIsMCPToolName(toolName) {
		return false
	}
	kind, known := kiroToolKindFor(toolName)
	if !known {
		return false
	}
	if operation := kiroWriteOperation(toolName, toolInput, nil); operation != "" {
		kind = kiroWriteKindForOperation(operation)
	}
	return kind == kiroToolCreate || kind == kiroToolModify
}

// kiroToolPath reads the file a call names, out of the one argument shape Kiro documents.
//
// `read`/`fs_read` takes `{"operations": [{"mode": "Line", "path": "..."}]}` rather than a `path`,
// so the shared reader -- which looks for a path at the top level of tool_input -- finds nothing
// and a read event carries no file at all. That is Kiro's most frequent tool.
//
// The first operation's path, when there are several. The endpoint schema's file.path is one
// string, so a multi-file read has to choose, and the alternative -- joining them -- would produce
// a value that is not a path and that no rule matching on file.path could use. What is lost is
// recorded honestly rather than hidden: the event still names the tool, and a reader can see from
// tool.name that a read happened, just not that it covered four files.
//
// Returns "" for anything else, which leaves the shared reader's own key list in charge.
func kiroToolPath(toolInput map[string]interface{}) string {
	operations, ok := toolInput["operations"].([]interface{})
	if !ok {
		return ""
	}
	for _, raw := range operations {
		operation, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if path := firstToolString(operation, "path", "file_path", "filePath"); path != "" {
			return path
		}
	}
	return ""
}

// kiroToolFailed reports whether a tool result describes a failure.
//
// `success` on the tool_response, which Kiro documents on its postToolUse example
// (`{"success": true, "result": [...]}`). Read only when the key is actually present and boolean:
// an absent `success` is not a failure, and treating it as one would record every tool whose
// result Kiro shapes differently as a high-severity failure.
//
// Deliberately not a shell command's exit code. Kiro reports a non-zero exit inside the result of
// a command that ran, which is an ordinary outcome the log should record as command.executed --
// the same call OpenHands' reader makes, and for the same reason: reading it as a failure would
// turn every failing test run and every grep that matched nothing into tool.failed at high
// severity.
func kiroToolFailed(toolResponse map[string]interface{}) bool {
	if toolResponse == nil {
		return false
	}
	value, ok := toolResponse["success"]
	if !ok {
		return false
	}
	switch success := value.(type) {
	case bool:
		return !success
	case string:
		return strings.EqualFold(strings.TrimSpace(success), "false")
	default:
		return false
	}
}

// kiroAssistantResponse reads the agent's final message off a Stop payload.
//
// `assistant_response` is Kiro's own field and the only place the agent's reply reaches a hook.
// Deliberately not added to a shared key list: "assistant_response" is unambiguous here, but the
// shared readers are consulted for every runtime and a key added there is a key read from payloads
// that mean something else by it.
func kiroAssistantResponse(input map[string]interface{}) string {
	return getFirstStr(input, "assistant_response")
}

// emitKiroAssistantResponse records the agent's final reply as its own event.
//
// The shared stop command records that a turn finished; it does not record what the agent said,
// because most runtimes do not tell it. Kiro does, on the same payload, and dropping it would
// throw away the one piece of model output this runtime puts on a hook -- the half of a session
// that prompt.submitted only covers the user's side of.
//
// Written in the OTel GenAI output-messages shape with the retention marker beside it, exactly as
// the agent-thought command writes reasoning, so a reader that already understands one understands
// the other. `agent.message` rather than `agent.reasoning`: this is the reply, not the thinking,
// and it is the action the fx and Devin Cloud mappers already use for the same thing.
func emitKiroAssistantResponse(logger *logging.Logger, input map[string]interface{}, sessionID string) {
	text := kiroAssistantResponse(input)
	if text == "" {
		return
	}
	fields := sessionFields(sessionID, input)
	fields["gen_ai"] = mergeNested(fields["gen_ai"], map[string]interface{}{
		"output": map[string]interface{}{"messages": asymptoteobserve.TextOutputMessages(text)},
	})
	fields["content"] = retainedContentFields(text)
	emitHookEvent(logger, "agent.message", "session", "info", "Agent response captured", input, fields)
}

// applyKiroToolResult attaches what the shared toolFieldsWithResponse cannot reach: the operation a
// write call performed, and a shell command's output.
//
// The operation has to be applied here rather than left to fileOperation because a multiplexed
// write's operation lives in its arguments, and the shared reader has already written its answer
// into the file field by the time this runs.
func applyKiroToolResult(fields map[string]interface{}, toolName string, toolInput, toolResponse map[string]interface{}) {
	if operation := kiroFileOperation(toolName, toolInput, toolResponse); operation != "" {
		if file, ok := fields["file"].(map[string]interface{}); ok {
			file["operation"] = operation
		}
	}
	kind, known := kiroToolKindFor(toolName)
	if !known || kind != kiroToolShell {
		return
	}
	output := kiroResultText(toolResponse)
	if output == "" {
		return
	}
	command := mutableChild(fields["command"])
	command["output"] = output
	fields["command"] = command
	// The retention marker describes the command output specifically, so it is not overwritten:
	// a caller that already retained something -- a diff -- has the more specific claim.
	if _, exists := fields["content"]; !exists {
		fields["content"] = retainedContentFields(output)
	}
}

// kiroResultText joins the text a tool returned.
//
// Kiro's documented tool_response is `{"success": true, "result": [...]}`, where result is a list.
// Its element type is not documented, so both shapes are read: a plain string, and an object with
// the text under one of the usual keys. Anything else is skipped rather than formatted, because
// the alternative is writing a Go rendering of a map into a field a person reads as output.
//
// A bare string `result` is read too, for the same reason the table above carries two catalogs:
// the shape is inferred from one example and the cost of being wrong is a missing field.
//
// The type assertions are deliberate rather than a call to the shared string reader. That reader
// normalizes any value through fmt.Sprint, so a `result` list would come back as Go's rendering of
// a slice -- `[ok  \tbeacon\t0.2s\n]`, brackets and all -- written into a field a person reads as
// command output.
func kiroResultText(toolResponse map[string]interface{}) string {
	if toolResponse == nil {
		return ""
	}
	for _, key := range []string{"result", "output", "stdout"} {
		if text, ok := toolResponse[key].(string); ok && text != "" {
			return text
		}
	}
	items, ok := toolResponse["result"].([]interface{})
	if !ok {
		return ""
	}
	var out strings.Builder
	for _, item := range items {
		switch value := item.(type) {
		case string:
			out.WriteString(value)
		case map[string]interface{}:
			if text := firstToolString(value, "text", "output", "content", "stdout"); text != "" {
				out.WriteString(text)
			}
		}
	}
	return out.String()
}

// parseKiroEdit turns one post-tool payload into the file edit it performed, or nil when it is not
// one.
//
// A single edit rather than the slice the OpenHands reader returns: Kiro's write tools each name
// one file, and there is no patch tool that commits several at once.
//
// nil is not a failure. It means this payload is not a recordable edit -- a read, a shell command,
// a delete, a write whose arguments this build could not read -- and the caller falls through to
// the observing path, which still records the tool call with its path and operation.
func parseKiroEdit(input map[string]interface{}, logger *logging.Logger) *evaluationParams {
	toolName := getFirstStr(input, "tool_name")
	toolInput := resolveToolInput(input)
	toolResponse := resolveToolResponse(input)

	if !isKiroFileEditTool(toolName, toolInput) {
		return nil
	}
	// A failure is not an edit. Kiro reports a failed write with the same tool name and the same
	// arguments as a successful one -- `success` on the result is the only thing that separates
	// them -- so without this guard a diff would be built from the content of a write that never
	// landed, and the log would assert a file changed when it did not. Same guard, same reason, as
	// the Qwen and OpenHands branches.
	if kiroToolFailed(toolResponse) {
		return nil
	}

	filePath := hookdiff.NormalizePath(kiroEditPath(toolInput, toolResponse))
	if filePath == "" {
		return nil
	}
	if !config.IsScannableFile(filePath) {
		logger.Debug("Skipping non-scannable file: " + filePath)
		return nil
	}
	diffStr := hookdiff.FromKiroWrite(kiroWriteOperation(toolName, toolInput, toolResponse), toolName, toolInput, toolResponse)
	if diffStr == "" {
		logger.Debug("Could not construct diff, skipping", "tool_name", toolName, "file_path", filePath)
		return nil
	}
	return &evaluationParams{
		sessionID: resolveSessionID(input, kiroPlatform),
		toolName:  toolName,
		filePath:  filePath,
		diffStr:   diffStr,
		// The taxonomy already answered this, and the shared diff path would otherwise write
		// "modify" for a file that did not exist a moment ago.
		fileOperation: kiroFileOperation(toolName, toolInput, toolResponse),
	}
}

// kiroEditPath resolves the file a write call targeted.
//
// The arguments first and the result second, both across the same key list, because Kiro publishes
// neither shape and the two plausible spellings -- `path` from the Amazon Q lineage, `file_path`
// from the ecosystem convention -- cost nothing to read together. The operations array is
// consulted last: a write does not use it, but a tool that multiplexes reads and writes onto one
// name would.
func kiroEditPath(toolInput, toolResponse map[string]interface{}) string {
	if path := firstToolStringAcross(
		[]map[string]interface{}{toolInput, toolResponse},
		"path", "file_path", "filePath", "Path", "absolute_path"); path != "" {
		return path
	}
	return kiroToolPath(toolInput)
}

// kiroBlockExitCode is the status a Kiro hook exits with to block the event that fired it.
//
// Two, and only two. Kiro documents exit 0 as success and exit 2 as a block on the three
// blockable triggers -- PreToolUse, UserPromptSubmit, PreTaskExec -- with STDERR returned to the
// agent as the reason. Every other non-zero code is an error: the operator sees a warning and the
// call proceeds. So a hook that failed and a hook that denied are distinguished by this number
// alone, which is why Beacon exits 0 on every path except a policy deny.
const kiroBlockExitCode = 2
