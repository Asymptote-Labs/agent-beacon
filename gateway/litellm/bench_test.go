package litellm

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/asymptote-labs/agent-beacon/gateway/events"
	"github.com/asymptote-labs/agent-beacon/gateway/internal/synth"
	"github.com/asymptote-labs/agent-beacon/gateway/llm"
	"github.com/asymptote-labs/agent-beacon/gateway/llm/codec"
	"github.com/asymptote-labs/agent-beacon/gateway/pipeline"
)

// benchPayload is a request-phase guardrail payload at turn k of a long session, laid out as
// LiteLLM sends it: the messages, the tools, then texts, a copy of every message's text.
func benchPayload(k int) []byte {
	s := synth.NewScript(1, 0, k, 3000)
	pc := [][]byte{[]byte(`{"input_type":"request","litellm_call_id":"c1","litellm_trace_id":"t1","model":"` + synth.Model + `","structured_messages":[`)}
	pc = s.ChatMessages(pc, k, synth.Benign)
	pc = append(pc, []byte(`],"tools":`), s.ChatTools(), []byte(`,"texts":[`))
	pc = s.Texts(pc, k, synth.Benign)
	pc = append(pc, []byte(`],"request_headers":{"x-claude-code-session-id":"s1","user-agent":"claude-cli/2.1.0"},"request_data":{"user_api_key_user_id":"u"}}`))
	return synth.AppendJoin(nil, pc)
}

type inlineAnalyzer struct{}

func (inlineAnalyzer) Name() string { return "inline" }
func (inlineAnalyzer) Inline() bool { return true }
func (inlineAnalyzer) Analyze(context.Context, *pipeline.Turn) (pipeline.Report, error) {
	return pipeline.Report{}, nil
}

// BenchmarkRequestPhase is a request-phase call's work in this process, by stage, in the steady
// state of a long session (the codec's memo warm with its history): walking the payload, building
// the Chat body over it, skimming that body, and the whole call through a pipeline that analyzes
// before it answers.
func BenchmarkRequestPhase(b *testing.B) {
	for _, k := range []int{100, 300} {
		payload := benchPayload(k)
		name := fmt.Sprintf("turns=%d/%dKB", k, len(payload)>>10)
		buf := make([]byte, len(payload))
		b.Run(name+"/walk", func(b *testing.B) {
			b.SetBytes(int64(len(payload)))
			for b.Loop() {
				var req request
				if err := decodeRequest(payload, &req); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(name+"/build", func(b *testing.B) {
			b.SetBytes(int64(len(payload)))
			for b.Loop() {
				copy(buf, payload)
				var req request
				_ = decodeRequest(buf, &req)
				if _, _, err := requestBody(req, buf); err != nil {
					b.Fatal(err)
				}
			}
		})
		copy(buf, payload)
		var req request
		_ = decodeRequest(buf, &req)
		body, split, _ := requestBody(req, buf)
		if _, err := codec.SkimSplitRequest(llm.OpenAIChat, body, split); err != nil {
			b.Fatal(err)
		}
		b.Run(name+"/skim", func(b *testing.B) {
			b.SetBytes(int64(len(payload)))
			for b.Loop() {
				if _, err := codec.SkimSplitRequest(llm.OpenAIChat, body, split); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(name+"/call", func(b *testing.B) {
			ev, err := events.New(events.Config{})
			if err != nil {
				b.Fatal(err)
			}
			p := pipeline.New(pipeline.Config{Analyzers: []pipeline.Analyzer{ev, inlineAnalyzer{}}})
			defer p.Close()
			a, _ := New(Config{}, nil)
			ad := a.(*adapter)
			b.SetBytes(int64(len(payload)))
			for b.Loop() {
				rec := httptest.NewRecorder()
				ad.guardrail(rec, httptest.NewRequest(http.MethodPost, Path, bytes.NewReader(payload)), p)
				if rec.Code != http.StatusOK {
					b.Fatal(rec.Body.String())
				}
			}
		})
	}
}
