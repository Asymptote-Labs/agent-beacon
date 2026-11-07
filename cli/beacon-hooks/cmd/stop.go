package cmd

import (
	"bufio"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/state"
)

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Record agent response completion",
	Long: `Stop hook - triggered when the coding agent finishes responding.
The public Beacon build does not poll hosted evaluations.`,
	Run: runStop,
}

func init() {
	rootCmd.AddCommand(stopCmd)
}

// stopResponse is the no-op reply for the Stop hook, per runtime.
//
// It exists for goose and only for goose, and it is the same hazard preToolResponse answers: goose
// runs Stop through emit_blocking, so the `{}` this hook used to write on every path is classified
// as a failed hook rather than as no opinion -- once per turn, for a hook whose entire job is to
// observe one. Every other runtime ignores this hook's stdout or reads `{}` as no opinion, and
// keeps getting it.
func stopResponse() map[string]interface{} {
	if platformFlag == goosePlatform {
		return gooseBlockingEventResponse
	}
	return emptyResponse
}

func runStop(cmd *cobra.Command, args []string) {
	start := time.Now()

	input, err := readStdinJSON()
	if err != nil {
		outputJSONAndExit(stopResponse())
		return
	}

	sessionID, transcriptPath := resolveSessionIDWithTranscript(input, platformFlag)

	logger := newHookLogger("stop", platformFlag, sessionID)

	logger.Debug("Stop hook called", "session_id", sessionID, "has_transcript", transcriptPath != "", "platform", platformFlag)
	// Kiro and Claude Code put the agent's final reply on this payload. Recorded before the
	// completion event below and before the sessionID guard, because the reply happened before the
	// turn ended and because a payload with no session id still carries real model output --
	// dropping it there would lose the message to protect a field it does not need.
	emitStopAssistantResponse(logger, input, sessionID)
	if sessionID == "" {
		if isDevinLikePlatform(platformFlag) {
			logger.Info("stop completed")
			emitHookEvent(logger, "tool.completed", "tool", "info", "Agent response completed", input, stopFields("", input))
			uploadCloudTelemetry(logger, true)
			outputJSON(stopResponse())
			return
		}
		uploadCloudTelemetry(logger, true)
		outputJSONAndExit(stopResponse())
		return
	}

	config.RotateLogIfNeededForPlatform(platformFlag)
	st := state.NewSessionState(sessionID, platformFlag)
	// Factory Droid's Stop payload carries no reply; its transcript does.
	if platformFlag == "factory" {
		emitFactoryTranscriptReply(logger, input, sessionID, transcriptPath, st)
	}

	elapsed := time.Since(start)
	logger.Info("stop completed", "duration_ms", elapsed.Milliseconds())
	emitHookEvent(logger, "tool.completed", "tool", "info", "Agent response completed", input, stopFields(sessionID, input))
	uploadCloudTelemetry(logger, true)
	outputJSONAndExit(stopResponse())
}

// stopFields is the turn-end event's fields: the session, plus how the turn ended when the runtime
// says so.
//
// The action and message stay tool.completed / "Agent response completed" on every runtime, because
// the dashboard and TraceMessages recognize the turn end by that shape. What changes is that Cursor,
// whose stop hook sends status completed, aborted or error, now has that status recorded verbatim
// as turn.status, so in the event log and to threat rules a stopped or failed turn no longer reads
// exactly like a finished one.
//
// Read on Cursor only, as subagent.go reads Cursor's subagentStop status. As of each runtime's hook
// reference, no other runtime that reaches this hook sends a top-level status on Stop (Claude Code
// and Qwen nest one per background task, which is not the turn's outcome), so a status key showing
// up elsewhere is not known to mean how the turn ended. A payload with no status gets no turn block
// at all rather than a default.
func stopFields(sessionID string, input map[string]interface{}) map[string]interface{} {
	fields := sessionFields(sessionID, input)
	if platformFlag != "cursor" {
		return fields
	}
	if status := strings.TrimSpace(getFirstStr(input, "status")); status != "" {
		fields["turn"] = map[string]interface{}{"status": status}
	}
	return fields
}

// platformToTranscriptName maps the platform flag to the transcript platform identifier.
func platformToTranscriptName(platform string) string {
	switch platform {
	case "copilot":
		return "copilot"
	case "cursor":
		return "cursor"
	case "vscode":
		return "vscode"
	case "factory":
		return "factory"
	case "devin", "devin-cli", "devin-desktop":
		return "devin"
	default:
		return "claude_code"
	}
}

// extractMessages dispatches to the correct transcript parser based on platform.
func extractMessages(transcriptPath, platform string) []map[string]interface{} {
	switch platform {
	case "copilot":
		return extractMessagesFromCopilotTranscript(transcriptPath)
	case "cursor", "vscode":
		return extractMessagesFromCursorTranscript(transcriptPath)
	case "factory":
		return extractMessagesFromFactoryTranscript(transcriptPath)
	case "devin", "devin-cli", "devin-desktop":
		return extractMessagesFromClaudeTranscript(transcriptPath)
	default:
		return extractMessagesFromClaudeTranscript(transcriptPath)
	}
}

// extractMessagesFromClaudeTranscript parses Claude Code's JSONL transcript format.
func extractMessagesFromClaudeTranscript(transcriptPath string) []map[string]interface{} {
	file, err := os.Open(transcriptPath)
	if err != nil {
		return nil
	}
	defer file.Close()

	var messages []map[string]interface{}
	decoder := json.NewDecoder(file)
	for decoder.More() {
		var entry map[string]interface{}
		if err := decoder.Decode(&entry); err != nil {
			break
		}

		msgType, _ := entry["type"].(string)
		message, _ := entry["message"].(map[string]interface{})

		// Skip meta entries (e.g., Conductor local-command-caveat messages),
		// but preserve stop hook feedback which also has isMeta=true.
		if isMeta, _ := entry["isMeta"].(bool); isMeta {
			if !isStopHookFeedback(entry) {
				continue
			}
		}

		switch msgType {
		case "user":
			var userContent string
			if content, ok := message["content"].(string); ok && content != "" {
				// Terminal Claude Code: content is a plain string
				if isLocalCommandBlock(content) {
					continue
				}
				userContent = stripSystemInstructions(content)
			} else if contentBlocks, ok := message["content"].([]interface{}); ok {
				// VS Code Claude Code: content is an array of content blocks
				userContent = extractTextFromBlocks(contentBlocks, true)
			}
			if userContent != "" {
				messages = append(messages, map[string]interface{}{
					"role":    "user",
					"content": userContent,
				})
			}
		case "assistant":
			contentBlocks, _ := message["content"].([]interface{})
			if text := extractTextFromBlocks(contentBlocks, false); text != "" {
				messages = append(messages, map[string]interface{}{
					"role":    "assistant",
					"content": text,
				})
			}
		}
	}

	return messages
}

// extractMessagesFromCopilotTranscript parses Copilot's JSONL transcript format.
func extractMessagesFromCopilotTranscript(transcriptPath string) []map[string]interface{} {
	file, err := os.Open(transcriptPath)
	if err != nil {
		return nil
	}
	defer file.Close()

	var messages []map[string]interface{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var entry map[string]interface{}
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}

		entryType, _ := entry["type"].(string)
		data, _ := entry["data"].(map[string]interface{})
		if data == nil {
			continue
		}

		var role string
		switch entryType {
		case "user.message":
			role = "user"
		case "assistant.message":
			role = "assistant"
		default:
			continue
		}

		if content, ok := data["content"].(string); ok && content != "" {
			messages = append(messages, map[string]interface{}{
				"role":    role,
				"content": content,
			})
		}
	}

	return messages
}

// extractMessagesFromCursorTranscript parses Cursor's JSONL transcript format.
// Format: {"role":"user","message":{"content":[{"type":"text","text":"..."}]}}
func extractMessagesFromCursorTranscript(transcriptPath string) []map[string]interface{} {
	file, err := os.Open(transcriptPath)
	if err != nil {
		return nil
	}
	defer file.Close()

	var messages []map[string]interface{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var entry map[string]interface{}
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}

		role, _ := entry["role"].(string)
		if role != "user" && role != "assistant" {
			continue
		}

		message, _ := entry["message"].(map[string]interface{})
		if message == nil {
			continue
		}

		contentBlocks, _ := message["content"].([]interface{})
		if text := extractTextFromBlocks(contentBlocks, false); text != "" {
			content := stripCursorXMLTags(text)
			if content != "" {
				messages = append(messages, map[string]interface{}{
					"role":    role,
					"content": content,
				})
			}
		}
	}

	return messages
}

// cursorXMLTagPattern matches known Cursor-injected wrapper tags in transcript text.
// These tags are added by Cursor to wrap user input and context — they are not part
// of the actual conversation content.
// Known tags: <user_query>, <attached_files>, <git_diff_from_branch_to_main>
var cursorXMLTagPattern = sync.OnceValue(func() *regexp.Regexp {
	return regexp.MustCompile(`</?(?:user_query|attached_files|git_diff_from_branch_to_main)>`)
})

// stripCursorXMLTags removes Cursor's wrapper tags from transcript text.
func stripCursorXMLTags(text string) string {
	text = cursorXMLTagPattern().ReplaceAllString(text, "")
	return strings.TrimSpace(text)
}

// extractMessagesFromFactoryTranscript parses Factory's JSONL transcript format.
// Format: {"type":"message","message":{"role":"user"|"assistant","content":[{"type":"text","text":"..."}]}}
// Skips tool_use-only assistant messages and tool_result-only user messages.
func extractMessagesFromFactoryTranscript(transcriptPath string) []map[string]interface{} {
	file, err := os.Open(transcriptPath)
	if err != nil {
		return nil
	}
	defer file.Close()

	var messages []map[string]interface{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var entry map[string]interface{}
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}

		entryType, _ := entry["type"].(string)
		if entryType != "message" {
			continue
		}

		message, _ := entry["message"].(map[string]interface{})
		if message == nil {
			continue
		}

		role, _ := message["role"].(string)
		if role != "user" && role != "assistant" {
			continue
		}

		contentBlocks, _ := message["content"].([]interface{})

		// Skip messages that have no text blocks (e.g., tool_use-only or tool_result-only)
		if !hasTextBlocks(contentBlocks) {
			continue
		}

		if text := extractTextFromBlocks(contentBlocks, role == "user"); text != "" {
			messages = append(messages, map[string]interface{}{
				"role":    role,
				"content": text,
			})
		}
	}

	return messages
}

// hasTextBlocks returns true if the content blocks contain at least one text block.
func hasTextBlocks(blocks []interface{}) bool {
	for _, block := range blocks {
		blockMap, ok := block.(map[string]interface{})
		if !ok {
			continue
		}
		if blockMap["type"] == "text" {
			return true
		}
	}
	return false
}

// extractTextFromBlocks extracts and joins text content from an array of content blocks.
// When filterIDEContext is true, IDE-injected context blocks are excluded (for user messages).
func extractTextFromBlocks(blocks []interface{}, filterIDEContext bool) string {
	var parts []string
	for _, block := range blocks {
		blockMap, ok := block.(map[string]interface{})
		if !ok || blockMap["type"] != "text" {
			continue
		}
		text, ok := blockMap["text"].(string)
		if !ok || text == "" {
			continue
		}
		if filterIDEContext && isIDEContextBlock(text) {
			continue
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, " ")
}

// isIDEContextBlock checks if a text block is IDE-injected context
// (e.g. <ide_opened_file>...</ide_opened_file> or <system-reminder>...</system-reminder>)
func isIDEContextBlock(text string) bool {
	trimmed := strings.TrimSpace(text)
	return strings.HasPrefix(trimmed, "<ide_") ||
		strings.HasPrefix(trimmed, "<system-reminder>") ||
		strings.HasPrefix(trimmed, "<system_instruction>") ||
		strings.HasPrefix(trimmed, "<system-instruction>") ||
		strings.HasPrefix(trimmed, "<local-command-caveat>") ||
		strings.HasPrefix(trimmed, "<command-name>") ||
		strings.HasPrefix(trimmed, "<local-command-stdout>")
}

// isStopHookFeedback checks if a transcript entry is stop hook feedback from Beacon.
// These entries have isMeta=true but contain security feedback that should be in the transcript.
func isStopHookFeedback(entry map[string]interface{}) bool {
	msgType, _ := entry["type"].(string)
	if msgType != "user" {
		return false
	}
	message, _ := entry["message"].(map[string]interface{})
	if message == nil {
		return false
	}
	content, _ := message["content"].(string)
	return strings.HasPrefix(content, "Stop hook feedback:")
}

// isLocalCommandBlock checks if a plain-string user message is a Conductor local command block.
func isLocalCommandBlock(text string) bool {
	trimmed := strings.TrimSpace(text)
	return strings.HasPrefix(trimmed, "<local-command-caveat>") ||
		strings.HasPrefix(trimmed, "<command-name>") ||
		strings.HasPrefix(trimmed, "<local-command-stdout>")
}

// systemInstructionPattern matches <system_instruction>...</system_instruction> and
// <system-instruction>...</system-instruction> blocks including their content.
var systemInstructionPattern = sync.OnceValue(func() *regexp.Regexp {
	return regexp.MustCompile(`(?s)<system[_-]instruction>.*?</system[_-]instruction>\s*`)
})

// stripSystemInstructions removes system instruction blocks from user message text.
func stripSystemInstructions(text string) string {
	return strings.TrimSpace(systemInstructionPattern().ReplaceAllString(text, ""))
}
