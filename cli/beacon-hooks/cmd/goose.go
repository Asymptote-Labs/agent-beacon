package cmd

import (
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/config"
	hookdiff "github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/diff"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/logging"
)

// goose payload readers and tool taxonomy.
//
// goose (Block) ships a native hook system modelled on the Open Plugins hooks specification: a
// plugin declares commands in `<plugin-root>/hooks/hooks.json`, goose runs each matching command
// with the event as JSON on stdin, and the events line up closely enough with the subcommands this
// binary already has that goose rides them rather than getting a mapper of its own. That is the
// OpenHands and Kiro shape, and it is chosen for the same reason: the shared path already carries
// the inventory heartbeat, log rotation, session state, the policy seam, content retention and the
// tool call id, and a second implementation of those is the thing that drifts.
//
// The envelope is goose's HookContext:
//
//	{"event": "PreToolUse", "session_id": "...", "matcher_context": "shell",
//	 "tool_call_id": "...", "tool_name": "shell", "tool_input": {...},
//	 "message": "...", "last_assistant_message": "...", "working_dir": "..."}
//
// Three fields differ from the Claude spelling the shared readers use: the event name is `event`
// and not `hook_event_name`, the working directory is `working_dir` and not `cwd`, and the prompt
// is `message` and not `prompt`. `session_id`, `tool_name`, `tool_input` and `tool_call_id` are
// spelled exactly as Claude spells them, so the shared readers find those with no branch -- the
// tool call id in particular already resolves through asymptoteobserve.ToolCallIDKeys, which
// carries `tool_call_id`.
//
// Unlike OpenHands, `event` *is* read here rather than being left to the subcommand binding. goose
// reports a failed tool call as a separate event, `PostToolUseFailure`, whose payload is otherwise
// identical to `PostToolUse` -- same tool name, same arguments, no result either way. The event
// name is therefore the only thing that distinguishes a write that landed from one that did not,
// and both are bound to `post-tool`.
//
// What goose does not send, and what that costs:
//
//   - No tool output, ever. `HookContext` has a `tool_output` field and no emission site in the
//     runtime populates it, so a shell command's exit code and output, a file read's contents and
//     an MCP call's result are all absent. Commands are recorded as executed with their command
//     line and nothing about how they went.
//   - No token usage and no cost. goose reports both, but over OTLP rather than to a hook; that is
//     what the OTLP path collects, and it is why a goose endpoint wants both paths installed.
//   - No operator approval decision. goose does ask -- `ToolApprovalOperation` runs the permission
//     judge and can stop for a person -- and exposes none of it to a hook. Approvals are therefore
//     not synthesized here, matching the Cline, Pi, OpenHands and Kiro decision. The branch in
//     runPreTool carries that reasoning, together with why answering goose's blocking events with
//     an explicit allow is nevertheless safe.
//   - No working directory on three of the events; see gooseWorkingDir.
const goosePlatform = "goose"

// gooseEventPostToolUseFailure is the only goose event name this binary has to recognize.
//
// Every other event it subscribes to is bound to its own subcommand, so the command that is
// running already knows which event it is answering -- the OpenHands reasoning. `PostToolUse` and
// `PostToolUseFailure` are the exception: they share the `post-tool` subcommand because their
// payloads are identical, and the name is the only thing that tells them apart.
const gooseEventPostToolUseFailure = "PostToolUseFailure"

// gooseToolNameSeparator is what goose puts between an extension's name and its tool's.
//
// goose's own categorize_tool splits on this and keeps the last segment, so matching it here is
// matching the runtime rather than guessing at a convention.
const gooseToolNameSeparator = "__"

// goosePlatformExtensions are the extensions goose builds in, by the name that prefixes their
// tools.
//
// This set exists for one job: telling a built-in tool from an MCP one. goose gives every
// third-party extension an MCP server and calls its tools `<extension>__<tool>`, with no "mcp"
// anywhere in the name, no mcp_* argument, and -- since there is no tool output at all on this
// runtime -- no result to inspect either. So none of the shared MCP signals fire, and a GitHub MCP
// server's `github__create_issue` would be classified by the generic substring rules, which see
// "create" and record it as a file modification.
//
// The prefix is the only thing that says otherwise, and it says it by exclusion: a prefix that is
// not one of these names belongs to an extension goose loaded over MCP. Enumerating the built-ins
// rather than trying to enumerate MCP servers is the only direction that works, since the server
// list is whatever the operator configured.
//
// "extension manager" appears alongside "extensionmanager" because goose registers that one under
// a map key that differs from its display name, and both spellings are reachable.
var goosePlatformExtensions = map[string]bool{
	"analyze":           true,
	"apps":              true,
	"chatrecall":        true,
	"code_execution":    true,
	"developer":         true,
	"extension manager": true,
	"extensionmanager":  true,
	"orchestrator":      true,
	"scheduler":         true,
	"skills":            true,
	"summarize":         true,
	"summon":            true,
	"todo":              true,
	"tom":               true,
}

// gooseCommandTools, gooseCreateTools, gooseEditTools and gooseReadTools are the built-in tools
// whose effect Beacon records as something more specific than "a tool ran".
//
// They are keyed on the tool's own name with any extension prefix already removed, because goose
// advertises its `developer` tools *unprefixed*: the extension is registered with
// `unprefixed_tools: true`, so the model calls `shell`, not `developer__shell`. Both spellings
// reach a hook in practice -- goose's own resolver recovers `developer__shell` and `developer.shell`
// from a model that adds the prefix anyway -- which is why gooseSplitToolName runs first and this
// table sees one name either way.
//
// The generic classifier would reach the same answer for `shell`, `write` and `edit` by substring,
// and they are stated anyway for the reason the OpenHands and Kiro tables give: this table is what
// the goose taxonomy is read from, and a reader asking whether shell execution is captured should
// find the answer here rather than having to work out which substring rule happens to fire. It
// would reach the *wrong* answer for `tree`, which is a directory listing whose name contains
// none of the read words.
var (
	gooseCommandTools = map[string]bool{"shell": true}
	gooseCreateTools  = map[string]bool{"write": true}
	gooseEditTools    = map[string]bool{"edit": true}
	// `tree` lists a directory rather than reading one file, and its `path` is that directory.
	// Recording it under file.path with operation "read" is the shape OpenHands' glob and grep
	// already produce, and it is the honest one: a traversal did happen and the directory is what
	// it touched.
	//
	// `read_image` is here but is not unconditional; see gooseReadImageSource.
	gooseReadTools = map[string]bool{"tree": true, "read_image": true}
)

// gooseReadImageToolName is the built-in that reads an image from a path *or* a URL.
const gooseReadImageToolName = "read_image"

// gooseBlockingEventResponse is the reply goose's two blocking events require.
//
// goose runs `PreToolUse` and `Stop` through emit_blocking, which classifies what the hook wrote to
// stdout instead of ignoring it. A hook that exits 0 with stdout carrying no recognized decision is
// classified a *failure*: goose logs one per tool call and per turn, and on a PreToolUse rule
// configured `on_failure: block` it denies the call outright. The no-op `{}` every other runtime
// reads as "no opinion" lands there, and so does `{"permission":"allow"}` -- goose reads `decision`
// and accepts only "allow" and "block".
//
// Saying "allow" approves nothing. goose's hook chain is a plugin-policy layer inside
// ToolExecutionOperation, and the pipeline registers ToolApprovalOperation *before* it, so the
// operator's decision has already been made by the time a hook is consulted; HookDecision::Allow
// means "this policy hook does not object" and reaches no permission judge. That is what separates
// goose from Qwen Code, where "allow" would genuinely disarm the user's own prompts and Beacon
// therefore answers with an empty object.
//
// Shared by both callers rather than written twice, because the two events are one contract: a
// change to what goose accepts has to reach pre-tool and stop together, and the literal appearing
// in two files is how one of them gets left behind.
var gooseBlockingEventResponse = map[string]interface{}{"decision": "allow"}

// gooseHookEvent reads which goose lifecycle event a payload describes.
func gooseHookEvent(input map[string]interface{}) string {
	return getFirstStr(input, "event")
}

// gooseToolFailed reports whether a post-tool payload describes a call that failed.
//
// The event name, and nothing else, because on this runtime there is nothing else. goose splits
// the post-tool event in two -- `PostToolUse` for a result that is not flagged `is_error`,
// `PostToolUseFailure` for one that is or for a transport error -- and then sends an identical
// payload either way. No error string, no exit code, no result.
//
// This is what the diff path guards on. goose's `edit` requires its `before` text to match the
// file exactly and uniquely and fails the call otherwise, so a failed edit arrives carrying the
// same `before` and `after` as a successful one. Building a diff from it would assert that a file
// changed when the edit never landed -- the same false positive the Qwen and OpenHands guards
// exist to prevent, reached by a different route.
func gooseToolFailed(input map[string]interface{}) bool {
	return gooseHookEvent(input) == gooseEventPostToolUseFailure
}

// gooseWorkingDir resolves the directory the agent is working in.
//
// The payload field is `working_dir`, which the shared resolveCwd does not read.
//
// There is deliberately no fallback. goose omits the field on three of the events Beacon
// subscribes to -- `SessionStart` and `SessionEnd` when they come from the agent rather than the
// CLI session loop, and a `UserPromptSubmit` raised by steering mid-turn -- and the obvious
// fallback, the hook process's own working directory, is wrong in exactly the case that matters:
// goose runs hooks with `sh -c` inheriting its own cwd, which is the project directory under the
// CLI and the application bundle under the desktop app. A desktop session would therefore record
// every event against a path that looks like a repository, resolves to nothing, and reaches
// customer logs -- which is worse than recording no path, the judgement the Cline workspace reader
// already makes about file:// URIs.
//
// What the omission costs is bounded and known: those three events carry no session.working_directory
// and no repository. Every tool event does carry `working_dir`, and those are the events a
// repository attribution is actually read from.
func gooseWorkingDir(input map[string]interface{}) string {
	return getFirstStr(input, "working_dir")
}

// goosePrompt reads the submitted prompt text.
//
// `message` is deliberately not added to the shared key list in runPromptSubmit, for the reason
// the OpenHands reader states: that list is consulted for every hook runtime, and `message` is an
// ordinary key that carries something other than the user's prompt in other runtimes' payloads --
// a status line, a tool result, an error -- so widening it would put one of those into prompt.text
// for a runtime that has nothing to do with goose.
func goosePrompt(input map[string]interface{}) string {
	return getFirstStr(input, "message")
}

// gooseSplitToolName separates a goose tool name into the extension that owns it and the tool's
// own name.
//
// Both separators goose itself accepts are handled, and they are handled differently on purpose:
//
//   - `__` is goose's real separator, the one it advertises MCP tools under and the one its own
//     categorize_tool splits on. Any prefix is taken, because an unknown prefix is precisely the
//     signal that an extension came from MCP.
//   - `.` is the mangled spelling goose's resolver recovers when a model writes `developer.shell`
//     for a tool advertised as `shell`. Here the prefix is taken *only* when it names a built-in
//     extension, because a dot is not a goose convention and an MCP server is free to put one in
//     a tool name of its own. Splitting `foo.bar` would invent an extension called "foo" and
//     report an ordinary tool as an MCP call.
//
// An unprefixed name returns an empty extension and itself, which is the common case: the
// `developer` extension is registered with unprefixed_tools, so its tools arrive bare.
func gooseSplitToolName(toolName string) (extension, local string) {
	trimmed := strings.TrimSpace(toolName)
	if index := strings.LastIndex(trimmed, gooseToolNameSeparator); index >= 0 {
		return strings.ToLower(trimmed[:index]), strings.ToLower(trimmed[index+len(gooseToolNameSeparator):])
	}
	if index := strings.LastIndex(trimmed, "."); index >= 0 {
		if candidate := strings.ToLower(trimmed[:index]); goosePlatformExtensions[candidate] {
			return candidate, strings.ToLower(trimmed[index+1:])
		}
	}
	return "", strings.ToLower(trimmed)
}

// gooseBuiltinToolName returns the built-in tool a name refers to, or "" when the name belongs to
// an extension goose loaded over MCP.
//
// The guard is what keeps the taxonomy from claiming a third party's tool: an MCP server may name
// its tool `edit` or `shell`, and without the prefix check `github__edit` would be recorded as a
// file modification on the strength of a name its own server chose.
func gooseBuiltinToolName(toolName string) string {
	extension, local := gooseSplitToolName(toolName)
	if extension != "" && !goosePlatformExtensions[extension] {
		return ""
	}
	return local
}

// gooseIsMCPToolName reports whether a tool name names a tool goose loaded from an MCP server.
func gooseIsMCPToolName(toolName string) bool {
	extension, local := gooseSplitToolName(toolName)
	return extension != "" && local != "" && !goosePlatformExtensions[extension]
}

// gooseMCPServerTool splits an MCP tool name into the server and the tool.
//
// Only ever called for a name gooseIsMCPToolName accepted, so the prefix is known to be an
// extension goose loaded over MCP rather than a built-in.
func gooseMCPServerTool(toolName string) (server, tool string) {
	return gooseSplitToolName(toolName)
}

// gooseReadImageSource returns the local path a `read_image` call read, or "" when it read a URL.
//
// goose's read_image takes a `source` that is documented as "local file path or http(s) URL", so
// the argument is a file path only some of the time. Recording a URL under file.path would put a
// value that is not a filesystem path into the field every rule, every git helper and every SIEM
// query treats as one -- the judgement the Cline workspace reader already makes, applied to the
// one built-in whose argument is ambiguous.
//
// The argument is `source` rather than `path`, so the shared path list in toolFieldsWithResponse
// misses it entirely and without this the call records no file at all.
func gooseReadImageSource(toolInput map[string]interface{}) string {
	source := firstToolString(toolInput, "source")
	lower := strings.ToLower(source)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return ""
	}
	return source
}

// gooseToolPath reads a file path the shared path list cannot reach, or "" when there is none.
func gooseToolPath(toolName string, toolInput map[string]interface{}) string {
	if gooseBuiltinToolName(toolName) != gooseReadImageToolName {
		return ""
	}
	return gooseReadImageSource(toolInput)
}

// gooseToolAction maps a goose tool call onto an endpoint event action, or returns "" to let the
// generic classifier decide.
//
// MCP is asked first because an MCP tool's name is chosen by whoever wrote the server and can
// collide with any built-in name below. Returning "" rather than a default is what keeps the tools
// with no filesystem or shell meaning -- todo, summarize, load_skill, analyze, the orchestrator
// set -- on the shared path, where tool.invoked is already the right answer for them.
func gooseToolAction(toolName string, toolInput map[string]interface{}) string {
	if gooseIsMCPToolName(toolName) {
		return "mcp.tool_invoked"
	}
	local := gooseBuiltinToolName(toolName)
	switch {
	case gooseCommandTools[local]:
		return "command.executed"
	case gooseCreateTools[local], gooseEditTools[local]:
		return "file.modified"
	case local == gooseReadImageToolName:
		// A read_image that fetched a URL read no file. tool.invoked says a tool ran and nothing
		// about the filesystem, which is exactly what is known; the generic classifier would see
		// "read" in the name and record a file read with no file.
		if gooseReadImageSource(toolInput) == "" {
			return "tool.invoked"
		}
		return "file.read"
	case gooseReadTools[local]:
		return "file.read"
	default:
		return ""
	}
}

// gooseFileOperation is the `file.operation` value for a goose tool, or "" to fall through to the
// generic reader.
//
// Separate from gooseToolAction, as the OpenHands and Kiro pairs are, because the two answer
// different questions: the action says what kind of event this is and the operation says what
// happened to the file, and `write` and `edit` share one action while differing here.
//
// `write` is reported as a creation even though goose's own description is "create a new file or
// overwrite an existing file". Nothing in the payload distinguishes the two -- there is no result
// and no prior content -- and "create" is the same answer Beacon already gives Claude Code's Write
// and OpenHands' write_file, so a reader comparing runtimes sees one convention rather than three.
func gooseFileOperation(toolName string, toolInput map[string]interface{}) string {
	if gooseIsMCPToolName(toolName) {
		return ""
	}
	local := gooseBuiltinToolName(toolName)
	switch {
	case gooseCreateTools[local]:
		return "create"
	case gooseEditTools[local]:
		return "modify"
	case local == gooseReadImageToolName:
		if gooseReadImageSource(toolInput) == "" {
			return ""
		}
		return "read"
	case gooseReadTools[local]:
		return "read"
	default:
		return ""
	}
}

// parseGooseEdit turns one post-tool payload into the file edit it performed, or nil when it
// performed none.
//
// nil is not a failure. It means this payload is a read, a shell command, an MCP call, a failed
// write, or a built-in whose arguments this build could not read -- and the caller falls through
// to the observing path, which still records the tool call with its path and operation.
func parseGooseEdit(input map[string]interface{}, logger *logging.Logger) *evaluationParams {
	// A failure is not an edit. goose sends the same tool name and the same arguments either way,
	// so the event name is the only guard there is; without it a `before`/`after` pair from an edit
	// that never matched would be recorded as a completed file.modified.
	if gooseToolFailed(input) {
		return nil
	}

	toolName := getFirstStr(input, "tool_name")
	toolInput := resolveToolInput(input)
	local := gooseBuiltinToolName(toolName)
	if !gooseCreateTools[local] && !gooseEditTools[local] {
		return nil
	}

	path := hookdiff.NormalizePath(firstToolString(toolInput, "path"))
	if path == "" {
		return nil
	}
	if !config.IsScannableFile(path) {
		logger.Debug("Skipping non-scannable file: " + path)
		return nil
	}

	var diffStr string
	if gooseEditTools[local] {
		// `before` and `after` are fragments of the file rather than whole copies of it -- goose's
		// edit replaces one exactly-matching, uniquely-occurring span -- so this is the same shape
		// as Claude Code's old_string/new_string and takes the fragment builder, not the
		// whole-content one. FromContentChange would render the two fragments as if they were the
		// entire file before and after.
		diffStr = hookdiff.FromEditFragments(path, firstToolString(toolInput, "before"), firstToolString(toolInput, "after"))
	} else {
		// `write` carries the whole new file under `content`, which is exactly the shape the shared
		// reader's "write" case already handles, down to resolving the path from `path`. Reused
		// rather than restated so goose's creation diffs and every other runtime's stay one
		// implementation.
		diffStr = hookdiff.FromToolResponse("write", toolInput, nil)
	}
	if diffStr == "" {
		logger.Debug("Could not construct diff, skipping", "tool_name", toolName, "file_path", path)
		return nil
	}

	return &evaluationParams{
		sessionID:     resolveSessionID(input, goosePlatform),
		toolName:      toolName,
		filePath:      path,
		diffStr:       diffStr,
		fileOperation: gooseFileOperation(toolName, toolInput),
	}
}
