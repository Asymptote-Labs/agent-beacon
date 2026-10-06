package beaconevent

import (
	"encoding/json"
	"strings"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

const claudeWebFetchApplyQuerySource = "web_fetch_apply"

// NormalizeClaudeWebFetchAuxiliaryRequest promotes only user-role text from Claude Code's
// WebFetch apply request body. Claude Code 2.1.291 sets query_source=web_fetch_apply when WebFetch
// sends fetched page text to its auxiliary summarizer; other API bodies can contain the full
// conversation, system prompt, tool schemas, and file results, so none of those are retained here.
func NormalizeClaudeWebFetchAuxiliaryRequest(event *Event, attrs map[string]interface{}) {
	if event == nil || !isClaudeOTelLogHarness(event.Harness.Name) {
		return
	}
	name := ClaudeLogEventName(attrs, "")
	if name != "claude_code.api_request_body" && name != "claude_code.api_response_body" {
		return
	}
	var body string
	if name == "claude_code.api_request_body" && FirstString(attrs, "query_source") == claudeWebFetchApplyQuerySource {
		body = FirstString(attrs, "body")
	}
	delete(attrs, "body")
	delete(attrs, "body_ref")
	if body == "" {
		return
	}
	text := claudeWebFetchUserText(body)
	if text == "" {
		return
	}
	event.Prompt = &PromptInfo{Text: text}
	event.Content = asymptoteobserve.RetainedContent(text, asymptoteobserve.DefaultStringLimit)
}

// claudeWebFetchUserText decodes only text parts of user messages. It deliberately ignores system
// prompts, tool definitions and non-text blocks, any of which can carry unrelated or prohibited
// content when another API request body reaches the same telemetry pipeline.
func claudeWebFetchUserText(body string) string {
	var request struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.NewDecoder(strings.NewReader(body)).Decode(&request) != nil {
		return ""
	}
	var parts []string
	for _, message := range request.Messages {
		if message.Role != "user" {
			continue
		}
		var plain string
		if json.Unmarshal(message.Content, &plain) == nil {
			if plain != "" {
				parts = append(parts, plain)
			}
			continue
		}
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(message.Content, &blocks) != nil {
			continue
		}
		for _, block := range blocks {
			if block.Type == "text" && block.Text != "" {
				parts = append(parts, block.Text)
			}
		}
	}
	return strings.Join(parts, "\n")
}
