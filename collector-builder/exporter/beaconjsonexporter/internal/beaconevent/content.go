package beaconevent

import (
	"strings"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// PromptTextKeys are the attribute spellings that carry a user turn's text.
// Shared with PopulateCommon so the early and late prompt promotions read the
// same list rather than two copies that can disagree about which runtimes are
// covered.
//
// Bare `prompt` is safe here only because every reader gates on the event being a
// prompt in the first place.
var PromptTextKeys = []string{
	"beacon.prompt.text",
	"gen_ai.prompt",
	"prompt",
	"user_prompt",
	"input.prompt",
	"copilot_chat.user_request",
}

// assistantTextKeys are the spellings that carry an assistant turn's text.
//
// Deliberately specific, and with no bare `response`: nothing gates this list on
// the event being a response, and `response` on its own appears on events that
// are not a turn at all -- Claude Code's `feedback_survey` carries
// `response: "positive"`. Every entry is either a semconv-adjacent name or a
// runtime-namespaced one. The bare spelling is read only on the event whose own
// name says what it is, in claudeAssistantText.
var assistantTextKeys = []string{
	"beacon.gen_ai.output.text",
	"gen_ai.completion",
	"gen_ai.response",
	"gen_ai.output.text",
	"response.text",
	"output.text",
	"completion",
	"assistant_response",
}

// claudeAssistantText reads the model's reply off a Claude Code log record.
//
// This is where the largest single content gap on the OTLP path was: Claude Code
// emits the whole reply as `response` on `claude_code.assistant_response` and
// nothing read it, so the text sat in raw.attributes and the event carried no
// assistant output at all. The attribute is read only under that event name,
// which is what keeps a survey answer from being recorded as something the model
// said.
func claudeAssistantText(harness, eventName string, attrs map[string]interface{}) string {
	if harness != "claude_code" || eventName != ClaudeAssistantResponse {
		return ""
	}
	return FirstTextAttr(attrs, "response")
}

// PromoteRetainedContent fills the fields that carry a turn's text -- prompt.text,
// gen_ai.input.messages, gen_ai.output.messages -- and stamps the content marker
// describing what was retained.
//
// It runs after the per-harness normalizers rather than inside PopulateCommon,
// because the normalizers are what decide an event is a prompt: PopulateCommon
// only promoted prompt text when the category was already "prompt" at the moment
// it ran, which for a runtime whose classification lands later meant the text was
// dropped even though the attribute was right there.
//
// Nothing here overwrites text a source stated in semconv form. A record that
// already carries gen_ai.output.messages keeps them; these promotions exist for
// runtimes that report a turn under their own attribute names.
func (c Converter) PromoteRetainedContent(event *Event, attrs map[string]interface{}, body string) {
	if event == nil {
		return
	}
	eventName := ClaudeLogEventName(attrs, body)

	promptText := ""
	if event.Prompt != nil {
		promptText = event.Prompt.Text
	}
	if promptText == "" && isPromptEvent(event) {
		promptText = FirstNonEmpty(FirstTextAttr(attrs, PromptTextKeys...), FirstMessageText(event.GenAI))
		if promptText != "" {
			event.Prompt = &PromptInfo{Text: promptText}
		}
	}

	assistantText := FirstNonEmpty(
		claudeAssistantText(event.Harness.Name, eventName, attrs),
		FirstTextAttr(attrs, assistantTextKeys...),
	)
	if assistantText != "" && !hasOutputMessages(event) {
		if event.GenAI == nil {
			event.GenAI = &GenAIInfo{}
		}
		if event.GenAI.Output == nil {
			event.GenAI.Output = &GenAIOutputInfo{}
		}
		event.GenAI.Output.Messages = asymptoteobserve.TextOutputMessages(assistantText)
	} else {
		// Cleared, not just unwritten: the marker below describes text this function
		// retained, and when the source's own messages are what got stored, nothing
		// here did. Hashing a structured messages payload as if it were one string
		// would produce a marker that describes neither.
		assistantText = ""
	}

	if promptText != "" && !hasInputMessages(event) {
		if event.GenAI == nil {
			event.GenAI = &GenAIInfo{}
		}
		if event.GenAI.Input == nil {
			event.GenAI.Input = &GenAIInputInfo{}
		}
		event.GenAI.Input.Messages = asymptoteobserve.TextInputMessages(promptText)
	}

	if event.Content != nil {
		return
	}
	// The marker describes one body of retained text and the limit that will be
	// applied to where that text is stored, which is why the two cases carry
	// different limits: gen_ai is sanitized at the raw-attribute limit and
	// prompt.text at the longer string limit. Assistant text wins when an event
	// somehow carries both, because it is the larger of the two in every payload
	// observed.
	switch {
	case assistantText != "":
		event.Content = asymptoteobserve.RetainedContent(assistantText, asymptoteobserve.DefaultRawStringLimit)
	case promptText != "":
		event.Content = asymptoteobserve.RetainedContent(promptText, asymptoteobserve.DefaultStringLimit)
	}
}

// isPromptEvent reports whether an event describes a user turn, and so whether
// prompt-shaped attributes on it are that turn's text.
func isPromptEvent(event *Event) bool {
	return event.Event.Category == "prompt" || strings.HasPrefix(event.Event.Action, "prompt.")
}

func hasOutputMessages(event *Event) bool {
	return event.GenAI != nil && event.GenAI.Output != nil && event.GenAI.Output.Messages != nil
}

func hasInputMessages(event *Event) bool {
	return event.GenAI != nil && event.GenAI.Input != nil && event.GenAI.Input.Messages != nil
}
