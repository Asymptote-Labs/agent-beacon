package codec

import (
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/asymptote-labs/agent-beacon/gateway/llm"
)

// ReadRoot's first message is the full decode's first non-system message, read without reading
// the history after it: a body cut off after that message still yields it. The system text sent
// ahead of it is kept as written; system text after the messages is not read.
func TestReadRoot(t *testing.T) {
	for _, tc := range []struct {
		name   string
		p      llm.Protocol
		body   string
		first  string // "" for no root
		system []string
	}{
		{"anthropic, system after the messages", llm.AnthropicMessages,
			`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"fix it"},{"type":"text","text":"now"}]},{"role":"assistant","content":"ok"}],"system":"sys"}`,
			"fix it\nnow", nil},
		{"anthropic, system ahead", llm.AnthropicMessages,
			`{"model":"m","system":[{"type":"text","text":"sys"}],"tools":[],"messages":[{"role":"user","content":"fix it"}]}`,
			"fix it", []string{`[{"type":"text","text":"sys"}]`}},
		{"chat skips system messages", llm.OpenAIChat,
			`{"model":"m","messages":[{"role":"system","content":"sys"},{"role":"developer","content":"dev"},{"role":"user","content":"fix it"},{"role":"assistant","content":"ok"}]}`,
			"fix it", []string{`{"role":"system","content":"sys"}`, `{"role":"developer","content":"dev"}`}},
		{"responses input string", llm.OpenAIResponses, `{"model":"m","instructions":"sys","input":"fix it"}`, "fix it", []string{`"sys"`}},
		{"responses instructions after the input", llm.OpenAIResponses, `{"model":"m","input":"fix it","instructions":"sys"}`, "fix it", nil},
		{"responses input items", llm.OpenAIResponses,
			`{"model":"m","input":[{"role":"developer","content":"dev"},{"type":"message","role":"user","content":[{"type":"input_text","text":"fix it"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`,
			"fix it", []string{`{"role":"developer","content":"dev"}`}},
		{"responses naming the state members below the top level", llm.OpenAIResponses,
			`{"model":"m","instructions":"sys","metadata":{"conversation":"c","previous_response_id":"r"},"input":[{"role":"user","content":"fix it"}],"store":false}`,
			"fix it", []string{`"sys"`}},
		{"no messages", llm.OpenAIChat, `{"model":"m","messages":[{"role":"system","content":"sys"}]}`, "", nil},
		{"no messages member", llm.AnthropicMessages, `{"model":"m"}`, "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := ReadRoot(tc.p, []byte(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			if tc.first == "" {
				if r.First != nil {
					t.Fatalf("First = %+v, want none", r.First)
				}
				return
			}
			var system []string
			for _, s := range r.System {
				system = append(system, string(s))
			}
			if !slices.Equal(system, tc.system) {
				t.Errorf("System = %q, want %q", system, tc.system)
			}
			if r.First == nil || r.First.Text() != tc.first {
				t.Fatalf("First = %+v, want %q", r.First, tc.first)
			}
			req, err := DecodeRequest(tc.p, []byte(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			var want *llm.Message
			for i := range req.Messages {
				if req.Messages[i].Role != llm.RoleSystem {
					want = &req.Messages[i]
					break
				}
			}
			if !reflect.DeepEqual(r.First, want) {
				t.Errorf("First = %+v, the decode's first message %+v", r.First, want)
			}
		})
	}
	cut := `{"model":"m","messages":[{"role":"user","content":"fix it"},{"role":"assistant","content":` // cut mid-history
	if r, err := ReadRoot(llm.OpenAIChat, []byte(cut)); err != nil || r.First == nil || r.First.Text() != "fix it" {
		t.Errorf("cut body: First %+v, %v", r.First, err)
	}
}

// Reading a Chat or Anthropic root costs the same however long the history after it; a Responses
// body that names no provider-side state costs one IndexByte pass more.
func BenchmarkReadRoot(b *testing.B) {
	for _, turns := range []int{10, 300} {
		for _, p := range []llm.Protocol{llm.OpenAIChat, llm.OpenAIResponses} {
			body := rootBody(p, turns)
			b.Run(fmt.Sprintf("%s/turn%d", p, turns), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if r, err := ReadRoot(p, body); err != nil || r.First == nil {
						b.Fatal(r, err)
					}
				}
			})
		}
	}
}

func rootBody(p llm.Protocol, turns int) []byte {
	if p == llm.OpenAIResponses {
		b := []byte(`{"model":"m","instructions":"You are a coding agent.","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the tests"}]}`)
		for range turns {
			b = append(b, `,{"type":"message","role":"assistant","content":[{"type":"output_text","text":"I ran the tests; two fail in the parser. Fixing the first now."}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"go on"}]}`...)
		}
		return append(b, `],"store":false}`...)
	}
	b := []byte(`{"model":"m","messages":[{"role":"system","content":"You are a coding agent."},{"role":"user","content":"fix the tests"}`)
	for range turns {
		b = append(b, `,{"role":"assistant","content":"I ran the tests; two fail in the parser. Fixing the first now."},{"role":"user","content":"go on"}`...)
	}
	return append(b, `]}`...)
}
