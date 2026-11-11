package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"google.golang.org/grpc/status"
)

type target uint8

const (
	tLiteLLM target = iota
	tPortkey
	tEnvoy
	nTargets
)

var targetNames = [nTargets]string{"litellm", "portkey", "envoy"}

func (t target) String() string { return targetNames[t] }

type phase uint8

const (
	phRequest  phase = iota // one request-phase call (Envoy: request_body → answer)
	phResponse              // one response-phase call (Envoy: response body, all chunks)
	phExchange              // Envoy: the whole ext_proc stream of one turn
	phChaos                 // one chaos action
	nPhases
)

var phaseNames = [nPhases]string{"request", "response", "exchange", "chaos"}

// errClass classifies a failed call.
type errClass uint8

const (
	eConnRefused errClass = iota
	eConnTimeout
	eReset
	eEOF
	eClientTimeout
	eCanceled
	eAddrNotAvail
	eTooManyFiles
	eBrokenPipe
	eH2
	eOther
	nErrClasses
)

var errNames = [nErrClasses]string{
	"connect refused", "connect timeout", "reset", "EOF", "client timeout", "canceled",
	"addr not available (ephemeral ports)", "too many open files", "broken pipe", "http2 protocol", "other",
}

// violation is a protocol violation: an answer of the wrong shape.
type violation uint8

const (
	vReplyShape    violation = iota // HTTP 200 body that is not the adapter's verdict JSON
	vMessageKind                    // ext_proc answer of the wrong kind for the message sent
	vImmediate                      // ext_proc immediate response that is malformed
	vStreamEnd                      // ext_proc stream not ended with EOF after CloseSend
	vBlockedStatus                  // blocked HTTP reply without the expected shape
	nViolations
)

var violationNames = [nViolations]string{
	"reply body not a verdict", "ext_proc answer of wrong kind", "malformed immediate response",
	"stream did not end with EOF", "block reply malformed",
}

// expect is what a call's verdict should be.
type expect uint8

const (
	expAllow      expect = iota
	expBlock             // a bad call, when the server blocks bad calls (-expect-blocks)
	expUnenforced        // a bad call the adapter cannot enforce (Envoy STREAMED reply)
)

// shard holds the counters of a set of workers. Fields are atomic, written by the shard's
// workers and read by the timeline.
type shard struct {
	hist      [nTargets][nPhases]hist
	errs      [nTargets][nErrClasses]atomic.Uint64
	status    [nTargets][600]atomic.Uint64 // non-200 HTTP statuses
	grpc      [nTargets][17]atomic.Uint64
	viol      [nTargets][nViolations]atomic.Uint64
	allow     [nTargets]atomic.Uint64
	block     [nTargets]atomic.Uint64
	badBlock  [nTargets]atomic.Uint64 // expected block, blocked
	falseAllw [nTargets]atomic.Uint64
	falseBlk  [nTargets]atomic.Uint64
	unenf     [nTargets]atomic.Uint64 // bad calls the adapter could not enforce, allowed as designed
	inflight  atomic.Int64
	sessions  atomic.Uint64 // session generations started
	_         [64]byte
}

type metrics struct {
	shards []shard

	mu      sync.Mutex
	samples map[string]string // first error message per class
	chaos   map[string]uint64 // "<target> <action>: <outcome>"
	chaosUn atomic.Uint64     // chaos actions with an unexpected outcome
}

func newMetrics(n int) *metrics {
	return &metrics{shards: make([]shard, n), samples: map[string]string{}, chaos: map[string]uint64{}}
}

func (m *metrics) sample(key, msg string) {
	m.mu.Lock()
	if _, ok := m.samples[key]; !ok {
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		m.samples[key] = msg
	}
	m.mu.Unlock()
}

func (m *metrics) chaosOutcome(t target, action, outcome string, expected bool) {
	if !expected {
		outcome += " (UNEXPECTED)"
		m.chaosUn.Add(1)
	}
	m.mu.Lock()
	m.chaos[t.String()+" "+action+": "+outcome]++
	m.mu.Unlock()
}

func (m *metrics) sortedChaos() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.chaos))
	for k, v := range m.chaos {
		out = append(out, fmt.Sprintf("%-70s %d", k, v))
	}
	sort.Strings(out)
	return out
}

// recordErr counts err against t and returns its class name.
func (m *metrics) recordErr(sh *shard, t target, err error) string {
	if st, ok := status.FromError(err); ok {
		c := st.Code()
		if int(c) < len(sh.grpc[t]) {
			sh.grpc[t][c].Add(1)
		}
		key := t.String() + " grpc " + c.String()
		m.sample(key, st.Message())
		return key
	}
	c := classify(err)
	sh.errs[t][c].Add(1)
	key := t.String() + " " + errNames[c]
	m.sample(key, err.Error())
	return key
}

func classify(err error) errClass {
	var op *net.OpError
	dial := errors.As(err, &op) && op.Op == "dial"
	var ne net.Error
	timeout := errors.As(err, &ne) && ne.Timeout()
	msg := err.Error()
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return eConnRefused
	case dial && (timeout || errors.Is(err, syscall.ETIMEDOUT)):
		return eConnTimeout
	case errors.Is(err, syscall.EADDRNOTAVAIL):
		return eAddrNotAvail
	case errors.Is(err, syscall.EMFILE), errors.Is(err, syscall.ENFILE):
		return eTooManyFiles
	case errors.Is(err, syscall.ECONNRESET), strings.Contains(msg, "connection reset"):
		return eReset
	case errors.Is(err, syscall.EPIPE), strings.Contains(msg, "broken pipe"):
		return eBrokenPipe
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded), timeout:
		return eClientTimeout
	case errors.Is(err, context.Canceled):
		return eCanceled
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), strings.Contains(msg, "EOF"):
		return eEOF
	case strings.Contains(msg, "http2"), strings.Contains(msg, "GOAWAY"), strings.Contains(msg, "stream error"):
		return eH2
	}
	return eOther
}

// judge counts a verdict against its expectation.
func (sh *shard) judge(t target, exp expect, blocked bool) {
	if blocked {
		sh.block[t].Add(1)
	} else {
		sh.allow[t].Add(1)
	}
	switch {
	case exp == expBlock && blocked:
		sh.badBlock[t].Add(1)
	case exp == expBlock:
		sh.falseAllw[t].Add(1)
	case exp == expUnenforced && !blocked:
		sh.unenf[t].Add(1)
	case exp == expAllow && blocked:
		sh.falseBlk[t].Add(1)
	case exp == expUnenforced && blocked:
		// An adapter enforcing what it documents it cannot is a protocol surprise, not a false
		// block of a benign call; counted as a violation by the caller.
	}
}

// totals sums a per-target counter across shards.
func (m *metrics) totals(f func(*shard) *[nTargets]atomic.Uint64) [nTargets]uint64 {
	var out [nTargets]uint64
	for i := range m.shards {
		c := f(&m.shards[i])
		for t := range out {
			out[t] += c[t].Load()
		}
	}
	return out
}

func (m *metrics) inflight() int64 {
	var n int64
	for i := range m.shards {
		n += m.shards[i].inflight.Load()
	}
	return n
}

// merged merges every shard's histogram of (t, p).
func (m *metrics) merged(t target, p phase) snap {
	var s snap
	for i := range m.shards {
		s.add(&m.shards[i].hist[t][p])
	}
	return s
}

// errTotal is every failure counted so far: errors, non-200 statuses, gRPC codes, violations.
func (m *metrics) errTotal() uint64 {
	var n uint64
	for i := range m.shards {
		sh := &m.shards[i]
		for t := range nTargets {
			for c := range sh.errs[t] {
				n += sh.errs[t][c].Load()
			}
			for c := range sh.status[t] {
				n += sh.status[t][c].Load()
			}
			for c := range sh.grpc[t] {
				n += sh.grpc[t][c].Load()
			}
			for c := range sh.viol[t] {
				n += sh.viol[t][c].Load()
			}
		}
	}
	return n
}
