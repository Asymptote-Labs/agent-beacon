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

// recorder is an analyzer that keeps the turns it saw and reports scripted findings. It is
// inline, so a call is analyzed before Handle returns.
type recorder struct {
	turns    []Turn
	findings []Finding
	err      error
}

func (r *recorder) Name() string { return "recorder" }
func (r *recorder) Inline() bool { return true }

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

// A call the default pipeline cannot analyze is allowed and counted as failed.
func TestUnanalyzableCallAllowed(t *testing.T) {
	broken := &gateway.Call{Gateway: "g", ID: "c", Phase: gateway.PhaseRequest, Enforce: true, Protocol: llm.OpenAIChat, Body: []byte("{not json")}
	p := New(Config{})
	if v := p.Handle(context.Background(), broken); v.Action != gateway.Allow {
		t.Errorf("undecodable call answered %+v", v)
	}
	p.Close()
	if p.Stats().Failed != 1 {
		t.Errorf("failed %d, want 1", p.Stats().Failed)
	}
}

// policyFake blocks a call with a finding, a call analysis could not complete when it is
// enforced, and every call refused under overload; it records what it was asked.
type policyFake struct {
	decided    []int
	incomplete [][]error
}

func (p *policyFake) Decide(_ *Turn, findings []Finding) gateway.Verdict {
	p.decided = append(p.decided, len(findings))
	if len(findings) > 0 {
		return gateway.Verdict{Action: gateway.Block, Reason: "test: finding", Rules: []string{findings[0].Rule}}
	}
	return gateway.Verdict{}
}

func (p *policyFake) Incomplete(t *Turn, decided gateway.Verdict, errs []error) gateway.Verdict {
	p.incomplete = append(p.incomplete, errs)
	if t.Call.Enforce {
		return gateway.Verdict{Action: gateway.Block, Reason: "test: incomplete"}
	}
	return decided
}

func (p *policyFake) Overloaded() gateway.Verdict {
	return gateway.Verdict{Action: gateway.Block, Reason: "test: overloaded"}
}

// A policy decides every verdict: findings, standing ones included, through Decide; a call whose
// analysis did not complete (it did not decode, or an analyzer failed) through Incomplete; a call
// refused for capacity, at admission or for an analysis slot, through Overloaded.
func TestPolicyDecidesEveryVerdict(t *testing.T) {
	ctx := context.Background()
	req := &llm.Request{Messages: []llm.Message{text(llm.RoleUser, "hi")}}

	pol := &policyFake{}
	rec := &recorder{}
	p := New(Config{Analyzers: []Analyzer{rec}, Policy: pol, MaxBodyBytes: 10, QueueWait: time.Millisecond})
	defer p.Close()
	if v := p.Handle(ctx, call("clean", req)); v.Action != gateway.Allow {
		t.Errorf("clean call: %+v", v)
	}
	rec.findings = []Finding{{Rule: "r1", Severity: SeverityHigh}}
	if v := p.Handle(ctx, call("found", req)); v.Action != gateway.Block || !slices.Equal(v.Rules, []string{"r1"}) {
		t.Errorf("call with a finding: %+v", v)
	}
	rec.findings, rec.err = nil, errors.New("boom")
	if v := p.Handle(ctx, call("failed", req)); v.Reason != "test: incomplete" || len(pol.incomplete) != 1 {
		t.Errorf("call whose analyzer failed: %+v (incomplete asked %d times)", v, len(pol.incomplete))
	}
	broken := &gateway.Call{Gateway: "g", ID: "c", Phase: gateway.PhaseRequest, Protocol: llm.OpenAIChat, Body: []byte("{not json")}
	if v := p.Handle(ctx, broken); v.Action != gateway.Allow || len(pol.incomplete) != 2 {
		t.Errorf("unenforced undecodable call: %+v (incomplete asked %d times)", v, len(pol.incomplete))
	}
	hold, _, _ := p.Admit(ctx, 10)
	if _, v, ok := p.Admit(ctx, 5); ok || v.Reason != "test: overloaded" {
		t.Errorf("refused at admission: ok=%v %+v", ok, v)
	}
	hold()
	if p.AnswersFirst() {
		t.Error("a pipeline with a policy answers before analyzing")
	}
}

// gate is an analyzer that holds every call until released, and reports when it ends.
type gate struct {
	entered, release, done chan struct{}
}

func (g *gate) Name() string { return "gate" }
func (g *gate) Analyze(context.Context, *Turn) (Report, error) {
	g.entered <- struct{}{}
	<-g.release
	defer func() { g.done <- struct{}{} }()
	return Report{Records: []Record{{Kind: "test.event"}}}, nil
}

// With the default policy and no inline analyzer, nothing analysis finds changes a verdict: Handle
// answers at once and analysis goes on after it, holding its analysis slot and the body's bytes
// until it ends, and Close waits for it before closing the sinks.
func TestAnswersFirst(t *testing.T) {
	g := &gate{entered: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{}, 1)}
	sink := &memSink{}
	p := New(Config{Analyzers: []Analyzer{g}, Routes: []Route{{Name: "all", Sink: sink}}})
	if !p.AnswersFirst() {
		t.Fatal("default pipeline does not answer first")
	}
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	c := &gateway.Call{Gateway: "g", ID: "c", Phase: gateway.PhaseRequest, Enforce: true, Protocol: llm.OpenAIChat, Body: body}
	release, _, _ := p.Admit(context.Background(), int64(len(body)))
	if v := p.Handle(context.Background(), c); v.Action != gateway.Allow {
		t.Fatalf("answered %+v", v)
	}
	release() // the adapter is done with the call
	<-g.entered
	if s := p.Stats(); s.InFlight != 1 || s.BodyBytes != int64(len(body)) {
		t.Errorf("while analysis runs: in_flight %d body_bytes %d, want 1 and %d", s.InFlight, s.BodyBytes, len(body))
	}
	closed := make(chan struct{})
	go func() { p.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("Close returned while an analysis was running")
	case <-time.After(20 * time.Millisecond):
	}
	close(g.release)
	<-closed
	if s := p.Stats(); s.InFlight != 0 || s.BodyBytes != 0 || s.Calls != 1 {
		t.Errorf("after analysis: %+v", s)
	}
	if len(sink.got) != 1 || !sink.closed {
		t.Errorf("sink got %d records (closed %v), want the analysis's one before closing", len(sink.got), sink.closed)
	}
}

// observer records what it was shown and adds a record of its own.
type observer struct{ seen []Observation }

func (o *observer) Observe(ob *Observation) []Record {
	o.seen = append(o.seen, *ob)
	return []Record{{Kind: "test.observed", Value: ob.Turn.Call.ID}}
}

// An observer sees each call after its verdict, with the findings, errors and time that led to
// it, and its records join the call's.
func TestObserver(t *testing.T) {
	rec := &recorder{findings: []Finding{{Rule: "r", Severity: SeverityLow}}, err: errors.New("boom")}
	ob := &observer{}
	sink := &memSink{}
	p := New(Config{Analyzers: []Analyzer{rec}, Policy: &policyFake{}, Observers: []Observer{ob}, Routes: []Route{{Name: "all", Sink: sink, Kinds: []string{"test.observed"}}}})
	p.Handle(context.Background(), call("c1", &llm.Request{Messages: []llm.Message{text(llm.RoleUser, "hi")}}))
	p.Close()
	if len(ob.seen) != 1 {
		t.Fatalf("observed %d calls", len(ob.seen))
	}
	o := ob.seen[0]
	if o.Turn.Call.ID != "c1" || len(o.Findings) != 1 || o.Verdict.Reason != "test: incomplete" || len(o.Errors) != 1 || o.Elapsed <= 0 {
		t.Errorf("observation = %+v", o)
	}
	if len(sink.got) != 1 || sink.got[0].Value != "c1" {
		t.Errorf("observer records = %+v", sink.got)
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
