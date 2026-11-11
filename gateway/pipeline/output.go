package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/bits"
	"slices"
	"sync/atomic"
	"time"

	"github.com/asymptote-labs/agent-beacon/gateway/internal/batchq"
)

// Record is one output: a finding, or an analyzer's own record. Value must marshal to JSON.
type Record struct {
	Kind  string
	Value any
}

// KindFinding is the record kind of a finding, which the pipeline emits for every analyzer.
const KindFinding = "finding"

// Sink delivers records somewhere. Write is called from one goroutine per sink, with batches in
// emission order, and must not keep batch after returning. An error means the batch was dropped,
// unless it is a *WriteError, which says how many of its records were; either way the error is
// logged and the dropped records are counted in the route's Stats.Dropped. A sink that must not
// lose records retries inside Write, and a sink whose writes can fail must recover by the next
// one. Close is called once, after the last Write.
type Sink interface {
	Write(ctx context.Context, batch []Record) error
	Close() error
}

// WriteError is a Write error for a batch only part of which was dropped.
type WriteError struct {
	Dropped int
	Err     error
}

func (e *WriteError) Error() string {
	return fmt.Sprintf("%d records dropped: %v", e.Dropped, e.Err)
}

func (e *WriteError) Unwrap() error { return e.Err }

// droppedBy is how many records of batch a Write error says were dropped.
func droppedBy(err error, batch int) int {
	if we, ok := errors.AsType[*WriteError](err); ok {
		return min(max(we.Dropped, 0), batch)
	}
	return batch
}

// Route sends the records of the listed kinds to a sink; no kinds means every kind.
type Route struct {
	Name  string
	Sink  Sink
	Kinds []string
}

const (
	routeQueue = 16384
	routeBatch = 512
	routeFlush = 250 * time.Millisecond
)

// output fans records out to routes without slowing the call path. A call hands all its records
// over at once, and each route takes them under one short lock without waking its writer (see
// batchq): when a sink falls behind, new records are dropped and counted.
type output struct {
	routes []*route
	log    *slog.Logger
}

type route struct {
	Route
	q *batchq.Queue[Record]
	// failed counts the records the sink reported dropped.
	failed atomic.Uint64
}

func newOutput(routes []Route, log *slog.Logger) *output {
	o := &output{log: log}
	for _, r := range routes {
		rt := &route{Route: r}
		rt.q = batchq.New(routeQueue, routeBatch, routeFlush, func(batch []Record) {
			if err := rt.Sink.Write(context.Background(), batch); err != nil {
				n := droppedBy(err, len(batch))
				rt.failed.Add(uint64(n))
				log.Error("sink write", "sink", rt.Name, "records", len(batch), "dropped", n, "err", err)
			}
		})
		o.routes = append(o.routes, rt)
	}
	return o
}

// emit hands one call's records to every route that takes their kind.
func (o *output) emit(recs ...Record) {
	for _, r := range o.routes {
		var dropped int
		if len(r.Kinds) == 0 {
			dropped = r.q.Put(recs...)
		} else {
			var buf [16]Record
			sel := buf[:0]
			for _, rec := range recs {
				if slices.Contains(r.Kinds, rec.Kind) {
					sel = append(sel, rec)
				}
			}
			if len(sel) > 0 {
				dropped = r.q.Put(sel...)
			}
		}
		if dropped > 0 {
			// Log when the running count crosses a power of two: often at first, then rarely.
			if n := r.q.Dropped(); bits.Len64(n-uint64(dropped)) != bits.Len64(n) {
				o.log.Warn("output queue full; records dropped", "sink", r.Name, "dropped", n)
			}
		}
	}
}

// close drains every queue, then closes the sinks. Nothing may emit after it starts.
func (o *output) close() {
	for _, r := range o.routes {
		r.q.Close()
		if err := r.Sink.Close(); err != nil {
			o.log.Error("close sink", "sink", r.Name, "err", err)
		}
	}
}

// stats reports the records dropped per route: refused by a full queue, or reported dropped by
// the sink.
func (o *output) stats() map[string]uint64 {
	out := make(map[string]uint64, len(o.routes))
	for _, r := range o.routes {
		out[r.Name] = r.q.Dropped() + r.failed.Load()
	}
	return out
}
