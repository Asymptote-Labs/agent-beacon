package pipeline_test

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/gateway"
	"github.com/asymptote-labs/agent-beacon/gateway/events"
	"github.com/asymptote-labs/agent-beacon/gateway/llm"
	"github.com/asymptote-labs/agent-beacon/gateway/pipeline"
)

const (
	releaseRequest = `{"model":"claude-x","system":"You are terse.","tools":[{"name":"mcp__notes__get","description":"read a note","input_schema":{"type":"object"}}],` +
		`"messages":[{"role":"user","content":"look at the build"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"make test"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":"ok 12 tests"},{"type":"text","text":"now read the note"}]}]}`
	releaseResponse = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-x-2026","content":[{"type":"text","text":"Reading it."},` +
		`{"type":"tool_use","id":"tu2","name":"mcp__notes__get","input":{"id":"n7"}}],"stop_reason":"tool_use","usage":{"input_tokens":120,"output_tokens":9}}`
)

type collect struct {
	mu  sync.Mutex
	got [][]byte
}

// Write marshals each record now, as a sink does, which is after the call released its body.
func (s *collect) Write(_ context.Context, b []pipeline.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range b {
		line, err := json.Marshal(r.Value)
		if err != nil {
			return err
		}
		s.got = append(s.got, line)
	}
	return nil
}

func (*collect) Close() error { return nil }

type inline struct{}

func (inline) Name() string { return "inline" }
func (inline) Inline() bool { return true }
func (inline) Analyze(context.Context, *pipeline.Turn) (pipeline.Report, error) {
	return pipeline.Report{}, nil
}

// An adapter reuses a body's buffer once the pipeline releases it, so nothing the pipeline
// records may refer to the body: overwriting every buffer at release leaves each record as it
// was. Release comes exactly once per call, whether the pipeline answers first or after analysis.
func TestReleasedBodiesAreNotRead(t *testing.T) {
	at := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	for _, mode := range []string{"inline", "answer-first"} {
		run := func(poison bool) ([][]byte, int64) {
			ev, err := events.New(events.Config{})
			if err != nil {
				t.Fatal(err)
			}
			analyzers := []pipeline.Analyzer{ev}
			if mode == "inline" {
				analyzers = append(analyzers, inline{})
			}
			sink := &collect{}
			p := pipeline.New(pipeline.Config{Analyzers: analyzers, Routes: []pipeline.Route{{Name: "all", Sink: sink}}})
			var releases atomic.Int64
			// Each call is handed over once the one before is released, so that, answered first or
			// not, calls are analyzed in order and record the same sequence numbers in both runs.
			done := make(chan struct{}, 1)
			for _, phase := range []gateway.Phase{gateway.PhaseRequest, gateway.PhaseResponse} {
				c := &gateway.Call{
					Gateway: "g", ID: "c1", Phase: phase, Enforce: true, Time: at, Protocol: llm.AnthropicMessages,
					Client: gateway.Client{Headers: map[string]string{"x-claude-code-session-id": "s1"}},
					Body:   []byte(releaseRequest),
				}
				if phase == gateway.PhaseResponse {
					c.Body, c.RequestBody = []byte(releaseResponse), []byte(releaseRequest)
				}
				c.Release = func() {
					releases.Add(1)
					if poison {
						for _, b := range [][]byte{c.Body, c.RequestBody} {
							copy(b, bytes.Repeat([]byte{0xFF}, len(b)))
						}
					}
					done <- struct{}{}
				}
				p.Handle(context.Background(), c)
				<-done
			}
			p.Close()
			return sink.got, releases.Load()
		}
		clean, _ := run(false)
		poisoned, releases := run(true)
		if releases != 2 {
			t.Errorf("%s: %d releases for 2 calls", mode, releases)
		}
		if len(clean) == 0 {
			t.Fatalf("%s: nothing recorded", mode)
		}
		if len(poisoned) != len(clean) {
			t.Fatalf("%s: %d records after poisoning, %d without", mode, len(poisoned), len(clean))
		}
		for i := range clean {
			if !bytes.Equal(poisoned[i], clean[i]) {
				t.Errorf("%s: record %d refers to a released body:\n got %s\nwant %s", mode, i, poisoned[i], clean[i])
			}
		}
	}
}

// A call refused for want of an analysis slot is released too.
func TestRefusedCallIsReleased(t *testing.T) {
	block := make(chan struct{})
	p := pipeline.New(pipeline.Config{Analyzers: []pipeline.Analyzer{gateAnalyzer{block}}, MaxInFlight: 1, QueueWait: 10 * time.Millisecond})
	defer p.Close()
	defer close(block)
	var releases atomic.Int64
	call := func() *gateway.Call {
		return &gateway.Call{Gateway: "g", ID: "c", Phase: gateway.PhaseRequest, Protocol: llm.AnthropicMessages,
			Body: []byte(releaseRequest), Release: func() { releases.Add(1) }}
	}
	p.Handle(context.Background(), call()) // answered first; holds the only slot until block closes
	p.Handle(context.Background(), call()) // refused
	if got := releases.Load(); got != 1 {
		t.Errorf("%d releases while one call is still analyzed and one was refused, want 1", got)
	}
}

type gateAnalyzer struct{ block chan struct{} }

func (gateAnalyzer) Name() string { return "gate" }
func (g gateAnalyzer) Analyze(context.Context, *pipeline.Turn) (pipeline.Report, error) {
	<-g.block
	return pipeline.Report{}, nil
}
