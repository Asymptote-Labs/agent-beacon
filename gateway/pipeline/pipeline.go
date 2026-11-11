// Package pipeline is the shared machinery behind every gateway adapter: it decodes a call,
// resolves its session, works out what is new, runs the analyzers, applies policy, and hands
// records to sinks off the call path. It implements gateway.Handler.
//
//	adapter → Call → decode → session → delta → analyzers → policy → Verdict → adapter
//	                                                  ↘ records → routes → sinks (async)
//
// The call path holds no per-session memory: everything that outlives a call is in the Store, so
// replicas are interchangeable and the store can be shared.
//
// When nothing analysis finds can change a verdict (the default policy, and no Inline analyzer),
// the pipeline answers each call as soon as it is admitted and analyzes it afterwards, holding the
// call's analysis slot and body bytes until it is done.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/asymptote-labs/agent-beacon/gateway"
	"github.com/asymptote-labs/agent-beacon/gateway/harness"
	"github.com/asymptote-labs/agent-beacon/gateway/llm"
	"github.com/asymptote-labs/agent-beacon/gateway/llm/codec"
	"github.com/asymptote-labs/agent-beacon/gateway/store"
)

// Config assembles a pipeline.
type Config struct {
	// Analyzers run in order on every call; the events analyzer goes first.
	Analyzers []Analyzer
	// Policy decides verdicts; nil means AllowAll, and lets the pipeline answer calls before
	// analyzing them when no analyzer is Inline.
	Policy Policy
	// Observers see every analyzed call after its verdict.
	Observers []Observer
	Routes    []Route
	// Store holds cross-call state; nil means an in-memory store of a million keys.
	Store store.Store
	// SeenTTL is how long a session remembers having reported a system prompt or tool
	// definition (default 6h); past it, the content is reported once more.
	SeenTTL time.Duration
	// SessionHeaders are request headers naming the conversation, in order of preference.
	SessionHeaders []string
	// MaxBodyBytes bounds the bytes of call bodies being read and analyzed at once (default
	// 1 GiB); MaxInFlight bounds the calls being analyzed at once (default 2 per CPU). A call
	// waits up to QueueWait (default 250ms) for either, and is then refused with the policy's
	// Overloaded verdict, so overload costs a gateway a fast answer rather than a timeout.
	MaxBodyBytes int64
	MaxInFlight  int
	QueueWait    time.Duration
	Log          *slog.Logger
}

// DefaultSessionHeaders are the session headers harnesses are known to send.
var DefaultSessionHeaders = []string{"x-claude-code-session-id", "session_id", "x-session-id", "conversation_id"}

// Pipeline implements gateway.Handler.
type Pipeline struct {
	cfg    Config
	bodies *budget
	slots  chan struct{}
	out    *output
	seen   *seenMemo
	log    *slog.Logger
	// answerFirst is set when no verdict depends on analysis: Handle answers at admission.
	answerFirst bool
	// resent is set when an analyzer reads resent content.
	resent bool
	// pending counts analyses still running after their call was answered.
	pending   sync.WaitGroup
	closeOnce sync.Once
	stats     struct{ calls, blocked, failed, refused atomic.Uint64 }
}

// New builds a pipeline, starts its output routes and its background analyzers.
func New(cfg Config) *Pipeline {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	answerFirst := cfg.Policy == nil
	if cfg.Policy == nil {
		cfg.Policy = AllowAll{}
	}
	if cfg.Store == nil {
		cfg.Store = store.NewMemory(1_000_000)
	}
	if cfg.SeenTTL <= 0 {
		cfg.SeenTTL = 6 * time.Hour
	}
	if cfg.SessionHeaders == nil {
		cfg.SessionHeaders = DefaultSessionHeaders
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = 1 << 30
	}
	if cfg.MaxInFlight <= 0 {
		cfg.MaxInFlight = 2 * runtime.GOMAXPROCS(0)
	}
	if cfg.QueueWait <= 0 {
		cfg.QueueWait = 250 * time.Millisecond
	}
	p := &Pipeline{
		cfg:    cfg,
		bodies: &budget{size: cfg.MaxBodyBytes},
		slots:  make(chan struct{}, cfg.MaxInFlight),
		out:    newOutput(cfg.Routes, cfg.Log),
		seen:   newSeenMemo(),
		log:    cfg.Log,
	}
	for _, a := range cfg.Analyzers {
		if in, ok := a.(Inline); ok && in.Inline() {
			answerFirst = false
		}
		if r, ok := a.(ResentReader); ok && r.ReadsResent() {
			p.resent = true
		}
		if b, ok := a.(Background); ok {
			b.Start(Env{Emit: p.out.emit, Store: cfg.Store, Log: cfg.Log})
		}
	}
	p.answerFirst = answerFirst
	return p
}

// Close waits for analyses still running, stops the background analyzers, then flushes and
// closes the sinks. Call it after every adapter has stopped; calls after the first do nothing.
func (p *Pipeline) Close() {
	p.closeOnce.Do(func() {
		p.pending.Wait()
		for _, a := range p.cfg.Analyzers {
			if b, ok := a.(Background); ok {
				b.Stop()
			}
		}
		p.out.close()
	})
}

// AnswersFirst reports whether Handle answers calls before analyzing them.
func (p *Pipeline) AnswersFirst() bool { return p.answerFirst }

// Stats is a snapshot of the pipeline's counters.
type Stats struct {
	Calls   uint64            `json:"calls"`
	Blocked uint64            `json:"blocked"`
	Failed  uint64            `json:"failed"`
	Dropped map[string]uint64 `json:"dropped"`
	// Refused counts calls refused because the pipeline was saturated.
	Refused  uint64 `json:"refused"`
	InFlight int    `json:"in_flight"`
	// BodyBytes is the bytes of call bodies admitted and not yet released.
	BodyBytes int64 `json:"body_bytes"`
	// Analyzers holds the counters of analyzers that report their own (StatsReporter).
	Analyzers map[string]map[string]uint64 `json:"analyzers,omitempty"`
}

// StatsReporter is implemented by analyzers with counters worth exposing, such as work dropped
// off the call path under load.
type StatsReporter interface {
	Stats() map[string]uint64
}

// Stats returns the pipeline's counters.
func (p *Pipeline) Stats() Stats {
	s := Stats{
		Calls:     p.stats.calls.Load(),
		Blocked:   p.stats.blocked.Load(),
		Failed:    p.stats.failed.Load(),
		Dropped:   p.out.stats(),
		Refused:   p.stats.refused.Load(),
		InFlight:  len(p.slots),
		BodyBytes: p.bodies.inUse(),
	}
	for _, a := range p.cfg.Analyzers {
		if r, ok := a.(StatsReporter); ok {
			if s.Analyzers == nil {
				s.Analyzers = map[string]map[string]uint64{}
			}
			s.Analyzers[a.Name()] = r.Stats()
		}
	}
	return s
}

// Handle implements gateway.Handler.
func (p *Pipeline) Handle(ctx context.Context, c *gateway.Call) gateway.Verdict {
	if !p.analyze(ctx) {
		return p.overloaded("analysis slots")
	}
	if c.Time.IsZero() {
		c.Time = time.Now()
	}
	if !p.answerFirst {
		defer p.release()
		return p.run(ctx, c)
	}
	// The adapter releases its reservation for the body when Handle returns; the analysis
	// still reading it holds the bytes again until it ends. The two overlap, so the budget is
	// briefly counted twice rather than ever not at all.
	held := p.bodies.hold(int64(len(c.Body)))
	p.pending.Add(1)
	go func() {
		defer p.pending.Done()
		defer p.release()
		defer p.bodies.release(held)
		p.run(context.WithoutCancel(ctx), c)
	}()
	return allow
}

// run analyzes one call and returns its verdict.
func (p *Pipeline) run(ctx context.Context, c *gateway.Call) gateway.Verdict {
	start := time.Now()
	p.stats.calls.Add(1)

	var errs []error
	if err := decode(c); err != nil {
		errs = append(errs, err)
	}
	session, rootEntry, err := p.sessionID(ctx, c)
	if err != nil {
		errs = append(errs, fmt.Errorf("store: %w", err))
	}
	t := &Turn{Call: c, SessionID: session, Harness: harness.Identify(c.Client.UserAgent), Store: p.cfg.Store, wantsResent: p.resent}

	var findings, standing []Finding
	var recs []Record
	if len(errs) == 0 {
		switch c.Phase {
		case gateway.PhaseRequest:
			var err error
			if t.New, err = requestDelta(ctx, p.cfg.Store, p.seen, t.SessionID, c.Request, p.cfg.SeenTTL, rootEntry); err != nil {
				errs = append(errs, fmt.Errorf("store: %w", err))
			}
		case gateway.PhaseResponse:
			t.New = Delta{Output: c.Response}
		}
		for _, a := range p.cfg.Analyzers {
			rep, err := a.Analyze(ctx, t)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", a.Name(), err))
			}
			for _, fs := range [][]Finding{rep.Findings, rep.Standing} {
				for i := range fs {
					f := &fs[i]
					f.Analyzer, f.Session, f.Gateway, f.CallID, f.Phase = a.Name(), t.SessionID, c.Gateway, c.ID, c.Phase.String()
				}
			}
			findings = append(findings, rep.Findings...)
			standing = append(standing, rep.Standing...)
			recs = append(recs, rep.Records...)
		}
	}

	v := p.cfg.Policy.Decide(t, append(findings[:len(findings):len(findings)], standing...))
	if len(errs) > 0 {
		p.stats.failed.Add(1)
		p.log.Error("analysis incomplete", "gateway", c.Gateway, "call_id", c.ID, "phase", c.Phase, "err", errors.Join(errs...))
		v = p.cfg.Policy.Incomplete(t, v, errs)
	}
	if v.Action == gateway.Block && c.Enforce {
		p.stats.blocked.Add(1)
		p.log.Warn("blocked", "gateway", c.Gateway, "call_id", c.ID, "phase", c.Phase, "session", t.SessionID, "rules", v.Rules)
	}
	for _, f := range findings {
		recs = append(recs, Record{Kind: KindFinding, Value: f})
	}
	if len(p.cfg.Observers) > 0 {
		o := &Observation{Turn: t, Findings: findings, Standing: standing, Verdict: v, Errors: errs, Elapsed: time.Since(start)}
		for _, obs := range p.cfg.Observers {
			recs = append(recs, obs.Observe(o)...)
		}
	}
	p.out.emit(recs...)
	return v
}

// decode fills the call's Request or Response from its Body when the adapter left decoding to
// the pipeline. A request is skimmed: analysis reads what is new, and the history a client
// resends with every call is measured, not decoded.
func decode(c *gateway.Call) error {
	switch c.Phase {
	case gateway.PhaseRequest:
		if c.Request != nil {
			return nil
		}
		if c.Body == nil {
			return errors.New("request call carries no content")
		}
		r, err := codec.SkimRequest(c.Protocol, c.Body)
		if err != nil {
			return fmt.Errorf("decode request: %w", err)
		}
		c.Request = r
		if c.Model == "" {
			c.Model = r.Model
		}
	case gateway.PhaseResponse:
		if c.Response != nil {
			return nil
		}
		if c.Body == nil {
			return errors.New("response call carries no content")
		}
		r, err := codec.DecodeResponse(c.Protocol, c.Body)
		if err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		c.Response = r
		if c.Model == "" {
			c.Model = r.Model
		}
	default:
		return fmt.Errorf("unknown phase %d", c.Phase)
	}
	return nil
}

// rootTTL bounds how long a call's request remembers the session its root named, for the call's
// reply, which normally follows within minutes.
const rootTTL = time.Hour

// sessionID names the call's conversation: a session header the client sent, the gateway's own
// conversation id, or failing both the conversation's root (rootSession), and failing that the
// call itself. A reply carries no conversation, so its call's request leaves the session its root
// named in the store, under the call's id, for the reply to find: root is that entry, which the
// request adds in the batch requestDelta sends anyway. Only a reply whose session is named by a
// root costs a store round trip.
func (p *Pipeline) sessionID(ctx context.Context, c *gateway.Call) (session string, root *store.Entry, err error) {
	for _, h := range p.cfg.SessionHeaders {
		if v := c.Client.Headers[h]; v != "" {
			return v, nil, nil
		}
	}
	if c.Client.Session != "" {
		return c.Client.Session, nil, nil
	}
	key := "root/" + c.Gateway + "/" + c.ID
	switch c.Phase {
	case gateway.PhaseRequest:
		if c.Request != nil {
			if s, ok := rootSession(c); ok {
				return s, &store.Entry{Key: key, Value: []byte(s), TTL: rootTTL}, nil
			}
		}
	case gateway.PhaseResponse:
		vals, gerr := p.cfg.Store.Get(ctx, key)
		if gerr != nil {
			err = gerr
		} else if vals[0] != nil {
			return string(vals[0]), nil, nil
		}
	}
	return c.Gateway + ":" + c.ID, nil, err
}

// rootSession names a conversation no header names by its root, which a client resends unchanged
// with every call: a digest of the gateway, the caller's identity (user and key), the system text
// and the first non-system message, as "root:<hex>". The skim always decodes that message.
//
// It is a heuristic, with two known limits. Two conversations with the same identity, system text
// and first message (a scripted agent sending the same opening prompt twice) are one session, so
// their resent system text and tools are recorded once and a retried tail of one hides the other's
// identical tail. A conversation whose system text changes part-way (a client that puts the date or
// a changing status in it) is split where it changes. A request that carries no history (OpenAI
// Responses continuing previous_response_id) has no root to read and stays its own session.
func rootSession(c *gateway.Call) (string, bool) {
	msgs := c.Request.Messages
	first := -1
	for i := range msgs {
		if msgs[i].Role != llm.RoleSystem {
			first = i
			break
		}
	}
	if first < 0 || msgs[first].Elided {
		return "", false
	}
	d := newDigest()
	d.field(c.Gateway)
	d.field(c.Client.Identity.User)
	d.field(c.Client.Identity.Key)
	for i := range msgs {
		if msgs[i].Role == llm.RoleSystem {
			d.message(&msgs[i])
		}
	}
	d.message(&msgs[first])
	return "root:" + d.hex(), true
}
