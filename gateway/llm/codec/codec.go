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

// Detect names the protocol of a model request from its URL path, whatever prefix a gateway or
// provider puts in front ("/v1/messages", "/openai/v1/chat/completions",
// "/openai/deployments/gpt-4o/chat/completions"). It returns "" for paths that are not a model
// call this package decodes: token counting, model listing, embeddings, Responses' compaction
// and retrieval endpoints.
func Detect(path string) llm.Protocol {
	path, _, _ = strings.Cut(path, "?")
	path = strings.TrimSuffix(path, "/")
	switch {
	case strings.HasSuffix(path, "/messages"):
		return llm.AnthropicMessages
	case strings.HasSuffix(path, "/chat/completions"):
		return llm.OpenAIChat
	case strings.HasSuffix(path, "/responses"):
		return llm.OpenAIResponses
	}
	return ""
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
