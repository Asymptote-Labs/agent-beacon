package cmd

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/config"
	hookdiff "github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/diff"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/logging"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/policycontract"
)

// Kimi Code payload readers, tool taxonomy and approval mapping.
//
// Kimi Code (Moonshot AI) runs one agent core behind a terminal CLI, a desktop app, a VS Code
// extension and an ACP server. Its hook runner builds one envelope for every event -- the event
// name, the session id, the working directory and the client identity, then the event's own fields
// -- and snake_cases every key on the way out, so what a hook reads is:
//
//	{"hook_event_name": "PreToolUse", "session_id": "...", "cwd": "...",
//	 "client_type": "kimi_code_cli", "session_title": "...",
//	 "tool_name": "Bash", "tool_input": {...}, "tool_call_id": "..."}
//
// That envelope is close enough to Claude Code's that Kimi Code rides the shared subcommands, the
// way Kiro, OpenHands and DeepSeek Harness do and for the same reason: the shared path already
// carries log rotation, session state, the policy seam, content retention and the tool call id,
// and a second implementation of those is the thing that drifts. `session_id` and `cwd` resolve
// through the shared default cases, `tool_call_id` is already in the shared alias list, and MCP
// tools arrive as `mcp__<server>__<tool>`, which the shared detection and server/tool split
// already read.
//
// What is here is everything the envelope does not settle:
//
//   - The tool taxonomy. Kimi Code publishes its built-in tools, so this table is exhaustive over
//     what the reference lists rather than a best guess -- and it has to exist, because the
//     generic substring classifier is wrong about `Glob` and `Grep` (searches, recorded as bare
//     tool calls with no file operation) and would guess about the scheduling and background-task
//     families on names like `CronCreate`, `CronList` and `TaskList`.
//
//   - `Write`'s `mode` argument, which multiplexes a create and an append onto one tool name. That
//     is the OpenHands `file_editor`, Kiro `fs_write` and DeepSeek `str_replace_editor` hazard in
//     its mildest form, and it is guarded the same way -- an append is a modification, and its
//     diff describes the fragment that was added rather than claiming the file's whole contents.
//
//   - The result. Kimi Code puts a tool's output under `tool_output` on success and under
//     `error.message` on failure, and sends neither under any key `resolveToolResponse` reads --
//     so without this file a shell command would be recorded with no output at all.
//
//   - Failure. `PostToolUseFailure` is a separate event carrying the same `tool_name` and
//     `tool_input` as a success, which is the Qwen defect exactly: without a guard, a `Write` that
//     was rejected would be sent down the diff path and recorded as a completed `file.modified`.
//
//   - Approvals. Kimi Code is one of the few supported runtimes that reports a real operator
//     decision, through a `PermissionRequest` / `PermissionResult` pair. See kimiApprovalEvent.
//
// Deliberately not read: `hook_event_name`, for the tool phases. Beacon binds each Kimi event to a
// distinct subcommand in the `[[hooks]]` entries it writes, so argv already says which event is
// running. The exception is the pair of events that share one subcommand -- PostToolUse with
// PostToolUseFailure, and PermissionRequest with PermissionResult -- where the payload is the only
// thing that separates them.
//
// Not collected, because Kimi Code exposes none of it on a hook: token usage and runtime-reported
// cost (no hook payload carries either, and the runtime's own telemetry is an anonymous product
// channel Beacon does not read), and the model's assistant text.

const kimiPlatform = "kimi"

// kimiBlockExitCode is how a Kimi Code hook refuses the event that triggered it.
//
// Kimi Code reads exit code 2 as an intentional block with stderr as the reason, and reads it
// before it looks at stdout at all -- so this is the one shape that cannot be confused with a hook
// that merely produced output. Every other non-zero code, a timeout and a crash are fail-open.
//
// Only three events are blockable: PreToolUse, Stop and UserPromptSubmit. The seam runs in the
// pre-tool and permission-request phases, and only the first of those is blockable here; see
// kimiPolicyDenial.
const kimiBlockExitCode = 2

// kimiToolKind is what one Kimi Code tool does, as far as the endpoint schema is concerned.
type kimiToolKind int

const (
	// kimiToolOther is a tool with no filesystem or shell meaning. It is not absent from the
	// table: an entry saying "this is a tool call and nothing more" is what keeps the generic
	// substring classifier from guessing something else about it, and it records that the tool was
	// considered.
	kimiToolOther kimiToolKind = iota
	kimiToolRead
	kimiToolCreate
	kimiToolModify
	kimiToolShell
)

// kimiToolKinds maps every tool Kimi Code registers onto what it does.
//
// Exhaustive rather than a subset, for the reason the Kiro and DeepSeek tables give: a name that
// is not here falls through to the generic classifier, which reasons from substrings. Four groups
// would be wrong in ways that matter --
//
//   - `Glob` and `Grep` are searches. Neither contains a word the action classifier looks for, so
//     both would be recorded as bare `tool.invoked` even though each carries the path it searched;
//     `Grep` would separately pick up `file.operation = "read"` from the substring rule while
//     `Glob` picked up nothing, so two halves of one feature would disagree.
//   - `CronCreate` contains "create" and `CronList`, `TaskList` and `TodoList` contain "list", so
//     the substring rule in fileOperation answers "create" and "read" for tools that touch no
//     file. That costs nothing today because none of them takes a path argument, which is exactly
//     why it is worth pinning: the day one grows one, the wrong answer is already written.
//   - `TaskStop` contains neither, but `WaitFor` and `TaskOutput` sit beside it in a family where
//     a future member could, and entering the family whole is what stops that.
//   - The `Tower*` family is Kimi Code's multi-agent coordination surface. It is registered but
//     not in the published tool reference, so it is entered here as agent coordination rather than
//     left for a substring rule to interpret names like `TowerMerge` and `TowerSend`.
//
// Matched lowercased and exactly. There is no prefix or substring rule anywhere in this file.
var kimiToolKinds = map[string]kimiToolKind{
	// Reads. `Glob` and `Grep` take a file *or directory* to search rather than a file that was
	// read; recording that path under file.path with operation "read" is the shape the Kiro,
	// OpenHands, Qwen and DeepSeek taxonomies already produce, and it is the honest one -- a
	// search did happen, and that path is what it touched.
	"read":          kimiToolRead,
	"readmediafile": kimiToolRead,
	"glob":          kimiToolRead,
	"grep":          kimiToolRead,

	// Writes. `Write` creates or fully replaces by default, which is "create"; it is reclassified
	// per call by kimiEffectiveToolKind when `mode` says "append". `Edit` replaces literal text in
	// a file that already exists, which is "modify".
	"write": kimiToolCreate,
	"edit":  kimiToolModify,

	// Shell. One executor, and Kimi Code has no second spelling of it -- no persistent terminal,
	// no separate PowerShell tool. A background `Bash` is the same tool with
	// `run_in_background: true`; see kimiExitCode for why that distinction matters to the result
	// rather than to the classification.
	"bash": kimiToolShell,

	// Web. Neither reads the filesystem nor runs anything locally.
	"websearch": kimiToolOther,
	"fetchurl":  kimiToolOther,

	// Plan mode. `ExitPlanMode` reads the plan file the runtime maintains, but takes no path and
	// names no file the agent chose, so recording it as a file read would attribute a read of an
	// internal artifact to the agent's own work.
	"enterplanmode": kimiToolOther,
	"exitplanmode":  kimiToolOther,

	// Session-local state. `TodoList` keeps its list inside the agent session; nothing is written
	// to disk.
	"todolist": kimiToolOther,

	// Collaboration and sub-agents. `Agent` and `AgentSwarm` spawn sub-agents, whose own tool
	// calls fire their own hooks -- so the work they do is already recorded as itself, and
	// classifying the wrapper as anything more would double-count it.
	"agent":           kimiToolOther,
	"agentswarm":      kimiToolOther,
	"askuserquestion": kimiToolOther,
	"notifyuser":      kimiToolOther,
	"skill":           kimiToolOther,

	// Background tasks. A task was started by the tool that started it -- `Bash` with
	// `run_in_background`, `Agent`, or `AskUserQuestion` -- and that call is already recorded.
	// These four list, collect, stop and wait on it.
	"tasklist":   kimiToolOther,
	"taskoutput": kimiToolOther,
	"taskstop":   kimiToolOther,
	"waitfor":    kimiToolOther,

	// Scheduled prompts. These re-inject a prompt into the session at a future time. The prompt
	// they fire arrives as an ordinary turn and is recorded as one, so the scheduling call itself
	// is a tool invocation and nothing more.
	"croncreate": kimiToolOther,
	"cronlist":   kimiToolOther,
	"crondelete": kimiToolOther,

	// Goal tracking.
	"creategoal":    kimiToolOther,
	"getgoal":       kimiToolOther,
	"updategoal":    kimiToolOther,
	"setgoalbudget": kimiToolOther,

	// The Tower multi-agent family, entered whole so the generic classifier never sees a name from
	// it. `TowerMerge` is the entry that most needs to be here: it coordinates agents, and a
	// substring rule reading it as a source-control merge would file agent coordination as file
	// activity.
	"towerinit":     kimiToolOther,
	"towermission":  kimiToolOther,
	"towerplan":     kimiToolOther,
	"towerspawn":    kimiToolOther,
	"towersend":     kimiToolOther,
	"towerinbox":    kimiToolOther,
	"towerstatus":   kimiToolOther,
	"towerreview":   kimiToolOther,
	"towerfinding":  kimiToolOther,
	"towermerge":    kimiToolOther,
	"towerteardown": kimiToolOther,
}

// kimiAppendMode is the `mode` value that turns a `Write` from a replacement into an append.
const kimiAppendMode = "append"

// kimiExitCodeMarker is how Kimi Code's Bash tool reports a non-zero exit.
//
// The tool composes its failure result as the command's own output followed by a fixed sentence,
// so this string is the only place an exit status reaches a hook: the hook payload carries no
// structured result, only `tool_output` on success and a flattened `error` on failure.
//
// A successful command produces no such sentence, and its absence is deliberately not read as
// "exited 0" -- see kimiExitCode.
var kimiExitCodeMarker = regexp.MustCompile(`Command failed with exit code: (\d+)`)

// kimiToolKindFor classifies a Kimi Code tool by name, and reports whether the name is known at
// all.
//
// The second result matters: an unknown name is a tool this build has not seen -- an MCP tool, a
// tool added after this table was written -- and the caller falls through to the shared classifier
// rather than being told the tool does nothing.
func kimiToolKindFor(toolName string) (kimiToolKind, bool) {
	kind, ok := kimiToolKinds[strings.ToLower(strings.TrimSpace(toolName))]
	return kind, ok
}

// kimiWriteIsAppend reports whether a `Write` call appends rather than replaces.
//
// Guarded on the tool name as well as the value, for the reason the Kiro and DeepSeek editor
// guards give: `mode` is an ordinary argument name, and a future tool using it for something else
// must not have its calls reclassified by this.
//
// Only the literal "append" counts. `mode` is a closed enum of overwrite and append, so anything
// else is either the default or a value this build does not know, and both mean "do not treat this
// as an append" -- the direction that records a full-content diff for a full-content write rather
// than the reverse.
func kimiWriteIsAppend(toolName string, toolInput map[string]interface{}) bool {
	if strings.ToLower(strings.TrimSpace(toolName)) != "write" {
		return false
	}
	return strings.ToLower(strings.TrimSpace(firstToolString(toolInput, "mode"))) == kimiAppendMode
}

// kimiEffectiveToolKind is the kind a call has after its own arguments are taken into account.
//
// One place rather than four, because the append reclassification has to happen identically in the
// action, the file operation, the edit predicate and the diff path, and four copies of "ask the
// table, then ask the arguments" is how those drift apart.
func kimiEffectiveToolKind(toolName string, toolInput map[string]interface{}) (kimiToolKind, bool) {
	kind, known := kimiToolKindFor(toolName)
	if !known {
		return kimiToolOther, false
	}
	if kimiWriteIsAppend(toolName, toolInput) {
		// An append changes a file that already exists. Without this the call is recorded as a
		// creation, which asserts the file's whole contents are the appended fragment.
		kind = kimiToolModify
	}
	return kind, true
}

// kimiToolAction maps a Kimi Code tool call onto an endpoint event action, or returns "" to let
// the generic classifier decide.
//
// MCP is not asked first here, unlike the Kiro reader, because it does not have to be: Kimi Code's
// MCP names carry the `mcp__` prefix the shared detection already reads, and no built-in in the
// table above can collide with it. A server tool named `read` arrives as `mcp__files__read`, which
// is not the string `read`, so the table simply does not match it and the shared MCP path takes
// it.
func kimiToolAction(toolName string, toolInput map[string]interface{}) string {
	kind, known := kimiEffectiveToolKind(toolName, toolInput)
	if !known {
		return ""
	}
	switch kind {
	case kimiToolRead:
		return "file.read"
	case kimiToolCreate, kimiToolModify:
		return "file.modified"
	case kimiToolShell:
		return "command.executed"
	default:
		// A known tool with no filesystem or shell meaning. Returned directly rather than as ""
		// so the generic substring classifier never sees the name: `TaskOutput` and `TowerReview`
		// contain nothing it matches on today, but `TodoList` and `CronCreate` do, and "" is the
		// signal reserved for tools this build has not seen.
		return "tool.invoked"
	}
}

// kimiFileOperation is the `file.operation` value for a Kimi Code tool, or "" to fall through to
// the generic reader.
//
// Separate from kimiToolAction, as the Kiro, Qwen, OpenHands and DeepSeek pairs are, because the
// two answer different questions: an action says which event this is, an operation says what
// happened to the file.
func kimiFileOperation(toolName string, toolInput map[string]interface{}) string {
	kind, known := kimiEffectiveToolKind(toolName, toolInput)
	if !known {
		return ""
	}
	switch kind {
	case kimiToolRead:
		return "read"
	case kimiToolCreate:
		return "create"
	case kimiToolModify:
		return "modify"
	default:
		return ""
	}
}

// isKimiFileEditTool reports whether a Kimi Code tool changes a file, which is what routes a
// post-tool payload down the diff path.
func isKimiFileEditTool(toolName string, toolInput map[string]interface{}) bool {
	kind, known := kimiEffectiveToolKind(toolName, toolInput)
	if !known {
		return false
	}
	return kind == kimiToolCreate || kind == kimiToolModify
}

// kimiToolFailed reports whether this post-tool payload describes a call that failed.
//
// Kimi Code says so by sending a different event: `PostToolUseFailure` carries the same
// `tool_name`, `tool_input` and `tool_call_id` as a success and differs only in the event name and
// in where the output lives. That is the Qwen shape, and it is why this predicate exists rather
// than the shared `error`-is-a-non-empty-string check: on Kimi Code `error` is an object, so
// getFirstStr finds nothing in it and a failed write would be indistinguishable from a landed one.
//
// Both signals are read, and the event name is not enough on its own: Beacon binds both events to
// the same subcommand, and a payload that reached this build by some other route -- a replay, a
// future runner that fires one event for both outcomes -- still says which it was by carrying an
// error object.
func kimiToolFailed(input map[string]interface{}) bool {
	if getFirstStr(input, "hook_event_name") == "PostToolUseFailure" {
		return true
	}
	return kimiErrorMessage(input) != ""
}

// kimiErrorMessage is the text Kimi Code flattened into the `error` object on a failed call.
//
// The runtime builds that object from the tool's own output rather than from an exception -- the
// output string is passed through its error serializer, which puts it under `message` -- so on a
// failure this is where a command's stderr, a rejected write's reason and a shell command's exit
// marker all are. `tool_output` is absent on those payloads, which is why reading only that key
// would lose every failed call's output entirely.
func kimiErrorMessage(input map[string]interface{}) string {
	errorField, ok := input["error"].(map[string]interface{})
	if !ok {
		return ""
	}
	return firstToolString(errorField, "message")
}

// kimiResultText is the tool's output, from whichever of the two places this payload put it.
//
// Success and failure are read in that order rather than merged: a payload carries one or the
// other, never both, and asking the success key first keeps the common case a single lookup.
func kimiResultText(input map[string]interface{}) string {
	if output := firstToolString(input, "tool_output"); output != "" {
		return output
	}
	return kimiErrorMessage(input)
}

// kimiExitCode reads the exit status out of a shell tool's result text, and reports whether there
// was one to read.
//
// Kimi Code's Bash tool composes a failed command's result as the command's own output followed by
// `Command failed with exit code: N.`, and that sentence is the only place an exit code reaches a
// hook -- the payload carries no structured result to read a number out of.
//
// The LAST match rather than the first. The output above it is the command's own, and a command
// that prints something looking like the sentence -- a test asserting on one, a log line quoting
// one -- would otherwise decide the event's exit code. The runtime appends its sentence at the
// end, so the last one is the runtime's.
//
// A missing marker returns false rather than 0, and that is not caution for its own sake: on this
// runtime a `Bash` call with no marker is either a command that exited 0 *or* a background command
// that has only just been launched and whose result is a task id. Those are different facts, and
// writing 0 would make the second one indistinguishable from the first.
func kimiExitCode(output string) (int, bool) {
	matches := kimiExitCodeMarker.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return 0, false
	}
	code, err := strconv.Atoi(matches[len(matches)-1][1])
	if err != nil {
		return 0, false
	}
	return code, true
}

// applyKimiToolResult attaches what the shared toolFieldsWithResponse cannot reach: the operation
// an append performed, and a shell command's output and exit status.
//
// It takes the whole payload rather than a tool response, unlike its Kiro and DeepSeek
// counterparts, because Kimi Code has no tool-response object: the output is a sibling of
// `tool_name` at the top level, under `tool_output` or inside `error`. Passing the map
// resolveToolResponse returns would pass nil on every Kimi Code payload.
func applyKimiToolResult(fields map[string]interface{}, toolName string, toolInput, input map[string]interface{}) {
	if operation := kimiFileOperation(toolName, toolInput); operation != "" {
		if file, ok := fields["file"].(map[string]interface{}); ok {
			file["operation"] = operation
		}
	}
	kind, known := kimiEffectiveToolKind(toolName, toolInput)
	if !known || kind != kimiToolShell {
		return
	}
	output := kimiResultText(input)
	if output == "" {
		return
	}
	command := mutableChild(fields["command"])
	command["output"] = output
	if kimiToolFailed(input) {
		if code, ok := kimiExitCode(output); ok {
			command["exit_code"] = code
		}
	}
	fields["command"] = command
	// The retention marker describes the command output specifically, so it is not overwritten: a
	// caller that already retained something -- a diff -- has the more specific claim.
	if _, exists := fields["content"]; !exists {
		fields["content"] = retainedContentFields(output)
	}
}

// parseKimiEdit turns one post-tool payload into the file edit it performed, or nil when it is not
// one.
//
// nil is not a failure. It means this payload is not a recordable edit -- a read, a shell command,
// a failed write, a write whose arguments this build could not read -- and the caller falls
// through to the observing path, which still records the tool call with its path and operation.
//
// The failure guard is the reason this function exists at all rather than letting Kimi Code ride
// parseClaudeCopilotInput: `Write` and `Edit` are names that reader already knows, and `path`,
// `content`, `old_string` and `new_string` are spellings it already resolves, so the diff would be
// built correctly -- for a write that never landed. Kimi Code rejects a write whose file changed
// on disk since the session last read it, and rejects an `Edit` whose `old_string` matches more
// than once; both arrive here with the arguments of a successful call.
func parseKimiEdit(input map[string]interface{}, logger *logging.Logger) *evaluationParams {
	toolName := getFirstStr(input, "tool_name")
	toolInput := resolveToolInput(input)

	if !isKimiFileEditTool(toolName, toolInput) {
		return nil
	}
	if kimiToolFailed(input) {
		return nil
	}
	filePath := hookdiff.NormalizePath(firstToolString(toolInput, "path", "file_path"))
	if filePath == "" {
		return nil
	}
	if !config.IsScannableFile(filePath) {
		logger.Debug("Skipping non-scannable file: " + filePath)
		return nil
	}
	diffStr := kimiDiff(toolName, toolInput)
	if diffStr == "" {
		logger.Debug("Could not construct diff, skipping", "tool_name", toolName, "file_path", filePath)
		return nil
	}
	return &evaluationParams{
		sessionID: resolveSessionID(input, kimiPlatform),
		toolName:  toolName,
		filePath:  filePath,
		diffStr:   diffStr,
		// The taxonomy already answered this, and the shared diff path would otherwise write
		// "modify" for a file that did not exist a moment ago -- and "create" for an append.
		fileOperation: kimiFileOperation(toolName, toolInput),
	}
}

// kimiDiff builds the unified diff for a Kimi Code write, choosing the builder by the shape of the
// call rather than by the tool's name alone.
//
// `Edit` and a replacing `Write` go through the shared FromToolResponse, which already knows both
// by those exact names and reads `path` with `content` and `old_string`/`new_string` -- Kimi Code
// uses the ecosystem spellings verbatim, so there is nothing to translate.
//
// An appending `Write` does not, and this is the whole reason the function exists. FromToolResponse
// would hand that call to the write builder, which emits `@@ -0,0 +1,N @@` over the `content`
// argument -- a claim that the file's entire contents are the fragment that was appended to it.
// FromEditFragments states the true thing instead: these lines were added, and nothing is said
// about what was there before.
//
// No tool response is passed to either builder, unlike every other runtime's diff path, because
// Kimi Code has none: its result is a flat string under `tool_output`, and the arguments carry
// everything a diff needs.
func kimiDiff(toolName string, toolInput map[string]interface{}) string {
	if kimiWriteIsAppend(toolName, toolInput) {
		return hookdiff.FromEditFragments(
			firstToolString(toolInput, "path", "file_path"),
			"",
			firstToolString(toolInput, "content"),
		)
	}
	return hookdiff.FromToolResponse(toolName, toolInput, nil)
}

// kimiApprovalEvent reads the operator decision out of a permission payload.
//
// Kimi Code is one of the few supported runtimes that reports a real one. It fires
// `PermissionRequest` just before it blocks on a person and `PermissionResult` once they have
// answered, and the second carries the whole request back plus the answer: `decision`, an optional
// `scope` of "session" when the operator approved every future call like this one, and an optional
// `feedback` string when they typed a reason. That is a decision somebody made, so these events
// are `observed` rather than `inferred` -- unlike the synthesized approvals Beacon writes on
// runtimes that expose only a pre-tool notification, and unlike the ones it refuses to synthesize
// at all on Cline, Pi, fx, Kiro and goose.
//
// Both events reach one subcommand, so the event name is what separates them. It is read rather
// than inferred from the presence of `decision` because "no decision on a result payload" and "a
// request payload" are different facts: the first would be a runtime contract change worth seeing
// as an anomaly, and collapsing them would hide it.
//
// The four decisions are the runtime's own closed set plus the one it dispatches out of band:
// "approved" and "rejected" are the operator's answers, "cancelled" is the request being withdrawn
// -- by an interrupt or a turn that ended -- and "error" is the runtime failing to ask at all.
// Only "approved" is an allow. Cancelled and error are recorded as denials because that is what
// happened to the call: it did not run, and a reader counting blocked tool calls should see them.
// Their reason distinguishes them from a person saying no.
func kimiApprovalEvent(input map[string]interface{}) (action, decision, message string) {
	if getFirstStr(input, "hook_event_name") != "PermissionResult" {
		return "approval.requested", "requested", "Permission request observed"
	}
	switch strings.ToLower(strings.TrimSpace(getFirstStr(input, "decision"))) {
	case "approved":
		return "approval.allowed", "approve", "Permission request approved"
	case "rejected":
		return "approval.denied", "deny", "Permission request rejected"
	case "cancelled":
		return "approval.denied", "cancelled", "Permission request cancelled"
	case "error":
		return "approval.denied", "error", "Permission request failed"
	default:
		// A result payload with a decision this build does not know. Recorded as a resolved
		// request rather than guessed into allow or deny: the call's outcome is genuinely unknown
		// here, and both guesses would be a claim about whether a tool ran.
		return "approval.requested", "unknown", "Permission result observed"
	}
}

// emitKimiApproval records one Kimi Code permission event.
//
// The tool's own arguments are attached through the shared toolFields, and that is what makes the
// event worth having: `PermissionResult` carries `tool_input` verbatim from the request, so an
// approval for a shell command records the command line and an approval for a write records the
// path. Every approval rule Beacon ships matches on `command.command` or `file.path` rather than
// on a tool name, so an approval that said only "the operator denied Bash" would be telemetry no
// rule could act on.
//
// `scope` and `feedback` ride under raw rather than in the approval block, because neither has a
// schema field and both are worth keeping: "session" scope is the difference between a decision
// about one call and a standing permission for every call like it, and feedback is the operator's
// own words.
func emitKimiApproval(logger *logging.Logger, input map[string]interface{}, sessionID string) {
	action, decision, message := kimiApprovalEvent(input)
	toolName := getFirstStr(input, "tool_name")
	toolInput := resolveToolInput(input)

	fields := sessionFields(sessionID, input)
	for key, value := range toolFields(toolName, toolInput) {
		fields[key] = value
	}

	approval := map[string]interface{}{"required": true, "decision": decision}
	// The runtime's own account of why, when it gave one. `action` is the runtime's description of
	// what was being approved ("Editing src/main.go"), which is the closest thing a request payload
	// has to a reason; an operator's typed `feedback` is more specific and wins.
	if reason := firstNonEmpty(getFirstStr(input, "feedback"), getFirstStr(input, "action")); reason != "" {
		approval["reason"] = reason
	}
	fields["approval"] = approval

	emitHookEvent(logger, action, "approval", "info", message, input, fields)
}

// kimiPolicyDenial is how the optional policy seam refuses a call on Kimi Code, or nil when the
// phase it is running in cannot be refused.
//
// Kimi Code reads a deny from two places -- exit code 2 with the reason on stderr, and a stdout
// object carrying `hookSpecificOutput.permissionDecision` -- and the exit code is checked first,
// before stdout is parsed at all. The exit code is what Beacon uses, because stdout on Kimi Code
// is not free: hookStdoutIsConsumedAsAgentContext reports this runtime, so outputJSON writes
// nothing here and a stdout deny would never reach the runtime at all.
//
// Only three events are blockable, and only one of them is a phase the seam runs in. A deny raised
// while answering `PermissionRequest` cannot take effect: that event is observation-only, its
// return value is discarded, and Beacon does not pretend otherwise -- returning nil there makes
// enforcePolicy fall back to "no deny shape, allow", which is the seam's documented fail-open
// behavior and writes no denial telemetry for a call that was not in fact denied.
//
// The cost of that is bounded and worth stating: a provider that only denies at the permission
// phase has no effect on Kimi Code. It is not a gap that can be closed here -- the runtime
// discards the answer -- and the pre-tool phase, which runs on every tool call on this runtime
// whether or not an approval follows, is where a deny belongs anyway.
func kimiPolicyDenial(reason string, phase policycontract.Phase) *policyDenial {
	if phase != policycontract.PhasePreTool {
		return nil
	}
	return &policyDenial{exitCode: kimiBlockExitCode, stderr: reason}
}
