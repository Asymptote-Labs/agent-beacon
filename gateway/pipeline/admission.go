package pipeline

import (
	"container/list"
	"context"
	"sync"
	"time"

	"github.com/asymptote-labs/agent-beacon/gateway"
)

// Admission rations two resources separately (see gateway.Handler). Admit reserves memory for a
// body before the adapter reads it, against a budget of body bytes; Handle takes one of a few
// analysis slots per processor once the body is in. A single limit on calls in flight, taken
// before reading, held a slot through every upload: under load the slots were spent waiting on
// the network while the processors idled, and refusals rose with throughput half what the
// processors could do.

// budget is a weighted semaphore over bytes, granted first come, first served, so a large body is
// not starved by a stream of small ones.
type budget struct {
	mu      sync.Mutex
	size    int64
	used    int64
	waiters list.List // of *budgetWaiter
}

type budgetWaiter struct {
	n     int64
	ready chan struct{}
}

// acquire reserves n bytes (at most the whole budget), waiting up to wait.
func (b *budget) acquire(ctx context.Context, n int64, wait time.Duration) bool {
	b.mu.Lock()
	if b.used+n <= b.size && b.waiters.Len() == 0 {
		b.used += n
		b.mu.Unlock()
		return true
	}
	w := &budgetWaiter{n: n, ready: make(chan struct{})}
	e := b.waiters.PushBack(w)
	b.mu.Unlock()

	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-w.ready:
		return true
	case <-t.C:
	case <-ctx.Done():
	}
	b.mu.Lock()
	select {
	case <-w.ready: // granted while timing out: keep it
		b.mu.Unlock()
		return true
	default:
	}
	b.waiters.Remove(e)
	b.grant() // the head may have changed
	b.mu.Unlock()
	return false
}

func (b *budget) release(n int64) {
	b.mu.Lock()
	b.used -= n
	b.grant()
	b.mu.Unlock()
}

// hold counts n more bytes as in use without waiting, clamped to the budget like a reservation:
// for a body an analysis keeps reading after its adapter released the reservation it was read
// under. It returns what to release.
func (b *budget) hold(n int64) int64 {
	n = min(max(n, 0), b.size)
	b.mu.Lock()
	b.used += n
	b.mu.Unlock()
	return n
}

// grant admits waiters in order while the next fits. b.mu is held.
func (b *budget) grant() {
	for e := b.waiters.Front(); e != nil; e = b.waiters.Front() {
		w := e.Value.(*budgetWaiter)
		if b.used+w.n > b.size {
			return
		}
		b.used += w.n
		b.waiters.Remove(e)
		close(w.ready)
	}
}

func (b *budget) inUse() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}

func (b *budget) waiting() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.waiters.Len()
}

// Admit implements gateway.Handler: it reserves size bytes, clamped to the whole budget so that
// one body larger than the budget can still run, alone.
func (p *Pipeline) Admit(ctx context.Context, size int64) (func(), gateway.Verdict, bool) {
	n := min(max(size, 0), p.cfg.MaxBodyBytes)
	if !p.bodies.acquire(ctx, n, p.cfg.QueueWait) {
		return nil, p.overloaded("body budget"), false
	}
	return func() { p.bodies.release(n) }, gateway.Verdict{}, true
}

// analyze takes an analysis slot, waiting up to QueueWait; release gives it back.
func (p *Pipeline) analyze(ctx context.Context) bool {
	select {
	case p.slots <- struct{}{}:
		return true
	default:
	}
	t := time.NewTimer(p.cfg.QueueWait)
	defer t.Stop()
	select {
	case p.slots <- struct{}{}:
		return true
	case <-t.C:
	case <-ctx.Done():
	}
	return false
}

func (p *Pipeline) release() { <-p.slots }

// overloaded counts a refusal and returns the policy's verdict for it.
func (p *Pipeline) overloaded(what string) gateway.Verdict {
	if n := p.stats.refused.Add(1); n&(n-1) == 0 {
		p.log.Warn("overloaded: calls refused", "refused", n, "limit", what, "max_in_flight", p.cfg.MaxInFlight, "max_body_bytes", p.cfg.MaxBodyBytes)
	}
	return p.cfg.Policy.Overloaded()
}
