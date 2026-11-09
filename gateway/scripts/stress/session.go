package main

import (
	"bytes"
	"context"
	"math/rand/v2"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/asymptote-labs/agent-beacon/gateway/internal/synth"
)

// slot is one session as a worker advances it: request phase then response phase, turn by turn.
// A session that ends (its last turn, or its bad call) is replaced by a new generation with a new
// id, as a harness opens a new conversation.
type slot struct {
	base    int
	gen     int
	id      []byte // session id; JSON-safe
	script  *synth.Script
	turn    int // next turn
	bad     synth.Bad
	badTurn int
	alt     bool // Portkey: Anthropic Messages instead of Chat; Envoy: SSE instead of JSON reply
}

// turnPlan is one turn's calls and their expected verdicts.
type turnPlan struct {
	s          *slot
	k          int
	reqBad     synth.Bad
	respBad    synth.Bad
	expReq     expect
	expResp    expect
	callID     string
	sessionKey string
}

type worker struct {
	id   int
	widx int // index among the workers of the same target
	tgt  target
	r    *rand.Rand
	sh   *shard
	run  *runner

	slots []*slot
	next  int
	storm uint64

	http  *httpConn
	buf   bytes.Buffer // HTTP reply, reused
	envoy *envoyConn
	ev    *envoyMsgs
}

func (w *worker) newGeneration(s *slot, first bool) {
	cfg := w.run.cfg
	s.gen++
	s.script = w.run.scripts[w.r.IntN(len(w.run.scripts))]
	T := s.script.Turns()
	start := 1
	switch {
	case cfg.scenario == "bigbody":
		// Long conversations resumed late: every body is near the turn cap.
		start = T*3/4 + 1 + w.r.IntN(T-T*3/4)
	case first:
		// Spread the first sessions over the whole length so body sizes are mixed from the start
		// instead of every worker climbing from turn 1 in lockstep.
		start = 1 + w.r.IntN(T)
	}
	s.turn = start
	s.bad, s.badTurn = synth.Benign, 0
	if cfg.badRatio > 0 && w.r.Float64() < cfg.badRatio {
		s.bad = synth.CurlPipe
		if w.r.IntN(2) == 0 {
			s.bad = synth.Injection
		}
		s.badTurn = start + w.r.IntN(T-start+1)
	}
	switch cfg.scenario {
	case "hotsession":
		s.id = w.run.hotID
	default:
		s.id = append(append(append(append(s.id[:0], w.run.runID...), "-s"...), strconv.Itoa(s.base)...), "-g"...)
		s.id = strconv.AppendInt(s.id, int64(s.gen), 10)
		if s.bad != synth.Benign {
			// A bad session never shares its id, so its findings stand against it alone.
			s.id = append(s.id, "-bad"...)
		}
	}
	w.sh.sessions.Add(1)
}

// plan picks the next session and its turn.
func (w *worker) plan() turnPlan {
	s := w.slots[w.next]
	w.next = (w.next + 1) % len(w.slots)
	k := s.turn
	p := turnPlan{s: s, k: k}
	if s.bad != synth.Benign && k == s.badTurn && w.run.cfg.expectBlocks {
		switch s.bad {
		case synth.Injection:
			p.reqBad, p.expReq = synth.Injection, expBlock
		case synth.CurlPipe:
			p.respBad, p.expResp = synth.CurlPipe, expBlock
			if w.run.cfg.scenario == "envoy-streamed" {
				p.expResp = expUnenforced
			}
		}
	}
	if w.run.cfg.scenario == "sessionstorm" {
		// Every call a brand-new session: the store sees only first contacts.
		w.storm++
		s.id = append(append(append(append(s.id[:0], w.run.runID...), "-storm-w"...), strconv.Itoa(w.id)...), "-n"...)
		s.id = strconv.AppendUint(s.id, w.storm, 10)
	}
	p.sessionKey = string(s.id)
	p.callID = p.sessionKey + "-t" + strconv.Itoa(k)
	return p
}

// advance moves the session past the turn just played.
func (w *worker) advance(p turnPlan) {
	s := p.s
	if (s.bad != synth.Benign && p.k >= s.badTurn) || p.k >= s.script.Turns() {
		w.newGeneration(s, false)
		return
	}
	s.turn++
}

// step plays one unit of work: a turn, or in the chaos scenario sometimes a chaos action.
func (w *worker) step(ctx context.Context) {
	if w.run.cfg.scenario == "chaos" {
		if a := pickChaos(w.r.Float64()); a != chaosNone {
			w.chaos(ctx, a)
			return
		}
	}
	p := w.plan()
	switch w.tgt {
	case tEnvoy:
		w.envoyTurn(ctx, p)
	default:
		w.httpTurn(ctx, p)
	}
	w.advance(p)
}

// begin counts a call in flight; end uncounts it and, for a successful call, records its latency.
func (w *worker) begin() time.Time {
	w.sh.inflight.Add(1)
	return time.Now()
}

func (w *worker) end(start time.Time, ph phase, ok bool) time.Duration {
	d := time.Since(start)
	w.sh.inflight.Add(-1)
	if ok {
		w.sh.hist[w.tgt][ph].record(d)
	}
	return d
}

// loop runs steps until stop is set.
func (w *worker) loop(ctx context.Context, stop *atomic.Bool) {
	for !stop.Load() {
		w.step(ctx)
	}
}
