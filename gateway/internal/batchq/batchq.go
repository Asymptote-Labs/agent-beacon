// Package batchq is a bounded queue drained in batches by one goroutine, built so producers on a
// latency-sensitive path almost never wake the consumer.
//
// A channel send to a parked goroutine wakes it, and a wake is an OS-level signal (on macOS a
// pthread condition variable) that costs microseconds of the sender's time. A pipeline that sent
// every record of every call on a channel spent most of its call latency waking its own output
// goroutines. Here a producer appends under a short lock and signals only when its append fills a
// batch; otherwise the consumer drains on a timer. Under load that is one wake per batch; at low
// rates, none: the timer drains.
package batchq

import (
	"sync"
	"sync/atomic"
	"time"
)

// Queue holds items until its consumer drains them.
type Queue[T any] struct {
	mu      sync.Mutex
	buf     []T
	spare   []T
	limit   int
	batch   int
	kick    chan struct{}
	stop    chan struct{}
	done    chan struct{}
	dropped atomic.Uint64
}

// New starts a queue holding at most limit items. flush receives the queued items in order, at
// least every interval, or as soon as batch of them are waiting; it must not keep the slice.
func New[T any](limit, batch int, interval time.Duration, flush func([]T)) *Queue[T] {
	q := &Queue[T]{
		limit: limit,
		batch: min(batch, limit),
		kick:  make(chan struct{}, 1),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	go q.run(interval, flush)
	return q
}

// Put enqueues items, dropping those that do not fit, and reports how many were dropped.
func (q *Queue[T]) Put(items ...T) int {
	q.mu.Lock()
	before := len(q.buf)
	n := min(len(items), q.limit-before)
	q.buf = append(q.buf, items[:n]...)
	after := len(q.buf)
	q.mu.Unlock()
	if before < q.batch && after >= q.batch {
		select {
		case q.kick <- struct{}{}:
		default:
		}
	}
	dropped := len(items) - n
	if dropped > 0 {
		q.dropped.Add(uint64(dropped))
	}
	return dropped
}

// Dropped reports the items dropped because the queue was full.
func (q *Queue[T]) Dropped() uint64 { return q.dropped.Load() }

// Close drains what is queued and stops the consumer. Nothing may Put after Close starts.
func (q *Queue[T]) Close() {
	close(q.stop)
	<-q.done
}

func (q *Queue[T]) run(interval time.Duration, flush func([]T)) {
	defer close(q.done)
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-q.kick:
		case <-tick.C:
		case <-q.stop:
			q.drain(flush)
			return
		}
		q.drain(flush)
	}
}

// drain hands everything queued to flush, swapping in a spare buffer so producers keep appending
// while flush runs.
func (q *Queue[T]) drain(flush func([]T)) {
	q.mu.Lock()
	items := q.buf
	q.buf = q.spare[:0]
	q.mu.Unlock()
	if len(items) > 0 {
		flush(items)
	}
	clear(items) // let flushed items be collected while the slice waits to be reused
	q.spare = items[:0]
}
