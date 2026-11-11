package codec

import (
	"encoding/json/jsontext"
	"fmt"

	"github.com/asymptote-labs/agent-beacon/gateway/internal/jsonscan"
	"github.com/asymptote-labs/agent-beacon/gateway/llm"
)

// Root is what a request body says about the conversation it belongs to, read without decoding
// its history: the conversation's first message, which clients resend unchanged with every call.
type Root struct {
	// First is the conversation's first non-system message, decoded as the full decode decodes
	// it; nil when the request has none.
	First *llm.Message
}

// ReadRoot reads a request body's Root. It reads the message array only as far as its first
// non-system message, so its cost does not grow with the conversation. Member names match
// exactly, as the decoders match them; of duplicate members it reads the first.
func ReadRoot(p llm.Protocol, body []byte) (Root, error) {
	var r Root
	switch p {
	case llm.AnthropicMessages:
		return r, firstOf(body, "messages", anthropic{}, &r)
	case llm.OpenAIChat:
		return r, firstOf(body, "messages", openAIChat{}, &r)
	case llm.OpenAIResponses:
		at, ok := jsonscan.MemberStart(body, func(n string) bool { return n == "input" })
		if !ok {
			return r, nil
		}
		if body[at] == '"' {
			end, err := jsonscan.ValueEnd(body, at)
			if err != nil {
				return r, err
			}
			s, err := valueString(body[at:end])
			if err != nil {
				return r, err
			}
			r.First = &llm.Message{Role: llm.RoleUser, Parts: appendText(nil, llm.PartText, s)}
			return r, nil
		}
		return r, firstElement(body, at, openAIResponses{}, &r)
	}
	return r, fmt.Errorf("codec: unknown protocol %q", p)
}

// firstOf reads the first non-system message of the array that is member name of body.
func firstOf(body []byte, name string, c skimCodec, r *Root) error {
	at, ok := jsonscan.MemberStart(body, func(n string) bool { return n == name })
	if !ok {
		return nil
	}
	return firstElement(body, at, c, r)
}

// firstElement decodes the elements of the array at b[at] up to the first that holds a
// non-system message, which it sets as r.First.
func firstElement(b []byte, at int, c skimCodec, r *Root) error {
	if b[at] != '[' {
		return nil
	}
	var derr error
	err := jsonscan.Elements(b, at, func(elem []byte) bool {
		msgs, _, _, err := c.decode(elem, nil)
		if err != nil {
			derr = err
			return false
		}
		for i := range msgs {
			if msgs[i].Role != llm.RoleSystem {
				r.First = &msgs[i]
				return false
			}
		}
		return true
	})
	if derr != nil {
		return derr
	}
	return err
}

// valueString decodes a JSON string value; null is "".
func valueString(v []byte) (string, error) {
	if string(v) == "null" {
		return "", nil
	}
	if len(v) == 0 || v[0] != '"' {
		return "", fmt.Errorf("codec: want a string, got %.20s", v)
	}
	s, err := jsontext.AppendUnquote(nil, v)
	return string(s), err
}
