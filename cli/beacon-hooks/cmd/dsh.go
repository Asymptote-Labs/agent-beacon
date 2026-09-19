package cmd

import (
	"strconv"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/config"
	hookdiff "github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/diff"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/logging"
)

// DeepSeek Harness payload readers and tool taxonomy.
//
// DeepSeek Harness (`dsh`) is a plugin-composed TypeScript agent runtime. It reaches Beacon
// through a bridge DeepSeek ships in the CLI, `@deepseek-ai/dsh-hooks-claude-code`, whose whole
// purpose is to run an existing Claude Code hooks file against the harness's own extension points.
// So the envelope is not merely similar to Claude Code's, it is Claude Code's, field for field:
//
//	{"hook_event_name": "PreToolUse", "session_id": "...", "transcript_path": "",
//	 "cwd": "...", "tool_name": "bash", "tool_input": {...}, "tool_use_id": "...",
//	 "tool_response": "...", "prompt": "...", "source": "...",
//	 "agent_id": "...", "agent_type": "general-purpose", "stop_hook_active": false}
//
// dsh therefore rides the shared subcommands, the way Kiro and OpenHands do and for the same
// reason: the shared path already carries log rotation, session state, the policy seam, content
// retention and the tool call id, and a second implementation of those is the thing that drifts.
// There is no envelope reader in this file, because there is nothing to translate -- `session_id`,
// `cwd`, `prompt`, `tool_use_id` and the subagent pair all resolve through the shared default
// cases. MCP is the same story: dsh names an MCP tool `mcp__<server>__<tool>`, which is the
// spelling the shared detection and the shared server/tool split already read.
//
// What is here is everything the envelope does not settle:
//
//   - The tool taxonomy. dsh publishes a generated tool catalog with both the names and the full
//     argument schemas, so unlike Kiro this table is backed by schemas rather than inferred from
//     names -- and it is exhaustive over what the catalog lists rather than a best guess.
//
//   - `str_replace_editor`, which multiplexes view/create/str_replace/insert onto a `command`
//     argument. That is the OpenHands `file_editor` and Kiro `fs_write` hazard exactly, and it is
//     guarded the same way.
//
//   - `terminal_send`, whose command line is named `text`. Nothing in the shared reader's key list
//     finds it, so a persistent-terminal command would be recorded with no command in it.
//
//   - The flattened result. The bridge collapses a tool result to text before it reaches the hook
//     (`tool_response` is a string, not an object), so a shell command's output arrives as one
//     string and the exit code arrives inside it, as the `[exit code: N]` marker dsh's bash and
//     pwsh tools document.
//
// Deliberately not read: `hook_event_name`. Beacon binds each dsh event to a distinct subcommand
// in the hooks file it writes, so the command that is running already knows which event it is
// answering.
//
// Deliberately not read: `transcript_path`. The bridge always sends the empty string -- the
// harness's persistence seam exposes no artifact path, and its default session log is Zstandard
// compressed and not readable by a hook script. The shared default reader asks for the key and
// gets "", which is the correct answer rather than a gap to work around.
//
// Not collected, because the bridge exposes none of it on a hook: token usage and cost, a
// session-end event (dsh has none; `Stop` is the closing event and fires per turn), operator
// approval decisions (there is no PermissionRequest event -- see the pre-tool observing branch),
// compaction, and a per-tool error flag. That last one is why this file has no failure predicate:
// the bridge hands a hook `blocksToText(result.content)` and nothing else, so a tool that failed
// and a tool that succeeded are indistinguishable at the hook, and inventing a `tool.failed` from
// a non-zero shell exit would turn every failing test run into a high-severity event -- the call
// the Kiro and OpenHands readers already settled the same way.

const dshPlatform = "dsh"

// dshToolKind is what one dsh tool does, as far as the endpoint schema is concerned.
type dshToolKind int

const (
	// dshToolOther is a tool with no filesystem or shell meaning. It is not absent from the table:
	// an entry saying "this is a tool call and nothing more" is what keeps a future substring rule
	// from guessing something else about it, and it records that the tool was considered.
	dshToolOther dshToolKind = iota
	dshToolRead
	dshToolCreate
	dshToolModify
	dshToolShell
)

// dshToolKinds maps every tool in dsh's generated tool catalog onto what it does.
//
// Exhaustive over the catalog rather than a subset, for the reason the Kiro table gives: a name
// that is not here falls through to the generic classifier, which reasons from substrings and is
// wrong about most of these. Three of them would be wrong in ways that matter --
//
//   - `write` and `edit` are the two most common write tools and neither contains a word the
//     generic classifier looks for.
//   - `terminal_read` and `terminal_list` contain "terminal", which the generic classifier reads
//     as shell execution. They read retained output and enumerate sessions; neither runs anything.
//     Entering them as `other` is what stops a later substring rule promoting them.
//   - `send_message` and `spawn_teammate` contain neither, and are agent-coordination tools rather
//     than filesystem or shell ones.
//
// Matched lowercased and exactly. There is no prefix or substring rule anywhere in this file.
var dshToolKinds = map[string]dshToolKind{
	// Reads. `glob` and `grep` take a file *or directory* to search rather than a file that was
	// read; recording that path under file.path with operation "read" is the shape the Kiro,
	// OpenHands and Qwen taxonomies already produce, and it is the honest one -- a search did
	// happen, and that path is what it touched.
	"read":       dshToolRead,
	"read_image": dshToolRead,
	"glob":       dshToolRead,
	"grep":       dshToolRead,

	// Writes. `write` creates or fully replaces, which is "create"; `edit` replaces literal text in
	// a file that already exists, which is "modify". `str_replace_editor` is reclassified per call
	// by dshEditorOperation, because it multiplexes four operations onto one name.
	"write":              dshToolCreate,
	"edit":               dshToolModify,
	"str_replace_editor": dshToolCreate,

	// Shell. `bash` and `pwsh` are the two dialects of the one executor seam; `terminal_send`
	// writes a command line into a persistent terminal, which is the same act through a session
	// that survives between calls.
	//
	// `run_code` is deliberately NOT here, and it is the entry most likely to be argued the other
	// way: it executes a TypeScript program, and it is the one tool that can ask for
	// `danger-full-access`. Two things decide it.
	//
	// First, a program's nested tool calls are not invisible. dsh dispatches them back through the
	// complete guarded pipeline -- `scheduler.prepare` runs `tools/pre-execute` and the commit
	// stage runs `tools/post-execute` for every sub-call -- which is the same seam the hook bridge
	// listens on. So `await tools.bash({command: 'curl ... | sh'})` inside a program fires its own
	// PreToolUse and PostToolUse hooks and is recorded as a command execution with a real command
	// line. Classifying the outer wrapper as a command as well would double-count it.
	//
	// Second, command.command is a *shell* command line and every threat rule that reads it is
	// written in shell terms. A TypeScript program in that field would match those rules on its
	// comments and its string literals while matching none of them on what it does -- and it would
	// do so instead of the precise nested events, not as well as them.
	//
	// The program text is still retained: dsh is in rawPayloadKeys, so the `code` argument arrives
	// under raw.dsh.tool_input.code, secret-redacted and size-limited like every other field. That
	// entry exists largely for this tool; see the note on it.
	"bash":          dshToolShell,
	"pwsh":          dshToolShell,
	"terminal_send": dshToolShell,

	// Terminal session management. `terminal_open` creates a session and takes a cwd, not a
	// command; the rest inspect or end one. A signal is the closest of them to execution and is
	// still not it: it delivers SIGINT to something already running.
	"terminal_open":   dshToolOther,
	"terminal_read":   dshToolOther,
	"terminal_list":   dshToolOther,
	"terminal_close":  dshToolOther,
	"terminal_signal": dshToolOther,

	// Background jobs. A job was started by the tool that started it -- `bash`, `pwsh` or
	// `terminal_send` with run_in_background -- and that call is already recorded as a command.
	// These three collect, list and kill it.
	"job_output": dshToolOther,
	"job_list":   dshToolOther,
	"job_kill":   dshToolOther,

	// Everything else the catalog publishes, entered so the generic classifier never sees it.
	"run_code":                    dshToolOther,
	"ask_user_question":           dshToolOther,
	"exit_plan_mode":              dshToolOther,
	"present":                     dshToolOther,
	"plugin_manager":              dshToolOther,
	"lsp":                         dshToolOther,
	"ralph":                       dshToolOther,
	"skill":                       dshToolOther,
	"workflow":                    dshToolOther,
	"todo_write":                  dshToolOther,
	"web_fetch":                   dshToolOther,
	"web_search":                  dshToolOther,
	"create_goal":                 dshToolOther,
	"get_goal":                    dshToolOther,
	"update_goal":                 dshToolOther,
	"schedule_create":             dshToolOther,
	"schedule_delete":             dshToolOther,
	"schedule_list":               dshToolOther,
	"subagent":                    dshToolOther,
	"list_subagent_models":        dshToolOther,
	"spawn_teammate":              dshToolOther,
	"wait_agent":                  dshToolOther,
	"interrupt_agent":             dshToolOther,
	"list_agents":                 dshToolOther,
	"send_message":                dshToolOther,
	"team_task_create":            dshToolOther,
	"team_task_get":               dshToolOther,
	"team_task_list":              dshToolOther,
	"team_task_update":            dshToolOther,
	"session_search":              dshToolOther,
	"session_trace":               dshToolOther,
	"session_event_read":          dshToolOther,
	"session_event_search":        dshToolOther,
	"session_event_trace":         dshToolOther,
	"cordis_inspect_list":         dshToolOther,
	"cordis_inspect_query":        dshToolOther,
	"stagehand_act":               dshToolOther,
	"stagehand_extract":           dshToolOther,
	"stagehand_navigate":          dshToolOther,
	"stagehand_observe":           dshToolOther,
	"stagehand_screenshot":        dshToolOther,
	"stagehand_tabs":              dshToolOther,
	"list_mcp_resources":          dshToolOther,
	"list_mcp_resource_templates": dshToolOther,
	"read_mcp_resource":           dshToolOther,
}

// dshEditorCommands are the operations `str_replace_editor` selects between.
//
// This set has a second job, and it is the one that would bite: `command` is also the argument name
// dsh's bash and pwsh tools use for the command line. Without knowing that an editor call's
// `command` names an operation rather than a shell command, the shared field extraction would write
// "str_replace" into command.command and let the policy seam upgrade the call from tool.invoked to
// command.executed. That is the OpenHands `file_editor` defect and the Kiro `fs_write` defect,
// guarded the same way.
var dshEditorCommands = map[string]bool{
	"view":        true,
	"create":      true,
	"str_replace": true,
	"insert":      true,
}

// dshMultiplexedEditorTools are the tool names whose `command` argument names an editor operation
// rather than a shell command.
//
// Only `str_replace_editor`. Deliberately not every dsh tool: `command` means a shell command line
// on `bash` and `pwsh`, and a set that reached those would drop the one field that makes a shell
// event worth recording.
var dshMultiplexedEditorTools = map[string]bool{
	"str_replace_editor": true,
}

// dshShellTools are the tools whose result text is a command's combined output.
//
// Kept beside the taxonomy rather than derived from it because the two answer different questions:
// the taxonomy says what kind of event this is, this says whose output the result string is. They
// agree today; a future tool that is command.executed without producing shell output would make
// them differ, and the difference should be stated rather than discovered.
var dshShellTools = map[string]bool{
	"bash":          true,
	"pwsh":          true,
	"terminal_send": true,
}

// dshExitCodeMarkerPrefix is what dsh's bash and pwsh tools put in front of a non-zero exit.
//
// Both tools document the same contract: "Non-zero exits are reported as `[exit code: N]`". It is
// the only place an exit code reaches a hook, because the bridge flattens the tool result to text
// before the hook sees it -- there is no structured result to read a number out of.
//
// A zero exit produces no marker, which is why its absence is not read as success or failure by
// anything in this file. The marker is a source for command.exit_code when it is there, and
// nothing when it is not.
const dshExitCodeMarkerPrefix = "[exit code:"

// dshToolKindFor classifies a dsh tool by name, and reports whether the name is known at all.
//
// The second result matters: an unknown name is a tool this build has not seen -- an MCP tool, a
// tool added after this table was written, a tool a local plugin registered -- and the caller falls
// through to the shared classifier rather than being told the tool does nothing.
func dshToolKindFor(toolName string) (dshToolKind, bool) {
	kind, ok := dshToolKinds[strings.ToLower(strings.TrimSpace(toolName))]
	return kind, ok
}

// dshIsCatalogToolName reports whether a name is one of dsh's own built-in tools.
//
// Its one caller is the MCP detection, which treats any tool name containing "mcp" as an MCP
// invocation. That is true of every runtime that decorates MCP names -- dsh among them, which
// spells one `mcp__<server>__<tool>` -- and false of three of dsh's own built-ins:
// `list_mcp_resources`, `list_mcp_resource_templates` and `read_mcp_resource` manage MCP servers
// rather than call one. Without this, listing a server's resources is recorded as a tool call to a
// server that was never invoked, with an empty mcp.tool and a method of "tools/call" that is not
// the request that was made.
//
// The catalog is the right question to ask because it is exhaustive and exact: a real MCP call is
// never in it, so it is never suppressed here.
func dshIsCatalogToolName(toolName string) bool {
	_, known := dshToolKindFor(toolName)
	return known
}

// dshEditorOperation reads the operation a `str_replace_editor` call performed, or "" when the call
// is not one.
//
// Guarded on the tool name and on the value being a known editor operation, in that order. Both
// guards are load-bearing: the name keeps a shell command line out of here, and the value set keeps
// a shell command that happened to be run through a mis-named tool from being read as an operation.
//
// Only the arguments are consulted, unlike the Kiro reader which also looks at the result. dsh's
// result is a flattened string rather than an object, so there is no second place for the operation
// to be stated -- and reading a string for it would mean substring-matching tool output, which is
// how a file whose contents contain the word "create" gets classified.
func dshEditorOperation(toolName string, toolInput map[string]interface{}) string {
	if !dshMultiplexedEditorTools[strings.ToLower(strings.TrimSpace(toolName))] {
		return ""
	}
	value := strings.ToLower(strings.TrimSpace(firstToolString(toolInput, "command")))
	if !dshEditorCommands[value] {
		return ""
	}
	return value
}

// dshEditorCommandIsOperation reports whether a call's `command` argument names an editor operation
// rather than a shell command, so the shared field extraction can leave it alone.
func dshEditorCommandIsOperation(toolName string, toolInput map[string]interface{}) bool {
	return dshEditorOperation(toolName, toolInput) != ""
}

// dshEditorKindForOperation maps an editor operation onto the write kind it is.
//
// `view` is a read: it is the same tool and the same arguments as the three writes, and the only
// thing that separates them is this word. Without it a `view` call would be recorded as a file
// creation, asserting that a file changed when the agent only looked at it.
func dshEditorKindForOperation(operation string) dshToolKind {
	switch operation {
	case "view":
		return dshToolRead
	case "create":
		return dshToolCreate
	default:
		// "str_replace" and "insert". Both change a file that already exists.
		return dshToolModify
	}
}

// dshEffectiveToolKind is the kind a call has after its own arguments are taken into account.
//
// One place rather than four, because the multiplexed-editor reclassification has to happen
// identically in the action, the file operation, the edit predicate and the diff path, and four
// copies of "ask the table, then ask the arguments" is how those drift apart.
func dshEffectiveToolKind(toolName string, toolInput map[string]interface{}) (dshToolKind, bool) {
	kind, known := dshToolKindFor(toolName)
	if !known {
		return dshToolOther, false
	}
	if operation := dshEditorOperation(toolName, toolInput); operation != "" {
		kind = dshEditorKindForOperation(operation)
	}
	return kind, true
}

// dshTerminalCommand reads the command line a `terminal_send` call submitted, or "" for any other
// tool.
//
// dsh's persistent terminal names its payload `text`, not `command`, so every key in the shared
// reader's list misses it and a command run through a terminal session would be recorded as a
// command event with no command in it -- invisible to every rule that matches on command.command,
// which is most of them.
//
// A call with `submit: false` is still read. It carries control characters or an incomplete REPL
// line rather than a whole command, so what is recorded is a fragment of shell input; that is worth
// stating and is worse than nothing only if a reader assumes the field is always complete.
// `submit` itself rides along under gen_ai.tool.call.arguments, so the distinction is recoverable.
// The alternative -- dropping those calls -- would make the escape hatch of sending a command in
// two writes a way to run commands Beacon does not record.
//
// Guarded on the tool name rather than on the key: `text` is an ordinary argument name that other
// tools use for other things, which is exactly why it is not in the shared key list.
func dshTerminalCommand(toolName string, toolInput map[string]interface{}) string {
	if strings.ToLower(strings.TrimSpace(toolName)) != "terminal_send" {
		return ""
	}
	return firstToolString(toolInput, "text")
}

// dshToolAction maps a dsh tool call onto an endpoint event action, or returns "" to let the
// generic classifier decide.
//
// MCP is not asked first here, unlike the Kiro reader, because it does not have to be: dsh's MCP
// names carry the `mcp__` prefix the shared detection already reads, and no built-in in the table
// above can collide with it. A server tool named `read` arrives as `mcp__files__read`, which is not
// the string `read`, so the table simply does not match it and the shared MCP path takes it.
func dshToolAction(toolName string, toolInput map[string]interface{}) string {
	kind, known := dshEffectiveToolKind(toolName, toolInput)
	if !known {
		return ""
	}
	switch kind {
	case dshToolRead:
		return "file.read"
	case dshToolCreate, dshToolModify:
		return "file.modified"
	case dshToolShell:
		return "command.executed"
	default:
		// A known tool with no filesystem or shell meaning, answered here rather than handed back
		// to the shared classifier -- and this is the whole reason the `other` entries are in the
		// table at all. That classifier reasons from substrings, and dsh's names are full of the
		// ones it looks for: `terminal_read`, `terminal_list`, `terminal_open`, `terminal_close`
		// and `terminal_signal` contain "terminal" and would be recorded as command executions;
		// `read_mcp_resource`, `list_mcp_resources` and `list_mcp_resource_templates` contain
		// "mcp" and would be recorded as MCP tool invocations; `session_event_read` contains
		// "read" and would be recorded as a file read. Returning "" would put every one of them
		// back in front of the rule this table exists to keep them away from -- session
		// inspection and resource listing firing command and file rules. Reported by Cursor
		// Bugbot.
		//
		// A real MCP call cannot be swallowed by this branch: dsh names one `mcp__<server>__<tool>`
		// and no entry in the table is that shape, so an MCP call is never `known` here and reaches
		// the shared MCP detection unchanged.
		return "tool.invoked"
	}
}

// dshFileOperation is the `file.operation` value for a dsh tool. The second result reports whether
// the tool was in the table at all, so the caller can tell a known tool that did nothing to a file
// ("", true) from a tool this build has never seen ("", false) and only fall through to the generic
// reader for the second.
//
// Separate from dshToolAction, as the Kiro, Qwen and OpenHands pairs are, because the two answer
// different questions: an action says which event this is, an operation says what happened to the
// file.
//
// The distinction is the fileOperation half of the defect dshToolAction's default case describes:
// the generic reader matches "read", "view", "list", "grep" and "search" as a read and "write" as a
// create, so `terminal_read`, `terminal_list`, `session_search` and `todo_write` would each stamp a
// file operation onto a call that opened no file, whenever the call also carried something the path
// reader accepts.
func dshFileOperation(toolName string, toolInput map[string]interface{}) (string, bool) {
	kind, known := dshEffectiveToolKind(toolName, toolInput)
	if !known {
		return "", false
	}
	switch kind {
	case dshToolRead:
		return "read", true
	case dshToolCreate:
		return "create", true
	case dshToolModify:
		return "modify", true
	default:
		return "", true
	}
}

// dshFileOperationValue is dshFileOperation for the callers that only want the operation, for whom
// a known tool with no file operation and an unknown tool are the same answer: nothing to write.
func dshFileOperationValue(toolName string, toolInput map[string]interface{}) string {
	operation, _ := dshFileOperation(toolName, toolInput)
	return operation
}

// isDshFileEditTool reports whether a dsh tool changes a file, which is what routes a post-tool
// payload down the diff path.
func isDshFileEditTool(toolName string, toolInput map[string]interface{}) bool {
	kind, known := dshEffectiveToolKind(toolName, toolInput)
	if !known {
		return false
	}
	return kind == dshToolCreate || kind == dshToolModify
}

// dshResultText is the tool output the bridge flattened into `tool_response`.
//
// `tool_response` arrives as a plain string -- the bridge calls blocksToText on the result's
// content blocks before building the payload -- and the shared resolveToolResponse wraps a string
// response as {"result": <string>} so the rest of the pipeline has a map to work with. This reads
// that one key back out.
//
// The type assertion is deliberate rather than a call to the shared string reader, for the reason
// the Kiro version gives: that reader normalizes any value through fmt.Sprint, so a non-string
// would come back as Go's rendering of whatever it is, written into a field a person reads as
// command output.
func dshResultText(toolResponse map[string]interface{}) string {
	if toolResponse == nil {
		return ""
	}
	if text, ok := toolResponse["result"].(string); ok {
		return text
	}
	return ""
}

// dshExitCode reads the exit status out of a shell tool's flattened output, and reports whether
// there was one to read.
//
// dsh's bash and pwsh tools both document that a non-zero exit is reported as `[exit code: N]` in
// the returned text. That marker is the only place an exit code reaches a hook on this runtime,
// because the bridge flattens the result before the hook sees it.
//
// The LAST marker rather than the first. The output above it is the command's own, and a command
// that prints something looking like the marker -- a test asserting on one, a log line quoting one
// -- would otherwise decide the event's exit code. dsh appends its marker at the end, so the last
// one is the runtime's.
//
// A missing marker returns false rather than 0. Zero is what dsh means by omitting it, but saying
// so here would make this function's two failure modes -- "the command succeeded" and "this output
// is not from a tool that reports exit codes" -- indistinguishable to the caller, which only calls
// it for tools that do.
func dshExitCode(output string) (int, bool) {
	index := strings.LastIndex(output, dshExitCodeMarkerPrefix)
	if index < 0 {
		return 0, false
	}
	rest := output[index+len(dshExitCodeMarkerPrefix):]
	end := strings.Index(rest, "]")
	if end < 0 {
		return 0, false
	}
	code, err := strconv.Atoi(strings.TrimSpace(rest[:end]))
	if err != nil {
		return 0, false
	}
	return code, true
}

// applyDshToolResult attaches what the shared toolFieldsWithResponse cannot reach: the operation a
// multiplexed editor call performed, and a shell command's output and exit status.
//
// The operation has to be applied here rather than left to fileOperation because it lives in the
// call's arguments, and the shared reader has already written its answer into the file field by the
// time this runs.
func applyDshToolResult(fields map[string]interface{}, toolName string, toolInput, toolResponse map[string]interface{}) {
	if operation := dshFileOperationValue(toolName, toolInput); operation != "" {
		if file, ok := fields["file"].(map[string]interface{}); ok {
			file["operation"] = operation
		}
	}
	if !dshShellTools[strings.ToLower(strings.TrimSpace(toolName))] {
		return
	}
	output := dshResultText(toolResponse)
	if output == "" {
		return
	}
	command := mutableChild(fields["command"])
	command["output"] = output
	if code, ok := dshExitCode(output); ok {
		command["exit_code"] = code
	}
	fields["command"] = command
	// The retention marker describes the command output specifically, so it is not overwritten: a
	// caller that already retained something -- a diff -- has the more specific claim.
	if _, exists := fields["content"]; !exists {
		fields["content"] = retainedContentFields(output)
	}
}

// parseDshEdit turns one post-tool payload into the file edit it performed, or nil when it is not
// one.
//
// nil is not a failure. It means this payload is not a recordable edit -- a read, a shell command,
// an editor `view`, a write whose arguments this build could not read -- and the caller falls
// through to the observing path, which still records the tool call with its path and operation.
//
// There is no failed-write guard here, unlike the Kiro and Qwen readers, and its absence is the
// one thing about this function worth knowing. Those runtimes report a failed write with the same
// tool name and arguments as a successful one and a flag on the result that separates them; the
// dsh bridge sends no such flag, so a write that threw is indistinguishable at the hook from one
// that landed. The diff is built from the arguments either way. What that costs is a diff recorded
// for a write that failed -- stated here rather than papered over by substring-matching the result
// text for the word "error", which would drop real diffs whenever a file's contents contain it.
func parseDshEdit(input map[string]interface{}, logger *logging.Logger) *evaluationParams {
	toolName := getFirstStr(input, "tool_name")
	toolInput := resolveToolInput(input)
	toolResponse := resolveToolResponse(input)

	if !isDshFileEditTool(toolName, toolInput) {
		return nil
	}
	filePath := hookdiff.NormalizePath(firstToolString(toolInput, "file_path", "path"))
	if filePath == "" {
		return nil
	}
	if !config.IsScannableFile(filePath) {
		logger.Debug("Skipping non-scannable file: " + filePath)
		return nil
	}
	diffStr := dshDiff(toolName, toolInput, toolResponse)
	if diffStr == "" {
		logger.Debug("Could not construct diff, skipping", "tool_name", toolName, "file_path", filePath)
		return nil
	}
	return &evaluationParams{
		sessionID: resolveSessionID(input, dshPlatform),
		toolName:  toolName,
		filePath:  filePath,
		diffStr:   diffStr,
		// The taxonomy already answered this, and the shared diff path would otherwise write
		// "modify" for a file that did not exist a moment ago. Only a write reaches here, so the
		// operation is always one of "create" and "modify" and the known flag has nothing to add.
		fileOperation: dshFileOperationValue(toolName, toolInput),
	}
}

// dshDiff builds the unified diff for a dsh write, choosing the builder by the shape of the call's
// arguments rather than by the tool's name alone.
//
// `write` and `edit` go through the shared FromToolResponse, which already knows both by those
// exact lowercase names and reads `file_path` with `content` and `old_string`/`new_string` -- dsh's
// fs tools use the ecosystem spellings verbatim, so there is nothing to translate.
//
// `str_replace_editor` goes through FromEditorCommandWrite, which is the Anthropic text-editor
// shape: `path` with `file_text`, `old_str` and `new_str`, selected by a `command` argument. dsh is
// the second runtime to ship that tool and the builder is the same one Kiro's write path uses.
func dshDiff(toolName string, toolInput, toolResponse map[string]interface{}) string {
	if operation := dshEditorOperation(toolName, toolInput); operation != "" {
		return hookdiff.FromEditorCommandWrite(operation, toolName, toolInput, toolResponse)
	}
	return hookdiff.FromToolResponse(strings.ToLower(strings.TrimSpace(toolName)), toolInput, toolResponse)
}
