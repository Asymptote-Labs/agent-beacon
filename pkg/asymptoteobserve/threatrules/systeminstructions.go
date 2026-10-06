package threatrules

import (
	"strings"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// SystemInstructionsTextPath is the CEL path of the derived system-instructions text, after
// the "e." prefix.
const SystemInstructionsTextPath = "gen_ai.system_instructions_text"

// SystemInstructionsPartLimit bounds each text part a rule sees, at the limit the writers apply
// to retained text. The bound is per part, not on the whole value, because a skill index holds one
// part per skill: capping the joined text would hide every skill after the first few.
const SystemInstructionsPartLimit = asymptoteobserve.DefaultStringLimit

// SystemInstructionsTextLimit bounds the whole derived text at the largest event a writer keeps,
// so it only limits an event built in memory; anything read from a log is already smaller.
const SystemInstructionsTextLimit = 64 * 1024

// SystemInstructionsText returns the text a rule sees as e.gen_ai.system_instructions_text: the
// instructions a runtime gave the model outside the conversation, such as the skill index Beacon
// records on session.context, or the system instructions an OTLP producer reports on an inference
// span. It is never operator-submitted prompt text.
//
// The value is read in its OpenTelemetry GenAI semconv shape, a list of message parts, by
// asymptoteobserve.GenAIText: the text of each `text` part, in order, one per line, and a bare
// string as text. Each part is capped at SystemInstructionsPartLimit and passed through the
// writers' secret redaction, and the text is empty when the event's content marker, if it has one,
// says content was not retained, so a rule never sees more than was retained.
//
// It is exported so that another engine implementing the Threat Rules contract can derive the
// field the same way; fixtures that exercise it only conform if the derivation agrees.
func SystemInstructionsText(event asymptoteobserve.Event) string {
	if event.GenAI == nil || event.GenAI.SystemInstructions == nil || !contentRetained(event.Content) {
		return ""
	}
	parts := asymptoteobserve.GenAIText(event.GenAI.SystemInstructions, "", asymptoteobserve.GenAIPartTypeText)
	for i, part := range parts {
		parts[i] = asymptoteobserve.CleanString(part, SystemInstructionsPartLimit, true)
	}
	return asymptoteobserve.TruncateString(strings.Join(parts, "\n"), SystemInstructionsTextLimit)
}
