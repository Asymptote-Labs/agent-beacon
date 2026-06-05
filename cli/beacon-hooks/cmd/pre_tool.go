package cmd

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/logging"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/state"
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
	var logger *logging.Logger
	if sessionID != "" {
		logger = logging.NewSessionLogger("pre-tool", platformFlag, sessionID)
	} else {
		logger = logging.NewLoggerForPlatform("pre-tool", platformFlag)
	}

	logger.Debug("Pre-tool observed")
	if platformFlag == "antigravity" {
		emitAntigravityPromptFromTranscript(logger, input, sessionID)
		emitPreToolObserved(logger, input, sessionID)
	} else if platformFlag == "claude" || isDevinLikePlatform(platformFlag) || platformFlag == "grok" || platformFlag == "hermes" || platformFlag == "vscode" {
		emitPreToolObserved(logger, input, sessionID)
	} else {
		emitPreToolDecision(logger, input, sessionID, "approval.allowed", "allow", "Pre-tool observed")
	}
	outputJSON(preToolResponse())
}

func emitPreToolDecision(logger *logging.Logger, input map[string]interface{}, sessionID, action, decision, reason string) {
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
	emitHookEvent(logger, action, "approval", "info", reason, input, fields)
}

func preToolResponse() map[string]interface{} {
	if platformFlag == "antigravity" || platformFlag == "grok" {
		return map[string]interface{}{"decision": "allow"}
	}
	if platformFlag == "claude" || isDevinLikePlatform(platformFlag) || platformFlag == "hermes" || platformFlag == "vscode" {
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
	if config.ContentRetentionMode() != config.ContentRetentionMetadata {
		fields["prompt"] = map[string]interface{}{"text": prompt}
	}
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
