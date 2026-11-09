package pipeline

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/gateway"
	"github.com/asymptote-labs/agent-beacon/gateway/llm"
	"github.com/asymptote-labs/agent-beacon/gateway/store"
)

// recorder is an analyzer that keeps the turns it saw and reports scripted findings.
type recorder struct {
	turns    []Turn
	findings []Finding
	err      error
}

func (r *recorder) Name() string { return "recorder" }

func (r *recorder) Analyze(_ context.Context, t *Turn) (Report, error) {
	r.turns = append(r.turns, *t)
	return Report{Findings: r.findings, Records: []Record{{Kind: "test.event", Value: t.Call.ID}}}, r.err
}

// memSink collects records.
type memSink struct {
	mu     sync.Mutex
	got    []Record
	closed bool
}

func (s *memSink) Write(_ context.Context, b []Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, b...)
	return nil
}

func (s *memSink) Close() error { s.closed = true; return nil }

func text(role llm.Role, s string) llm.Message {
	return llm.Message{Role: role, Parts: []llm.Part{{Kind: llm.PartText, Text: s}}}
}

func call(id string, req *llm.Request) *gateway.Call {
	return &gateway.Call{Gateway: "test", ID: id, Phase: gateway.PhaseRequest, Enforce: true, Request: req,
		Client: gateway.Client{Headers: map[string]string{"x-claude-code-session-id": "s1"}}}
}

// A long session resends its system prompt, tool catalog and history on every call; analyzers
// must see only what each call adds, and a tool result must be tied to the call that asked for it.
// Two replicas sharing a store must agree on what is new, whichever of them serves a call.
func TestDeltaReportsOnlyNewContent(t *testing.T) {
	shared := store.NewMemory(1000)
	rec := &recorder{}
	first := New(Config{Analyzers: []Analyzer{rec}, Store: shared})
	defer first.Close()
	second := New(Config{Analyzers: []Analyzer{rec}, Store: shared})
	defer second.Close()
	sys := text(llm.RoleSystem, "You are Claude Code.")
	tools := []llm.Tool{{Name: "mcp__notes__get", Description: "d"}}
	user := text(llm.RoleUser, "summarize note 7")
	first.Handle(context.Background(), call("c1", &llm.Request{Messages: []llm.Message{sys, user}, Tools: tools}))

	asked := llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{{Kind: llm.PartToolCall, Call: &llm.ToolCall{ID: "t1", Name: "mcp__notes__get", Arguments: `{"id":"7"}`}}}}
	answer := llm.Message{Role: llm.RoleUser, Parts: []llm.Part{{Kind: llm.PartToolResult, Result: &llm.ToolResult{CallID: "t1"}}}}
	second.Handle(context.Background(), call("c2", &llm.Request{Messages: []llm.Message{sys, user, asked, answer}, Tools: tools}))

	d1, d2 := rec.turns[0].New, rec.turns[1].New
	if len(d1.System) != 1 || len(d1.Tools) != 1 || len(d1.Tail) != 1 {
		t.Fatalf("first call delta: %d system, %d tools, %d tail; want 1, 1, 1", len(d1.System), len(d1.Tools), len(d1.Tail))
	}
	if len(d2.System) != 0 || len(d2.Tools) != 0 {
		t.Errorf("resent system prompt or tools reported again by the other replica: %+v", d2)
	}
	if len(d2.Tail) != 1 || d2.Tail[0].Parts[0].Kind != llm.PartToolResult {
		t.Errorf("second call tail = %+v, want only the tool result", d2.Tail)
	}
	if tc, ok := rec.turns[1].ToolCall("t1"); !ok || tc.Name != "mcp__notes__get" {
		t.Errorf("tool result's call not found in its own request: %+v %v", tc, ok)
	}
}

// flakyStore fails Add while down.
type flakyStore struct {
	store.Store
	down bool
}

func (s *flakyStore) Add(ctx context.Context, es ...store.Entry) ([]bool, error) {
	if s.down {
		return nil, errors.New("store down")
	}
	return s.Store.Add(ctx, es...)
}

// A session resending the same system prompt and tools is answered from memory, but a tool that
// is added or changed is still reported as new, and a store failure (everything counts as new) is
// not remembered as having reported anything.
func TestSeenContentAcrossCalls(t *testing.T) {
	st := &flakyStore{Store: store.NewMemory(1000)}
	rec := &recorder{}
	p := New(Config{Analyzers: []Analyzer{rec}, Store: st})
	defer p.Close()
	sys := text(llm.RoleSystem, "You are Claude Code.")
	user := text(llm.RoleUser, "hi")
	tools := func(desc ...string) (out []llm.Tool) {
		for i, d := range desc {
			out = append(out, llm.Tool{Name: "mcp__s__t" + string(rune('a'+i)), Description: d})
		}
		return out
	}
	steps := []struct {
		name      string
		tools     []llm.Tool
		down      bool
		wantSys   int
		wantTools []string
	}{
		{"first call reports everything", tools("a", "b"), false, 1, []string{"a", "b"}},
		{"resent unchanged", tools("a", "b"), false, 0, nil},
		{"resent unchanged again", tools("a", "b"), false, 0, nil},
		{"a description changes", tools("a", "B"), false, 0, []string{"B"}},
		{"a tool is added", tools("a", "B", "c"), false, 0, []string{"c"}},
		{"store down: all new", tools("a", "B", "c", "d"), true, 1, []string{"a", "B", "c", "d"}},
		{"store back: only the unreported tool", tools("a", "B", "c", "d"), false, 0, []string{"d"}},
		{"then remembered", tools("a", "B", "c", "d"), false, 0, nil},
	}
	for i, s := range steps {
		st.down = s.down
		p.Handle(context.Background(), call("c", &llm.Request{Messages: []llm.Message{sys, user}, Tools: s.tools}))
		d := rec.turns[i].New
		var got []string
		for _, tl := range d.Tools {
			got = append(got, tl.Description)
		}
		if len(d.System) != s.wantSys || !slices.Equal(got, s.wantTools) {
			t.Errorf("%s: %d system, tools %v; want %d, %v", s.name, len(d.System), got, s.wantSys, s.wantTools)
		}
	}
}

func TestSessionID(t *testing.T) {
	p := New(Config{})
	defer p.Close()
	for _, tc := range []struct {
		name string
		c    gateway.Call
		want string
	}{
		{"session header wins over the gateway's id",
			gateway.Call{Gateway: "g", ID: "c", Client: gateway.Client{Session: "trace", Headers: map[string]string{"session_id": "codex-1"}}}, "codex-1"},
		{"gateway's conversation id", gateway.Call{Gateway: "g", ID: "c", Client: gateway.Client{Session: "trace"}}, "trace"},
		{"no session: the call is its own", gateway.Call{Gateway: "g", ID: "c"}, "g:c"},
	} {
		if got := p.sessionID(&tc.c); got != tc.want {
			t.Errorf("%s: sessionID = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Bodies are admitted against a byte budget: a body that does not fit is refused after the queue
// wait with an allow, released bytes are reusable, a body larger than the whole budget still runs
// alone, and waiting bodies are admitted in order, so a large one is not starved by small ones
// arriving behind it.
func TestBodyAdmission(t *testing.T) {
	ctx := context.Background()
	{
		p := New(Config{MaxBodyBytes: 100, QueueWait: 20 * time.Millisecond})
		release, _, ok := p.Admit(ctx, 60)
		if !ok {
			t.Fatal("idle pipeline refused a body")
		}
		_, refused, ok := p.Admit(ctx, 60)
		if ok || refused.Action != gateway.Allow {
			t.Errorf("over budget answered ok=%v %v, want a refusal that allows", ok, refused.Action)
		}
		release()
		if release, _, ok := p.Admit(ctx, 1000); !ok {
			t.Error("a body over the whole budget never runs")
		} else {
			release()
		}
		if s := p.Stats(); s.Refused != 1 || s.BodyBytes != 0 {
			t.Errorf("stats refused=%d body_bytes=%d, want 1 and 0", s.Refused, s.BodyBytes)
		}
		p.Close()
	}

	p := New(Config{MaxBodyBytes: 100, QueueWait: time.Second})
	defer p.Close()
	hold, _, _ := p.Admit(ctx, 60)
	large := make(chan bool)
	go func() {
		release, _, ok := p.Admit(ctx, 60)
		if ok {
			defer release()
		}
		large <- ok
	}()
	for p.bodies.waiting() == 0 {
		time.Sleep(time.Millisecond)
	}
	small, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, _, ok := p.Admit(small, 10); ok {
		t.Error("a small body overtook a large one waiting before it")
	}
	hold()
	if !<-large {
		t.Error("the waiting large body was not admitted when the budget freed")
	}
}

// blocker is an analyzer that holds its call until released.
type blocker struct{ entered, release chan struct{} }

func (b *blocker) Name() string { return "blocker" }
func (b *blocker) Analyze(context.Context, *Turn) (Report, error) {
	b.entered <- struct{}{}
	<-b.release
	return Report{}, nil
}

// Analysis slots are rationed apart from bodies: with every slot busy, a call is refused after the
// queue wait, and analyzed again once a slot frees.
func TestAnalysisSlots(t *testing.T) {
	b := &blocker{entered: make(chan struct{}), release: make(chan struct{})}
	p := New(Config{Analyzers: []Analyzer{b}, MaxInFlight: 1, QueueWait: 20 * time.Millisecond})
	defer p.Close()
	req := &llm.Request{Messages: []llm.Message{text(llm.RoleUser, "hi")}}
	go p.Handle(context.Background(), call("busy", req))
	<-b.entered
	if v := p.Handle(context.Background(), call("refused", req)); v.Action != gateway.Allow || p.Stats().Refused != 1 {
		t.Errorf("saturated analysis answered %v (refused %d), want a refusal that allows", v.Action, p.Stats().Refused)
	}
	b.release <- struct{}{}
	done := make(chan gateway.Verdict)
	go func() { done <- p.Handle(context.Background(), call("next", req)) }()
	<-b.entered
	b.release <- struct{}{}
	if v := <-done; v.Action != gateway.Allow {
		t.Errorf("call after the slot freed answered %v, want allow", v.Action)
	}
}

// A call the pipeline cannot analyze is allowed and counted as failed.
func TestFailurePolicy(t *testing.T) {
	broken := &gateway.Call{Gateway: "g", ID: "c", Phase: gateway.PhaseRequest, Enforce: true, Protocol: llm.OpenAIChat, Body: []byte("{not json")}
	p := New(Config{})
	defer p.Close()
	if v := p.Handle(context.Background(), broken); v.Action != gateway.Allow || p.Stats().Failed != 1 {
		t.Errorf("undecodable call answered %+v, failed %d", v, p.Stats().Failed)
	}
}

// An adapter that does not know the model (Portkey's webhook does not carry it apart from the
// body) gets the one the body names, on both phases; one it does know is kept.
func TestModelFromBody(t *testing.T) {
	rec := &recorder{}
	p := New(Config{Analyzers: []Analyzer{rec}})
	defer p.Close()
	for _, c := range []*gateway.Call{
		{Gateway: "g", ID: "c", Phase: gateway.PhaseRequest, Protocol: llm.OpenAIChat, Body: []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)},
		{Gateway: "g", ID: "c", Phase: gateway.PhaseResponse, Protocol: llm.OpenAIChat, Body: []byte(`{"model":"gpt-4o-2024","choices":[{"message":{"role":"assistant","content":"ok"}}]}`)},
		{Gateway: "g", ID: "c", Phase: gateway.PhaseRequest, Protocol: llm.OpenAIChat, Model: "routed", Body: []byte(`{"model":"gpt-4o","messages":[]}`)},
	} {
		p.Handle(context.Background(), c)
	}
	for i, want := range []string{"gpt-4o", "gpt-4o-2024", "routed"} {
		if got := rec.turns[i].Call.Model; got != want {
			t.Errorf("call %d: model %q, want %q", i, got, want)
		}
	}
}

// Records reach only the sinks routed for their kind, and Close delivers everything emitted
// before it.
func TestRoutesByKind(t *testing.T) {
	all, findings := &memSink{}, &memSink{}
	rec := &recorder{findings: []Finding{{Rule: "r", Severity: SeverityLow}}}
	p := New(Config{Analyzers: []Analyzer{rec}, Routes: []Route{
		{Name: "all", Sink: all},
		{Name: "findings", Sink: findings, Kinds: []string{KindFinding}},
	}})
	p.Handle(context.Background(), call("c1", &llm.Request{Messages: []llm.Message{text(llm.RoleUser, "hi")}}))
	p.Close()
	kinds := func(rs []Record) []string {
		out := make([]string, len(rs))
		for i, r := range rs {
			out[i] = r.Kind
		}
		slices.Sort(out)
		return out
	}
	if got := kinds(all.got); !slices.Equal(got, []string{KindFinding, "test.event"}) {
		t.Errorf("unfiltered route got %v", got)
	}
	if got := kinds(findings.got); !slices.Equal(got, []string{KindFinding}) {
		t.Errorf("findings route got %v", got)
	}
	if !all.closed || !findings.closed {
		t.Error("sinks not closed")
	}
	if f := findings.got[0].Value.(Finding); f.Session != "s1" || f.Rule != "r" || f.Analyzer != "recorder" {
		t.Errorf("finding = %+v", f)
	}
}
