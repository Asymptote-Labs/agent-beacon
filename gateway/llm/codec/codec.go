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

// Detect names the protocol of a model request from its URL path: any path that ends in a model
// call's endpoint (/messages, /chat/completions, /responses), whatever prefix a gateway, provider
// or proxy mounts it under ("/anthropic/v1/messages?beta=true", Azure AI Foundry's
// "/models/chat/completions", Vertex AI's ".../endpoints/openapi/chat/completions"), except the
// routes of other resources that end the same way (notDecoded). The query is ignored. Token
// counting, batches, model listing, embeddings, and Responses' retrieval, input items, cancel and
// compaction endpoints do not end in an endpoint and are "".
func Detect(path string) llm.Protocol {
	path, _, _ = strings.Cut(path, "?")
	path = strings.TrimSuffix(path, "/")
	for _, e := range endpoints {
		if parent, ok := strings.CutSuffix(path, e.suffix); ok && !notDecoded(parent) {
			return e.protocol
		}
	}
	return ""
}

var endpoints = []struct {
	suffix   string
	protocol llm.Protocol
}{
	{"/messages", llm.AnthropicMessages},
	{"/chat/completions", llm.OpenAIChat},
	{"/responses", llm.OpenAIResponses},
}

// notDecoded reports that an endpoint under parent belongs to another resource: OpenAI Assistants'
// thread messages and runs ("/v1/threads/<id>/messages", also under Azure's "/openai/threads/"),
// and a stored chat completion's messages ("/v1/chat/completions/<id>/messages").
func notDecoded(parent string) bool {
	segs := strings.Split(strings.Trim(parent, "/"), "/")
	for i, s := range segs {
		switch {
		case s == "threads":
			return true
		case s == "completions" && i > 0 && segs[i-1] == "chat" && i < len(segs)-1:
			return true
		}
	}
	return false
}

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
