// Package pipeline is the shared machinery behind every gateway adapter: it decodes a call,
// resolves its session, works out what is new, runs the analyzers, applies policy, and hands
// records to sinks off the call path. It implements gateway.Handler.
//
//	adapter → Call → decode → session → delta → analyzers → policy → Verdict → adapter
//	                                                  ↘ records → routes → sinks (async)
//
// The call path holds no per-session memory: everything that outlives a call is in the Store, so
// replicas are interchangeable and the store can be shared.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/asymptote-labs/agent-beacon/gateway"
	"github.com/asymptote-labs/agent-beacon/gateway/harness"
	"github.com/asymptote-labs/agent-beacon/gateway/llm/codec"
	"github.com/asymptote-labs/agent-beacon/gateway/store"
)

// Config assembles a pipeline.
type Config struct {
	Analyzers []Analyzer
	// Policy decides verdicts; nil means AllowAll.
	Policy Policy
	Routes []Route
	// Store holds cross-call state; nil means an in-memory store of a million keys.
	Store store.Store
	// SeenTTL is how long a session remembers having reported a system prompt or tool
	// definition (default 6h); past it, the content is reported once more.
	SeenTTL time.Duration
	// SessionHeaders are request headers naming the conversation, in order of preference.
	SessionHeaders []string
	// MaxBodyBytes bounds the bytes of call bodies being read and analyzed at once (default
	// 1 GiB); MaxInFlight bounds the calls being analyzed at once (default 2 per CPU). A call
	// waits up to QueueWait (default 250ms) for either, and is then refused with an allow, so
	// overload costs a gateway a fast answer rather than a timeout.
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
	stats  struct{ calls, blocked, failed, refused atomic.Uint64 }
}

// New builds a pipeline, starts its output routes and its background analyzers.
func New(cfg Config) *Pipeline {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
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
		if b, ok := a.(Background); ok {
			b.Start(Env{Emit: p.out.emit, Store: cfg.Store, Log: cfg.Log})
		}
	}
	return p
}

// Close stops the background analyzers, then flushes and closes the sinks. Call it after every
// adapter has stopped.
func (p *Pipeline) Close() {
	for _, a := range p.cfg.Analyzers {
		if b, ok := a.(Background); ok {
			b.Stop()
		}
	}
	p.out.close()
}

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
	defer p.release()
	start := time.Now()
	if c.Time.IsZero() {
		c.Time = start
	}
	p.stats.calls.Add(1)

	var errs []error
	if err := decode(c); err != nil {
		errs = append(errs, err)
	}
	t := &Turn{Call: c, SessionID: p.sessionID(c), Harness: harness.Identify(c.Client.UserAgent), Store: p.cfg.Store}

	var findings, standing []Finding
	var recs []Record
	if len(errs) == 0 {
		switch c.Phase {
		case gateway.PhaseRequest:
			var err error
			if t.New, err = requestDelta(ctx, p.cfg.Store, p.seen, t.SessionID, c.Request, p.cfg.SeenTTL); err != nil {
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
	}
	if v.Action == gateway.Block && c.Enforce {
		p.stats.blocked.Add(1)
		p.log.Warn("blocked", "gateway", c.Gateway, "call_id", c.ID, "phase", c.Phase, "session", t.SessionID, "rules", v.Rules)
	}
	for _, f := range findings {
		recs = append(recs, Record{Kind: KindFinding, Value: f})
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

// sessionID names the call's conversation: a session header the client sent, the gateway's own
// conversation id, or failing both the call itself.
func (p *Pipeline) sessionID(c *gateway.Call) string {
	for _, h := range p.cfg.SessionHeaders {
		if v := c.Client.Headers[h]; v != "" {
			return v
		}
	}
	if c.Client.Session != "" {
		return c.Client.Session
	}
	return c.Gateway + ":" + c.ID
}
