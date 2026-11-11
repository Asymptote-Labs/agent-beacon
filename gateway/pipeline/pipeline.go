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
	// still reading it holds the bytes again until it ends, with the request body a reply
	// carries. The two overlap, so the budget is briefly counted twice rather than ever not at
	// all.
	held := p.bodies.hold(int64(len(c.Body) + len(c.RequestBody)))
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
	session, named, previous := p.sessionID(c)
	t := &Turn{Call: c, SessionID: session, PreviousResponse: previous, Harness: harness.Identify(c.Client.UserAgent), Store: p.cfg.Store, wantsResent: p.resent}

	var findings, standing []Finding
	var recs []Record
	if len(errs) == 0 {
		switch c.Phase {
		case gateway.PhaseRequest:
			// A resent tail is taken for a retry only in a session the client or the gateway
			// named, and never for a stateful request, whose tail is only its new turn: a root
			// shared by two conversations, or a repeated "yes", must never lose a prompt.
			retries := named && !c.Request.Stateful()
			var err error
			if t.New, err = requestDelta(ctx, p.cfg.Store, p.seen, t.SessionID, c.Request, p.cfg.SeenTTL, retries); err != nil {
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

// sessionID names the call's conversation, and reads the provider response its request continues
// (Turn.PreviousResponse). It reads no store: everything it uses is on the call, so both phases of
// a call reach the same name by the same function, in any order and on any replica.
//
// The session is a session header the client sent, or the gateway's own conversation id; failing
// both, what the request's body says (callRoot; a reply reads the body of the request it answers,
// Call.RequestBody): the provider's conversation id, or the conversation's root (rootSession);
// failing those, the call itself. named reports that the client, the gateway or the provider named
// it (a header, the gateway's conversation id, the provider's conversation id), not the pipeline:
// only there is a resent tail taken for a retry. A request that continues previous_response_id,
// with nothing else naming its conversation, is its own session: the response it continues is on
// its events, and on its reply's, for whoever stitches the turns of such a chain together.
func (p *Pipeline) sessionID(c *gateway.Call) (session string, named bool, previous string) {
	for _, h := range p.cfg.SessionHeaders {
		if v := c.Client.Headers[h]; v != "" {
			return v, true, decodedPrevious(c)
		}
	}
	if c.Client.Session != "" {
		return c.Client.Session, true, decodedPrevious(c)
	}
	own := c.Gateway + ":" + c.ID
	root, ok := callRoot(c)
	switch {
	case !ok:
	case root.Conversation != "":
		return root.Conversation, true, root.PreviousResponse
	case root.PreviousResponse != "":
		return own, false, root.PreviousResponse
	default:
		if s, ok := rootSession(c, &root); ok {
			return s, false, ""
		}
	}
	return own, false, ""
}

// decodedPrevious is the response a decoded request continues. A reply whose session a header
// names does not read it: that would mean scanning the request's body, and its session already
// holds the chain.
func decodedPrevious(c *gateway.Call) string {
	if c.Phase == gateway.PhaseRequest && c.Request != nil {
		return c.Request.PreviousResponse
	}
	return ""
}

// callRoot reads what the body of a call's request says about its conversation: a request's
// Body, a reply's RequestBody. A call without one has no root.
func callRoot(c *gateway.Call) (codec.Root, bool) {
	body := c.Body
	if c.Phase == gateway.PhaseResponse {
		body = c.RequestBody
	}
	if body == nil {
		return codec.Root{}, false
	}
	r, err := codec.ReadRoot(c.Protocol, body)
	return r, err == nil
}

// rootSession names a conversation no header names by its root, which a client resends unchanged
// with every call: a digest of the gateway, the client's harness, the caller's identity (every
// field the gateway resolved), the system text sent ahead of the conversation, as written, and
// its first non-system message, as "root:<hex>". A call with no identity at all gets no root: the
// root would then merge different people's conversations that open alike.
//
// It is a heuristic. Two conversations of one caller in one harness with the same system text
// that open with the same message are one session; they share a session.id, and lose nothing, as
// a root session never takes a tail for a retry. A client that changes its system text part-way
// through a conversation splits it into two sessions there. System text sent after the messages
// (an Anthropic body may send it last) is not read, as that would mean reading past the whole
// history. A stateful request has no root (sessionID).
func rootSession(c *gateway.Call, root *codec.Root) (string, bool) {
	id := c.Client.Identity
	if id == (gateway.Identity{}) || root.First == nil {
		return "", false
	}
	d := newDigest()
	d.field(c.Gateway)
	d.field(harness.Identify(c.Client.UserAgent).Name)
	for _, f := range [...]string{id.User, id.Email, id.Team, id.Org, id.Key} {
		d.field(f)
	}
	d.int(len(root.System))
	for _, s := range root.System {
		d.bytes(s)
	}
	d.message(root.First)
	return "root:" + d.hex(), true
}
