package codec

import (
	"bytes"
	"encoding/json/jsontext"
	"fmt"

	"github.com/asymptote-labs/agent-beacon/gateway/internal/jsonscan"
	"github.com/asymptote-labs/agent-beacon/gateway/llm"
)

// Root is what a request body says about the conversation it belongs to, read without decoding
// its history: the provider-side state a stateful request continues, or else the conversation's
// first message, which clients resend unchanged with every call.
type Root struct {
	// Conversation and PreviousResponse are as in llm.Request.
	Conversation     string
	PreviousResponse string
	// First is the conversation's first non-system message, decoded as the full decode decodes
	// it; nil when the request has none, or is stateful, so that it carries only its new turn.
	First *llm.Message
}

// Stateful is llm.Request's Stateful.
func (r Root) Stateful() bool { return r.Conversation != "" || r.PreviousResponse != "" }

// ReadRoot reads a request body's Root. It reads the message array only as far as its first
// non-system message, so its cost does not grow with the conversation. A Responses body may name
// the state it continues after its input, so one that mentions either member is walked to its
// end, its input skipped unvalidated; one that mentions neither (as written, unescaped) is read
// only as far as its input's first message. Member names match exactly, as the decoders match
// them; of duplicate members it reads the first.
func ReadRoot(p llm.Protocol, body []byte) (Root, error) {
	var r Root
	switch p {
	case llm.AnthropicMessages:
		return r, firstOf(body, "messages", anthropic{}, &r)
	case llm.OpenAIChat:
		return r, firstOf(body, "messages", openAIChat{}, &r)
	case llm.OpenAIResponses:
		if !mentionsState(body) {
			at, ok := jsonscan.MemberStart(body, func(n string) bool { return n == "input" })
			if !ok {
				return r, nil
			}
			return r, responsesFirst(body, at, &r)
		}
		var input []byte
		var seen [3]bool // input, previous_response_id, conversation
		err := jsonscan.Object(body, func(name string, value []byte) error {
			var err error
			switch name {
			case "input":
				if !seen[0] {
					seen[0], input = true, value
				}
			case "previous_response_id":
				if !seen[1] {
					seen[1] = true
					r.PreviousResponse, err = valueString(value)
				}
			case "conversation":
				if !seen[2] {
					seen[2] = true
					r.Conversation, err = valueConversation(value)
				}
			}
			return err
		})
		if err != nil || r.Stateful() || input == nil {
			return r, err
		}
		return r, responsesFirst(input, 0, &r)
	}
	return r, fmt.Errorf("codec: unknown protocol %q", p)
}

// responsesFirst reads the first non-system message of the Responses input at b[at]: a string is
// one user message, an array a list of items.
func responsesFirst(b []byte, at int, r *Root) error {
	if b[at] != '"' {
		return firstElement(b, at, openAIResponses{}, r)
	}
	end, err := jsonscan.ValueEnd(b, at)
	if err != nil {
		return err
	}
	s, err := valueString(b[at:end])
	if err != nil {
		return err
	}
	r.First = &llm.Message{Role: llm.RoleUser, Parts: appendText(nil, llm.PartText, s)}
	return nil
}

// mentionsState reports whether body contains `"previous_response_id"` or `"conversation"`, in
// one pass: both names have a 'v' four bytes past their opening quote, a byte rare enough in model
// traffic that IndexByte skips most of a body between candidates.
func mentionsState(body []byte) bool {
	for i := 4; i < len(body); i++ {
		j := bytes.IndexByte(body[i:], 'v')
		if j < 0 {
			return false
		}
		i += j
		if s := body[i-4:]; bytes.HasPrefix(s, previousName) || bytes.HasPrefix(s, conversationName) {
			return true
		}
	}
	return false
}

var previousName, conversationName = []byte(`"previous_response_id"`), []byte(`"conversation"`)

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

// valueConversation decodes Responses' conversation parameter as readConversation does.
func valueConversation(v []byte) (string, error) {
	if len(v) == 0 || v[0] != '{' {
		return valueString(v)
	}
	var c struct {
		ID string `json:"id"`
	}
	err := unmarshal(v, &c)
	return c.ID, err
}
