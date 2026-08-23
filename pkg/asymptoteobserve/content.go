package asymptoteobserve

import (
	"crypto/sha256"
	"encoding/hex"
)

// Roles and part types Beacon writes into gen_ai.input.messages and
// gen_ai.output.messages. They are the OpenTelemetry GenAI semconv spellings,
// not Beacon's own: a consumer that already reads semconv message shapes reads
// these without a Beacon-specific mapping.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"

	GenAIPartTypeText      = "text"
	GenAIPartTypeReasoning = "reasoning"
)

// GenAIMessages wraps one piece of text in the GenAI messages shape: a single
// message from role carrying a single part of partType.
//
// One helper for every capture path. The hook adapter, the collector exporter
// and anything else that promotes retained text all reach for this, so
// hook-captured assistant text and semconv-native OTLP assistant text arrive in
// the log under the same structure rather than under two shapes a reader has to
// know about separately.
func GenAIMessages(role, partType, text string) []interface{} {
	return []interface{}{
		map[string]interface{}{
			"role": role,
			"parts": []interface{}{
				map[string]interface{}{"type": partType, "content": text},
			},
		},
	}
}

// TextOutputMessages is the assistant's own text -- what it said, as opposed to
// what it thought (ReasoningOutputMessages) -- in the gen_ai.output.messages shape.
func TextOutputMessages(text string) []interface{} {
	return GenAIMessages(RoleAssistant, GenAIPartTypeText, text)
}

// TextInputMessages is the user's text in the gen_ai.input.messages shape.
func TextInputMessages(text string) []interface{} {
	return GenAIMessages(RoleUser, GenAIPartTypeText, text)
}

// ReasoningOutputMessages is a thinking block in the gen_ai.output.messages
// shape, distinguished from TextOutputMessages only by its part type. Runtimes
// that expose reasoning separately from the response keep that distinction here.
func ReasoningOutputMessages(text string) []interface{} {
	return GenAIMessages(RoleAssistant, GenAIPartTypeReasoning, text)
}

// RetainedContent builds the content marker for an event that keeps text
// verbatim -- a prompt, an assistant response, command output, a diff.
//
// Hash and Bytes are computed over the original text, before the writer's
// redaction and truncation touch the stored copy, so an event keeps a stable
// identifier and a true size even when what is stored is shorter. That is the
// point of the marker: without it a reader cannot tell a short response from a
// truncated long one, and cannot tell that two truncated copies of the same
// response are the same response.
//
// storeLimit is the per-string truncation limit the writer emitting this event
// will apply to the text, and it is a parameter rather than a constant because
// the two writers do not agree on one: the hook adapter sanitizes a whole event
// map at DefaultStringLimit, while the collector applies DefaultRawStringLimit
// to gen_ai and DefaultStringLimit to prompt.text. Truncated has to describe the
// limit that actually applied, or it is a claim about a copy of the text that
// nobody stored. Pass 0 to leave Truncated unset.
//
// Returns nil for empty text: an event with nothing retained gets no marker
// rather than one asserting it retained the empty string.
func RetainedContent(text string, storeLimit int) *ContentInfo {
	if text == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(text))
	info := &ContentInfo{
		Retention: ContentRetentionFull,
		Included:  true,
		Hash:      hex.EncodeToString(sum[:]),
		Bytes:     len(text),
	}
	if storeLimit > 0 && len(text) > storeLimit {
		info.Truncated = true
	}
	if RedactString(text) != text {
		info.Redacted = true
	}
	return info
}

// RetainedContentFields is RetainedContent in the map form used by the hook
// adapter, which assembles events as maps rather than as the Event struct.
//
// The two forms must serialize identically -- a marker written by a hook and one
// written by the collector are the same object under the same keys -- and
// TestRetainedContentFieldsMatchesTheTypedMarker is what holds them together.
// Built field by field rather than round-tripped through JSON so the numbers stay
// integers: a float64 byte count would serialize the same but sit next to
// file.diff_bytes, an int, in the same fields map.
func RetainedContentFields(text string, storeLimit int) map[string]interface{} {
	info := RetainedContent(text, storeLimit)
	if info == nil {
		return nil
	}
	fields := map[string]interface{}{
		"retention": info.Retention,
		"included":  info.Included,
		"hash":      info.Hash,
		"bytes":     info.Bytes,
	}
	if info.Truncated {
		fields["truncated"] = true
	}
	if info.Redacted {
		fields["redacted"] = true
	}
	return fields
}
