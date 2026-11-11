package codec

import (
	"reflect"
	"testing"

	"github.com/asymptote-labs/agent-beacon/gateway/llm"
)

// ReadRoot's first message is the full decode's first non-system message, read without reading
// the history after it: a body cut off after that message still yields it.
func TestReadRoot(t *testing.T) {
	for _, tc := range []struct {
		name  string
		p     llm.Protocol
		body  string
		first string // "" for no root
	}{
		{"anthropic", llm.AnthropicMessages,
			`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"fix it"},{"type":"text","text":"now"}]},{"role":"assistant","content":"ok"}],"system":"sys"}`,
			"fix it\nnow"},
		{"chat skips system messages", llm.OpenAIChat,
			`{"model":"m","messages":[{"role":"system","content":"sys"},{"role":"developer","content":"dev"},{"role":"user","content":"fix it"},{"role":"assistant","content":"ok"}]}`,
			"fix it"},
		{"responses input string", llm.OpenAIResponses, `{"model":"m","instructions":"sys","input":"fix it"}`, "fix it"},
		{"responses input items", llm.OpenAIResponses,
			`{"model":"m","input":[{"role":"developer","content":"dev"},{"type":"message","role":"user","content":[{"type":"input_text","text":"fix it"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`,
			"fix it"},
		{"no messages", llm.OpenAIChat, `{"model":"m","messages":[{"role":"system","content":"sys"}]}`, ""},
		{"no messages member", llm.AnthropicMessages, `{"model":"m"}`, ""},
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

// Reading the root costs the same however long the history after it.
func BenchmarkReadRoot(b *testing.B) {
	for _, turns := range []int{10, 300} {
		body := chatBody(turns)
		b.Run(map[int]string{10: "turn10", 300: "turn300"}[turns], func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if r, err := ReadRoot(llm.OpenAIChat, body); err != nil || r.First == nil {
					b.Fatal(r, err)
				}
			}
		})
	}
}

func chatBody(turns int) []byte {
	b := []byte(`{"model":"m","messages":[{"role":"system","content":"You are a coding agent."},{"role":"user","content":"fix the tests"}`)
	for range turns {
		b = append(b, `,{"role":"assistant","content":"I ran the tests; two fail in the parser. Fixing the first now."},{"role":"user","content":"go on"}`...)
	}
	return append(b, `]}`...)
}
