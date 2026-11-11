// Package codec decodes provider wire formats into the canonical llm form. Adapters that see raw
// bytes (Envoy ext_proc, the native callout API) leave decoding to the pipeline, which uses this
// package; adapters whose gateway already normalized the call use it for the format the gateway
// normalized to.
package codec

import (
	"fmt"
	"strings"

	"github.com/asymptote-labs/agent-beacon/gateway/llm"
)

// Codec decodes one protocol.
type Codec interface {
	Protocol() llm.Protocol
	// DecodeRequest decodes a request body.
	DecodeRequest(body []byte) (*llm.Request, error)
	// DecodeResponse decodes a reply body: a JSON response, or a complete SSE stream (told apart
	// by content), assembled into one response.
	DecodeResponse(body []byte) (*llm.Response, error)
	// ErrorBody renders an error reply in the protocol's own error shape, for an adapter that
	// answers the client in the provider's place.
	ErrorBody(status int, message string) []byte
}

var codecs = map[llm.Protocol]Codec{
	llm.AnthropicMessages: anthropic{},
	llm.OpenAIChat:        openAIChat{},
	llm.OpenAIResponses:   openAIResponses{},
}

// For returns the codec for a protocol.
func For(p llm.Protocol) (Codec, bool) {
	c, ok := codecs[p]
	return c, ok
}

// Detect names the protocol of a model request from its URL path: a provider's model-call route,
// under whatever prefix a gateway or provider mounts it ("/anthropic/v1/messages?beta=true",
// "/openai/v1/chat/completions"). A route is the endpoint (messages, chat/completions,
// responses) under its API version (v1), or under the few parents providers use instead: Azure
// OpenAI's deployments/<name> and openai, OpenAI-compatible APIs served at their root
// ("/chat/completions") or under openai (Gemini's "/v1beta/openai/chat/completions"), and the
// ChatGPT backend's codex ("/backend-api/codex/responses"). Any other path is "", including
// endpoints of the same name under another resource (Assistants' "/v1/threads/<id>/messages"),
// token counting, model listing, embeddings, and Responses' compaction and retrieval endpoints.
func Detect(path string) llm.Protocol {
	path, _, _ = strings.Cut(path, "?")
	path = strings.TrimSuffix(path, "/")
	for _, r := range routes {
		if parent, ok := strings.CutSuffix(path, r.endpoint); ok && r.under(parent) {
			return r.protocol
		}
	}
	return ""
}

// routes are the model-call endpoints and the parents they are served under.
var routes = []struct {
	endpoint string
	protocol llm.Protocol
	under    func(parent string) bool
}{
	{"/messages", llm.AnthropicMessages, func(p string) bool { return lastSegment(p) == "v1" }},
	{"/chat/completions", llm.OpenAIChat, func(p string) bool {
		switch lastSegment(p) {
		case "v1", "openai", "":
			return true
		}
		// Azure OpenAI: /openai/deployments/<deployment>/chat/completions.
		return lastSegment(p[:max(strings.LastIndexByte(p, '/'), 0)]) == "deployments"
	}},
	{"/responses", llm.OpenAIResponses, func(p string) bool {
		switch lastSegment(p) {
		case "v1", "openai", "codex":
			return true
		}
		return false
	}},
}

// lastSegment is the last segment of a path, "" for the empty path.
func lastSegment(p string) string { return p[strings.LastIndexByte(p, '/')+1:] }

// DecodeRequest decodes a request body of protocol p.
func DecodeRequest(p llm.Protocol, body []byte) (*llm.Request, error) {
	c, ok := For(p)
	if !ok {
		return nil, fmt.Errorf("codec: unknown protocol %q", p)
	}
	return c.DecodeRequest(body)
}

// DecodeResponse decodes a response body (JSON or SSE) of protocol p.
func DecodeResponse(p llm.Protocol, body []byte) (*llm.Response, error) {
	c, ok := For(p)
	if !ok {
		return nil, fmt.Errorf("codec: unknown protocol %q", p)
	}
	return c.DecodeResponse(body)
}
