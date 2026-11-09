// Package perf benchmarks the request pipeline end to end, in process: a gateway.Call with a
// realistic body through decode, delta, the events analyzer, policy and outputs, all on the call
// path, as a build whose verdict waits for analysis runs them. It holds no code, only benchmarks,
// so profiles of the call path come from one place:
//
//	go test ./internal/perf -run '^$' -bench . -benchmem -cpuprofile cpu.out -memprofile mem.out
package perf

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/gateway"
	"github.com/asymptote-labs/agent-beacon/gateway/events"
	"github.com/asymptote-labs/agent-beacon/gateway/internal/synth"
	"github.com/asymptote-labs/agent-beacon/gateway/llm"
	"github.com/asymptote-labs/agent-beacon/gateway/pipeline"
)

// discard is a sink that drops records, so outputs cost what routing costs.
type discard struct{}

func (discard) Write(context.Context, []pipeline.Record) error { return nil }
func (discard) Close() error                                   { return nil }

// onPath keeps analysis on the call path, as an inline analyzer does.
type onPath struct{}

func (onPath) Name() string { return "on-path" }
func (onPath) Inline() bool { return true }
func (onPath) Analyze(context.Context, *pipeline.Turn) (pipeline.Report, error) {
	return pipeline.Report{}, nil
}

func newPipeline(b testing.TB) *pipeline.Pipeline {
	b.Helper()
	ev, err := events.New(events.Config{})
	if err != nil {
		b.Fatal(err)
	}
	p := pipeline.New(pipeline.Config{
		Analyzers:   []pipeline.Analyzer{ev, onPath{}},
		Routes:      []pipeline.Route{{Name: "all", Sink: discard{}}},
		MaxInFlight: 1 << 16,
	})
	b.Cleanup(p.Close)
	return p
}

// session renders the request body a Claude Code session sends at turn `turns` (the whole history,
// Read/Bash/MCP tool calls and their ~3 KB results) and the model's reply to it.
func session(turns int) (request, response []byte) {
	r := rand.New(rand.NewPCG(1, 2))
	prefix := synth.AnthropicPrefix("user_perf_0001")
	msgs := synth.UserMessage(synth.FirstPrompt)
	var last synth.Tool
	for k := 1; k <= turns; k++ {
		last = synth.RandomTool(r, synth.ToolID(r))
		if k == turns {
			break
		}
		msgs = append(msgs, ',')
		msgs = append(msgs, synth.AssistantMessage(synth.Narration(last), last)...)
		msgs = append(msgs, ',')
		msgs = append(msgs, synth.ResultMessage(last, synth.ToolResult(r, last, 3000))...)
	}
	request = append(append(append([]byte{}, prefix...), msgs...), "]}"...)
	response = synth.AnthropicResponse("msg_perf", synth.Narration(last), last, len(request)/4)
	return request, response
}

func call(id string, phase gateway.Phase, body []byte, sessionID string) *gateway.Call {
	return &gateway.Call{
		Gateway: "perf", ID: id, Phase: phase, Enforce: true, Time: time.Now(), Model: synth.Model,
		Protocol: llm.AnthropicMessages, Body: body,
		Client: gateway.Client{UserAgent: synth.UserAgent, Headers: map[string]string{"x-claude-code-session-id": sessionID}},
	}
}

// BenchmarkRequest is one request phase of a session at a given length, in a session that has
// already reported its system prompt and tools: the steady state of a long-running agent.
func BenchmarkRequest(b *testing.B) {
	for _, turns := range []int{10, 100, 300} {
		req, _ := session(turns)
		b.Run(fmt.Sprintf("turns=%d/%dKB", turns, len(req)>>10), func(b *testing.B) {
			p := newPipeline(b)
			ctx := context.Background()
			p.Handle(ctx, call("warm", gateway.PhaseRequest, req, "s"))
			b.SetBytes(int64(len(req)))
			b.ReportAllocs()
			for b.Loop() {
				p.Handle(ctx, call("c", gateway.PhaseRequest, req, "s"))
			}
		})
	}
}

// BenchmarkResponse is one response phase: the model's reply with a tool call, as JSON and as SSE.
func BenchmarkResponse(b *testing.B) {
	_, resp := session(10)
	s := synth.NewScript(1, 0, 20, 3000)
	for _, tc := range []struct {
		name string
		body []byte
	}{{"json", resp}, {"sse", s.SSE(5, synth.Benign, false)}, {"sse-long", s.SSE(5, synth.Benign, true)}} {
		b.Run(fmt.Sprintf("%s/%dKB", tc.name, len(tc.body)>>10), func(b *testing.B) {
			p := newPipeline(b)
			ctx := context.Background()
			b.SetBytes(int64(len(tc.body)))
			b.ReportAllocs()
			for b.Loop() {
				p.Handle(ctx, call("c", gateway.PhaseResponse, tc.body, "s"))
			}
		})
	}
}

// BenchmarkParallel is a request and its response per op across all CPUs and many sessions, at
// a mid-session length: the throughput figure.
func BenchmarkParallel(b *testing.B) {
	req, resp := session(60)
	p := newPipeline(b)
	var n atomic.Uint64
	b.SetBytes(int64(len(req) + len(resp)))
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		ctx := context.Background()
		for pb.Next() {
			i := n.Add(1)
			sid := fmt.Sprint("s", i%2048)
			p.Handle(ctx, call("c", gateway.PhaseRequest, req, sid))
			p.Handle(ctx, call("c", gateway.PhaseResponse, resp, sid))
		}
	})
}
