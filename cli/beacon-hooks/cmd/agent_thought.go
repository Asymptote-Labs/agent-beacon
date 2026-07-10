package cmd

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

var agentThoughtCmd = &cobra.Command{
	Use:   "agent-thought",
	Short: "Record agent reasoning output for local endpoint telemetry",
	Long: `afterAgentThought hook - triggered when the agent completes a thinking block.
Records the aggregated reasoning text as local endpoint telemetry.`,
	Run: runAgentThought,
}

func init() {
	rootCmd.AddCommand(agentThoughtCmd)
}

func runAgentThought(cmd *cobra.Command, args []string) {
	input, err := readStdinJSON()
	if err != nil {
		outputJSON(hookNoopResponse())
		return
	}

	sessionID := resolveSessionID(input, platformFlag)
	logger := newHookLogger("agent-thought", platformFlag, sessionID)
	logger.Debug("Agent thought observed")

	text := getFirstStr(input, "text", "thought", "thinking")
	if text == "" {
		outputJSON(hookNoopResponse())
		return
	}

	fields := sessionFields(sessionID, input)
	fields["gen_ai"] = map[string]interface{}{
		"output": map[string]interface{}{
			"messages": reasoningOutputMessages(text),
		},
	}
	fields["content"] = retainedContentFields(text)
	if meta := thoughtMetadataFields(input); len(meta) > 0 {
		fields["raw"] = map[string]interface{}{platformFlag: meta}
	}
	emitHookEvent(logger, "agent.reasoning", "session", "info", "Agent reasoning captured", input, fields)
	outputJSON(hookNoopResponse())
}

// reasoningOutputMessages wraps reasoning text in the OpenTelemetry GenAI
// output-messages shape (a single assistant message with a reasoning part), so
// hook-captured reasoning matches what semconv-native OTLP sources emit.
func reasoningOutputMessages(text string) []interface{} {
	return []interface{}{
		map[string]interface{}{
			"role": "assistant",
			"parts": []interface{}{
				map[string]interface{}{"type": "reasoning", "content": text},
			},
		},
	}
}

// retainedContentFields builds the content marker for an event that retains
// raw text, computed against the original text so hash and byte count stay
// stable even after the sink redacts or truncates the stored copy.
func retainedContentFields(text string) map[string]interface{} {
	sum := sha256.Sum256([]byte(text))
	fields := map[string]interface{}{
		"retention": asymptoteobserve.ContentRetentionFull,
		"included":  true,
		"hash":      hex.EncodeToString(sum[:]),
		"bytes":     len(text),
	}
	if len(text) > asymptoteobserve.DefaultStringLimit {
		fields["truncated"] = true
	}
	if asymptoteobserve.RedactString(text) != text {
		fields["redacted"] = true
	}
	return fields
}

// thoughtMetadataFields extracts the non-content afterAgentThought payload
// metadata worth retaining; the reasoning text itself is excluded because it
// already lives in gen_ai.output.messages.
func thoughtMetadataFields(input map[string]interface{}) map[string]interface{} {
	meta := map[string]interface{}{}
	if duration, ok := firstToolIntAcross([]map[string]interface{}{input}, "duration_ms", "durationMs"); ok {
		meta["duration_ms"] = duration
	}
	if generationID := getFirstStr(input, "generation_id", "generationId"); generationID != "" {
		meta["generation_id"] = generationID
	}
	return meta
}
