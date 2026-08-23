package cmd

import (
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
	return asymptoteobserve.ReasoningOutputMessages(text)
}

// retainedContentFields builds the content marker for an event that retains raw
// text, computed against the original text so hash and byte count stay stable
// even after the sink redacts or truncates the stored copy.
//
// The limit passed is the one this writer applies: the hook logger sanitizes the
// whole event map at DefaultStringLimit, so that is what "truncated" has to mean
// here. The collector answers the same question with its own limit.
func retainedContentFields(text string) map[string]interface{} {
	return asymptoteobserve.RetainedContentFields(text, asymptoteobserve.DefaultStringLimit)
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
