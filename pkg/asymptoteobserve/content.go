package asymptoteobserve

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// Roles and part types Beacon writes into gen_ai.input.messages and
// gen_ai.output.messages. They are the OpenTelemetry GenAI semconv spellings,
// not Beacon's own: a consumer that already reads semconv message shapes reads
// these without a Beacon-specific mapping.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleSystem    = "system"

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

// SystemInstructionParts is text in the gen_ai.system_instructions shape. The
// semconv defines that attribute as a bare list of message parts -- no role and
// no message wrapper, unlike gen_ai.input.messages -- so a Beacon-written value
// and one an OTLP producer sends are read the same way.
func SystemInstructionParts(text string) []interface{} {
	return []interface{}{
		map[string]interface{}{"type": GenAIPartTypeText, "content": text},
	}
}

// GenAIText returns the content of every part of partType in a GenAI messages or
// parts value, in order, each trimmed, blanks dropped. It is the one reader for
// gen_ai.input.messages, gen_ai.output.messages and gen_ai.system_instructions,
// and it accepts every shape the capture paths write or receive:
//
//   - the semconv message list, [{"role", "parts": [{"type", "content"}]}];
//   - a bare parts list, the gen_ai.system_instructions shape;
//   - a message whose body is "content" rather than "parts", as a string or a
//     block list, and a part whose text is under "text" rather than "content",
//     the shapes several session mappers and providers write;
//   - a {"messages": [...]} wrapper;
//   - a bare string, as the whole value, a message body or a list element,
//     which is text and never reasoning;
//   - a typed value from an in-process caller, normalized through JSON.
//
// A map with a role, a parts list or type "message" is a message, any other map
// with a type is a part, and a map with neither is a message. role, when not
// empty, keeps only messages from that role or naming none; a part outside any
// message is kept.
//
// It does no redaction or truncation; callers apply the limit that governs
// where the text goes.
func GenAIText(value interface{}, role, partType string) []string {
	var out []string
	collectGenAIText(value, role, partType, &out)
	return out
}

func collectGenAIText(v interface{}, role, partType string, out *[]string) {
	switch typed := v.(type) {
	case string:
		if text := strings.TrimSpace(typed); text != "" && partType == GenAIPartTypeText {
			*out = append(*out, text)
		}
	case []interface{}:
		for _, item := range typed {
			collectGenAIText(item, role, partType, out)
		}
	case map[string]interface{}:
		if messages, ok := typed["messages"]; ok {
			collectGenAIText(messages, role, partType, out)
			return
		}
		kind, _ := typed["type"].(string)
		_, hasRole := typed["role"]
		body, hasParts := typed["parts"]
		if kind != "" && kind != "message" && !hasRole && !hasParts {
			if kind != partType {
				return
			}
			text, _ := typed["content"].(string)
			if strings.TrimSpace(text) == "" {
				text, _ = typed["text"].(string)
			}
			if text = strings.TrimSpace(text); text != "" {
				*out = append(*out, text)
			}
			return
		}
		if messageRole, _ := typed["role"].(string); role != "" && messageRole != "" && messageRole != role {
			return
		}
		if !hasParts {
			body = typed["content"]
		}
		collectGenAIText(body, role, partType, out)
	case nil, bool, float64, float32, int, int64, int32, json.Number:
	default:
		// A typed value from an in-process caller rather than the generic shape a log
		// line decodes to. Normalized through JSON once; the result is handled above.
		data, err := json.Marshal(typed)
		if err != nil {
			return
		}
		var generic interface{}
		if json.Unmarshal(data, &generic) == nil {
			collectGenAIText(generic, role, partType, out)
		}
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
	return contentFields(RetainedContent(text, storeLimit))
}

func contentFields(info *ContentInfo) map[string]interface{} {
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
