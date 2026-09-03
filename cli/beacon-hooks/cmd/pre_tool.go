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
	if deny, denied := enforcePolicy(logger, input, sessionID, policycontract.PhasePreTool); denied {
		outputJSON(deny)
		return
	}
	if platformFlag == "cursor" && emitCursorPreHook(logger, input, sessionID) {
		maybeUploadCursorCloudTelemetry(logger)
	} else if platformFlag == "antigravity" {
		emitAntigravityPromptFromTranscript(logger, input, sessionID)
		emitPreToolObserved(logger, input, sessionID)
	} else if platformFlag == "claude" || platformFlag == "qwen" || isDevinLikePlatform(platformFlag) || platformFlag == "grok" || platformFlag == "hermes" || platformFlag == "vscode" || platformFlag == "muse" {
		// Muse Code belongs on the observing side rather than with the runtimes whose pre-tool
		// notification gets turned into a synthesized approval, and the reason is that it has a
		// real one. Its PermissionRequest event is a separate hook Beacon also subscribes to, so
		// deriving an approval.allowed from PreToolUse as well would record two approvals for one
		// tool call -- one of them inferred and describing nothing an operator did -- and put an
		// invented decision next to a reported one for the same call.
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
	if platformFlag == "claude" || platformFlag == "qwen" || isDevinLikePlatform(platformFlag) || platformFlag == "hermes" || platformFlag == "vscode" || platformFlag == "muse" {
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
