package codec

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/gateway/llm"
)

func TestDetect(t *testing.T) {
	tests := []struct {
		path string
		want llm.Protocol
	}{
		// Model calls, under any prefix.
		{"/v1/messages", llm.AnthropicMessages},
		{"/anthropic/v1/messages?beta=true", llm.AnthropicMessages},
		{"/v1/messages/", llm.AnthropicMessages},
		{"/messages", llm.AnthropicMessages},
		{"/api/messages", llm.AnthropicMessages},
		{"/v1/chat/completions", llm.OpenAIChat},
		{"/openai/v1/chat/completions", llm.OpenAIChat},
		{"/api/v1/chat/completions", llm.OpenAIChat},
		{"/openai/deployments/gpt-4o/chat/completions/", llm.OpenAIChat},
		{"/openai/deployments/gpt-4o/chat/completions?api-version=2024-10-21", llm.OpenAIChat},
		{"/v1beta/openai/chat/completions", llm.OpenAIChat},
		{"/chat/completions", llm.OpenAIChat},
		{"/models/chat/completions?api-version=2024-05-01-preview", llm.OpenAIChat},
		{"/v1/projects/p/locations/us-central1/endpoints/openapi/chat/completions", llm.OpenAIChat},
		{"/api/chat/completions", llm.OpenAIChat},
		{"/llm/chat/completions", llm.OpenAIChat},
		{"/v1/responses", llm.OpenAIResponses},
		{"/openai/v1/responses", llm.OpenAIResponses},
		{"/openai/responses", llm.OpenAIResponses},
		{"/backend-api/codex/responses", llm.OpenAIResponses},
		{"/llm/responses", llm.OpenAIResponses},
		// Routes that are not model calls.
		{"/v1/messages/count_tokens", ""},
		{"/v1/messages/batches", ""},
		{"/v1/messages/batches/msgbatch_1/results", ""},
		{"/v1/threads/thread_abc/messages", ""},
		{"/openai/threads/thread_abc/messages?api-version=2024-05-01-preview", ""},
		{"/v1/threads/thread_abc/runs", ""},
		{"/v1/threads/thread_abc/chat/completions", ""},
		{"/v1/threads/thread_abc/responses", ""},
		{"/v1/chat/completions/chatcmpl-1/messages", ""},
		{"/v1/chat/completions/chatcmpl-1", ""},
		{"/v1/responses/resp_123", ""},
		{"/v1/responses/resp_123/input_items", ""},
		{"/v1/responses/resp_123/cancel", ""},
		{"/v1/responses/compact", ""},
		{"/v1/embeddings", ""},
		{"/v1/models", ""},
		{"/v1/completions", ""},
	}
	for _, tt := range tests {
		if got := Detect(tt.path); got != tt.want {
			t.Errorf("Detect(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

// TestMalformed checks that a body that does not parse yields an error and no partial result.
func TestMalformed(t *testing.T) {
	tests := []struct {
		name     string
		protocol llm.Protocol
		request  bool
		body     string
	}{
		{"anthropic truncated request", llm.AnthropicMessages, true, `{"model":"claude","messages":[{"role":"user","content":"hi"}`},
		{"anthropic request array", llm.AnthropicMessages, true, `[{"model":"claude"}]`},
		{"anthropic content wrong type", llm.AnthropicMessages, true, `{"messages":[{"role":"user","content":42}]}`},
		{"anthropic bad block", llm.AnthropicMessages, true, `{"messages":[{"role":"user","content":[{"type":"text","text":7}]}]}`},
		{"anthropic bad response", llm.AnthropicMessages, false, `{"id":"msg_1","content":[{"type":"text","text":"hi"}`},
		{"anthropic bad stream event", llm.AnthropicMessages, false, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\"}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\n\n"},
		{"anthropic delta before start", llm.AnthropicMessages, false, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"x\"}}\n\n"},
		{"anthropic null request", llm.AnthropicMessages, true, `null`},
		{"anthropic empty request", llm.AnthropicMessages, true, ``},
		{"anthropic trailing data", llm.AnthropicMessages, true, `{"model":"claude"} {"model":"other"}`},
		{"chat truncated request", llm.OpenAIChat, true, `{"model":"gpt-4o","messages":[`},
		{"chat null request", llm.OpenAIChat, true, `null`},
		{"responses null request", llm.OpenAIResponses, true, `null`},
		{"chat messages wrong type", llm.OpenAIChat, true, `{"model":"gpt-4o","messages":"hello"}`},
		{"chat bad response", llm.OpenAIChat, false, `{"id":"chatcmpl-1","choices":[{"message":{"content":"x"}`},
		{"chat bad chunk", llm.OpenAIChat, false, "data: {\"id\":\"c\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"a\"}}]}\n\ndata: {\"id\":\n\ndata: [DONE]\n\n"},
		{"responses truncated request", llm.OpenAIResponses, true, `{"model":"gpt-5","input":[{"type":"message"`},
		{"responses input wrong type", llm.OpenAIResponses, true, `{"model":"gpt-5","input":42}`},
		{"responses bad item content", llm.OpenAIResponses, true, `{"input":[{"type":"message","role":"user","content":true}]}`},
		{"responses bad response", llm.OpenAIResponses, false, `{"id":"resp_1","output":[`},
		{"responses bad event", llm.OpenAIResponses, false, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\nevent: response.output_item.done\ndata: {not json}\n\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.request {
				req, err := DecodeRequest(tt.protocol, []byte(tt.body))
				if err == nil || req != nil {
					t.Fatalf("DecodeRequest = %+v, %v; want nil, error", req, err)
				}
				return
			}
			checkStream(t, tt.protocol, []byte(tt.body))
			resp, err := DecodeResponse(tt.protocol, []byte(tt.body))
			if err == nil || resp != nil {
				t.Fatalf("DecodeResponse = %+v, %v; want nil, error", resp, err)
			}
		})
	}
}

func TestUnknownProtocol(t *testing.T) {
	if _, err := DecodeRequest("gemini.generate", []byte(`{}`)); err == nil {
		t.Fatal("DecodeRequest of an unknown protocol succeeded")
	}
}

// describeMessages renders messages one line per part, with the channel each part classifies
// into, so tests state the expected decoding readably and check classification at once.
func describeMessages(msgs []llm.Message) []string {
	var out []string
	for _, m := range msgs {
		if len(m.Parts) == 0 {
			out = append(out, fmt.Sprintf("%s (empty)", m.Role))
		}
		for _, p := range m.Parts {
			out = append(out, fmt.Sprintf("%s %s %s", m.Role, p.Channel(m.Role), describePart(p)))
		}
	}
	return out
}

// describeResponse renders a response's parts as describeMessages would an assistant message.
func describeResponse(resp *llm.Response) []string {
	return describeMessages([]llm.Message{{Role: llm.RoleAssistant, Parts: resp.Parts}})
}

func describePart(p llm.Part) string {
	switch p.Kind {
	case llm.PartText:
		return fmt.Sprintf("text %q", p.Text)
	case llm.PartReasoning:
		return fmt.Sprintf("reasoning %q", p.Text)
	case llm.PartMedia:
		return fmt.Sprintf("media %s mime=%s url=%s data=%s", p.Media.Type, p.Media.MIME, p.Media.URL, p.Media.Data)
	case llm.PartToolCall:
		return fmt.Sprintf("call id=%s name=%s args=%s", p.Call.ID, p.Call.Name, p.Call.Arguments)
	case llm.PartToolResult:
		inner := make([]string, len(p.Result.Parts))
		for i, ip := range p.Result.Parts {
			inner[i] = describePart(ip)
		}
		return fmt.Sprintf("result call=%s error=%t [%s]", p.Result.CallID, p.Result.IsError, strings.Join(inner, "; "))
	}
	return fmt.Sprintf("kind(%d)", p.Kind)
}

func describeTools(tools []llm.Tool) []string {
	out := make([]string, len(tools))
	for i, tl := range tools {
		out[i] = fmt.Sprintf("%s kind=%s desc=%q params=%s", tl.Name, tl.Kind, tl.Description, tl.Parameters)
	}
	return out
}

// equalLines reports, line by line, where got differs from want.
func equalLines(t *testing.T, what string, got, want []string) {
	t.Helper()
	n := max(len(got), len(want))
	for i := range n {
		var g, w string
		if i < len(got) {
			g = got[i]
		}
		if i < len(want) {
			w = want[i]
		}
		if g != w {
			t.Errorf("%s[%d]:\n got: %s\nwant: %s", what, i, g, w)
		}
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
