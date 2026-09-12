package cmd

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/logging"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/state"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/policycontract"
)

var preToolCmd = &cobra.Command{
	Use:   "pre-tool",
	Short: "Observe pre-tool events for local endpoint telemetry",
	Long: `PreToolUse hook - triggered before a Write tool execution in Cursor.
Records local telemetry for the tool request and allows the runtime to continue.`,
	Run: runPreTool,
}

func init() {
	rootCmd.AddCommand(preToolCmd)
}

// allowResponse is the standard allow response for preToolUse.
var allowResponse = map[string]interface{}{"permission": "allow"}

func runPreTool(cmd *cobra.Command, args []string) {
	input, err := readStdinJSON()
	if err != nil {
		outputJSON(preToolResponse())
		return
	}

	sessionID := resolveSessionID(input, platformFlag)
	logger := newHookLogger("pre-tool", platformFlag, sessionID)

	logger.Debug("Pre-tool observed")
	if denial := enforcePolicy(logger, input, sessionID, policycontract.PhasePreTool); denial != nil {
		// emit does not return on a runtime that blocks by exit status, so nothing after this line
		// runs there -- which is correct: the telemetry for the denial has already been written by
		// enforcePolicy, and the observing event below would describe a call that is not going to
		// happen.
		denial.emit()
		return
	}
	if platformFlag == "cursor" && emitCursorPreHook(logger, input, sessionID) {
		maybeUploadCursorCloudTelemetry(logger)
	} else if platformFlag == "antigravity" {
		emitAntigravityPromptFromTranscript(logger, input, sessionID)
		emitPreToolObserved(logger, input, sessionID)
	} else if platformFlag == "claude" || platformFlag == "qwen" || isDevinLikePlatform(platformFlag) || platformFlag == "grok" || platformFlag == "hermes" || platformFlag == "vscode" || platformFlag == "muse" || platformFlag == openHandsPlatform || platformFlag == kiroPlatform || platformFlag == goosePlatform {
		// Muse Code belongs on the observing side rather than with the runtimes whose pre-tool
		// notification gets turned into a synthesized approval, and the reason is that it has a
		// real one. Its PermissionRequest event is a separate hook Beacon also subscribes to, so
		// deriving an approval.allowed from PreToolUse as well would record two approvals for one
		// tool call -- one of them inferred and describing nothing an operator did -- and put an
		// invented decision next to a reported one for the same call.
		//
		// OpenHands is on the same side for the opposite reason: it exposes no approval hook at
		// all. PreToolUse is the only pre-tool signal it sends, and it announces a tool call the
		// agent is about to make, not a question anybody was asked -- its own confirmation mode is
		// not surfaced to hooks. Synthesizing approval.allowed from it would put an operator
		// decision in the log that no operator made, which is the call Cline, Pi and fx already
		// settled the same way.
		//
		// Kiro is on that same side, and it is the sharpest case for it: Kiro genuinely does ask
		// the operator, through permissions.yaml rules and an interactive trust picker, and it
		// exposes none of that to a hook. PreToolUse fires whether the call was pre-approved by a
		// rule, waved through by autopilot, or about to stop and wait for a person -- so an
		// approval.allowed derived from it would claim a decision was made in the very cases where
		// one has not been made yet, on a runtime where real decisions exist and are invisible.
		// That is worse than the no-approval-gate runtimes, not better.
		//
		// goose is on that same side and for the Kiro reason: ToolApprovalOperation runs the
		// permission judge, marks calls that need a person as not executable and stops the turn
		// for an answer, and none of that reaches a hook -- goose's HookEvent set has no approval
		// event at all. PreToolUse fires identically whether the call was pre-approved by
		// goose_mode, waved through, or already confirmed by somebody, so an approval.allowed
		// derived from it would claim a decision in exactly the cases where none was made.
		//
		// The reply goose gets is a separate question from this one, and the two answers point
		// opposite ways; see preToolResponse.
		emitPreToolObserved(logger, input, sessionID)
	} else {
		emitPreToolDecision(logger, input, sessionID, "approval.allowed", "allow", "Pre-tool observed", asymptoteobserve.FidelityInferred)
	}
	outputJSON(preToolResponse())
}

func emitCursorPreHook(logger *logging.Logger, input map[string]interface{}, sessionID string) bool {
	switch getFirstStr(input, "hook_event_name", "hookEventName") {
	case "beforeShellExecution":
		fields := sessionFields(sessionID, input)
		command := getFirstStr(input, "command")
		fields["command"] = map[string]interface{}{"command": command}
		fields["approval"] = map[string]interface{}{
			"required": true,
			"decision": "allow",
			"reason":   "Shell execution observed",
		}
		emitInferredHookEvent(logger, "approval.allowed", "approval", "info", "Shell execution observed", input, fields)
		return true
	case "beforeReadFile":
		fields := sessionFields(sessionID, input)
		if filePath := getFirstStr(input, "file_path", "filePath", "path"); filePath != "" {
			fields["file"] = map[string]interface{}{
				"path":      filePath,
				"operation": "read",
				"language":  strings.TrimPrefix(filepath.Ext(filePath), "."),
			}
		}
		emitHookEvent(logger, "file.read", "file", "info", "File read observed", input, fields)
		return true
	default:
		emitPreToolDecision(logger, input, sessionID, "approval.allowed", "allow", "Pre-tool observed", asymptoteobserve.FidelityInferred)
		return true
	}
}

// emitPreToolDecision writes an approval-category event for a tool call.
//
// fidelity is a required parameter rather than a constant inside because this one function serves
// both kinds of caller, and the difference between them is the whole point of the field. The
// permission-request hook reaches it when a runtime genuinely asked for a decision; the pre-tool
// hook reaches it on runtimes that expose no approval gate at all, where the approval block below
// describes what Beacon concluded from seeing a tool call rather than anything an operator did.
func emitPreToolDecision(logger *logging.Logger, input map[string]interface{}, sessionID, action, decision, reason, fidelity string) {
	toolName := getFirstStr(input, "tool_name", "toolName")
	toolInput := resolveToolInput(input)
	fields := sessionFields(sessionID, input)
	for key, value := range toolFields(toolName, toolInput) {
		fields[key] = value
	}
	fields["approval"] = map[string]interface{}{
		"required": true,
		"decision": decision,
		"reason":   reason,
	}
	emitHookEventWithFidelity(logger, action, "approval", "info", reason, fidelity, input, fields)
}

func preToolResponse() map[string]interface{} {
	if platformFlag == "antigravity" || platformFlag == "grok" {
		return map[string]interface{}{"decision": "allow"}
	}
	// Qwen Code belongs with claude here, and the reason is a security property rather than a
	// convention. Qwen's PreToolUse contract reads a decision from
	// `hookSpecificOutput.permissionDecision`, where "allow" means *run the tool without the usual
	// approval prompt*. An observing hook that answered "allow" would therefore not be observing:
	// it would silently disarm the user's own permission prompts for every tool call, on a runtime
	// where the hook was installed to watch. An empty object carries no decision, so Qwen's normal
	// permission flow runs untouched -- which is the only correct answer for a telemetry hook, and
	// what TestQwenPreToolDoesNotApproveOnBehalfOfTheUser holds in place.
	// Muse Code requires the empty object for a second reason on top of the one above, and it is
	// not a preference: its hook runner rejects a stdout object carrying keys it does not know, so
	// `{"permission":"allow"}` would not read as a permissive answer -- it would fail the hook run
	// outright. Emitting nothing speculative is the only shape that leaves a Muse turn untouched.
	// OpenHands is here for the Qwen reason rather than the Muse one. It parses a hook's stdout as
	// JSON and acts on `decision`, `reason`, `additionalContext` and `continue`; a `permission` key
	// is not among them, so `{"permission":"allow"}` is inert on today's build. It is still the
	// wrong thing to send. The string is stored verbatim on the HookExecutionEvent OpenHands shows
	// in the conversation, so it puts a decision Beacon did not make in front of the user -- and if
	// OpenHands ever reads that key, an observing hook would begin approving tool calls on the
	// user's behalf without a line of Beacon changing. An empty object asserts nothing either way.
	// Kiro reaches this branch and never uses what it returns: hookStdoutIsConsumedAsAgentContext
	// suppresses the write entirely, because on Kiro stdout is model context rather than a
	// response object. The empty object is still the right value to hand back -- it is what the
	// suppression would have to fall back to if that ever changed, and it keeps this function
	// answering the same question for every runtime rather than having one whose answer is
	// "nothing, and the writer knows why".
	// goose is the one runtime where the empty object is the harmful answer, so it is answered
	// before the group below rather than joining it. gooseBlockingEventResponse carries why, and
	// carries it once because Stop needs the same answer for the same reason.
	if platformFlag == goosePlatform {
		return gooseBlockingEventResponse
	}
	if platformFlag == "claude" || platformFlag == "qwen" || isDevinLikePlatform(platformFlag) || platformFlag == "hermes" || platformFlag == "vscode" || platformFlag == "muse" || platformFlag == openHandsPlatform || platformFlag == kiroPlatform {
		return emptyResponse
	}
	return allowResponse
}

func emitPreToolObserved(logger *logging.Logger, input map[string]interface{}, sessionID string) {
	toolName := getFirstStr(input, "tool_name", "toolName")
	if platformFlag == "antigravity" {
		toolName = antigravityToolName(input)
	}
	toolInput := resolveToolInput(input)
	fields := sessionFields(sessionID, input)
	for key, value := range toolFields(toolName, toolInput) {
		fields[key] = value
	}
	emitHookEvent(logger, "tool.invoked", "tool", "info", "Tool invocation observed", input, fields)
}

func emitAntigravityPromptFromTranscript(logger *logging.Logger, input map[string]interface{}, sessionID string) {
	if sessionID == "" {
		return
	}
	st := state.NewSessionState(sessionID, "antigravity")
	if st.HasPromptEmitted() {
		return
	}
	prompt := antigravityPromptFromTranscript(input, sessionID)
	if prompt == "" {
		return
	}
	fields := sessionFields(sessionID, input)
	fields["prompt"] = map[string]interface{}{"text": prompt}
	fields["gen_ai"] = mergeNested(fields["gen_ai"], map[string]interface{}{
		"input": map[string]interface{}{"messages": asymptoteobserve.TextInputMessages(prompt)},
	})
	fields["content"] = retainedContentFields(prompt)
	emitHookEvent(logger, "prompt.submitted", "prompt", "info", "Prompt submitted to agent", input, fields)
	if err := st.SetPromptEmitted(); err != nil {
		logger.Warn("Failed to persist prompt state", "error", err.Error())
	}
}

func antigravityPromptFromTranscript(input map[string]interface{}, sessionID string) string {
	path := getFirstStr(input, "transcriptPath", "transcript_path")
	if path == "" {
		path = defaultAntigravityTranscriptPath(sessionID)
	}
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	for scanner.Scan() {
		var entry map[string]interface{}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		source := strings.ToUpper(getFirstStr(entry, "source"))
		entryType := strings.ToUpper(getFirstStr(entry, "type"))
		if source != "USER_EXPLICIT" && entryType != "USER_INPUT" {
			continue
		}
		if content := getFirstStr(entry, "content"); content != "" {
			return stripAntigravityPromptWrappers(content)
		}
	}
	return ""
}

func defaultAntigravityTranscriptPath(sessionID string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".gemini", "antigravity-cli", "brain", sessionID, ".system_generated", "logs", "transcript.jsonl")
}

func stripAntigravityPromptWrappers(content string) string {
	if start := strings.Index(content, "<USER_REQUEST>"); start >= 0 {
		content = content[start+len("<USER_REQUEST>"):]
		if end := strings.Index(content, "</USER_REQUEST>"); end >= 0 {
			content = content[:end]
		}
	}
	return strings.TrimSpace(content)
}
