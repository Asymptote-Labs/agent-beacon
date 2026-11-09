package main

import (
	"math/bits"
	"sync/atomic"
	"time"
)

// Latency histogram: HDR-style log-linear buckets. Values below 2^subBits ns are exact; above,
// every power of two splits into 2^subBits linear sub-buckets, so a bucket is at most 1/16 (6.25%)
// wide relative to its value. Buckets are atomic so a shard can be written by its workers and read
// by the 1s timeline without locks; shards are merged for reporting.
const (
	subBits  = 4
	subCount = 1 << subBits
	maxExp   = 40 // 2^40 ns ≈ 18 minutes; larger values land in the last bucket
	nBuckets = (maxExp - subBits + 2) * subCount
)

func bucketOf(ns int64) int {
	if ns < 1 {
		ns = 1
	}
	v := uint64(ns)
	e := bits.Len64(v) - 1
	if e < subBits {
		return int(v)
	}
	i := (e-subBits+1)<<subBits + int((v>>(e-subBits))&(subCount-1))
	return min(i, nBuckets-1)
}

// bucketValue is the midpoint of bucket i in ns.
func bucketValue(i int) int64 {
	if i < subCount {
		return int64(i)
	}
	e := i>>subBits + subBits - 1
	sub := uint64(i & (subCount - 1))
	lo := (1<<e | sub<<(e-subBits))
	width := uint64(1) << (e - subBits)
	return int64(lo + width/2)
}

// hist is one shard's histogram.
type hist struct {
	b   [nBuckets]atomic.Uint64
	max atomic.Int64
	sum atomic.Int64
}

func (h *hist) record(d time.Duration) {
	ns := int64(d)
	h.b[bucketOf(ns)].Add(1)
	h.sum.Add(ns)
	for {
		m := h.max.Load()
		if ns <= m || h.max.CompareAndSwap(m, ns) {
			return
		}
	}
}

// snap is a merged, plain histogram.
type snap struct {
	b     [nBuckets]uint64
	n     uint64
	max   int64
	sum   int64
	valid bool
}

func (s *snap) add(h *hist) {
	for i := range h.b {
		v := h.b[i].Load()
		s.b[i] += v
		s.n += v
	}
	s.max = max(s.max, h.max.Load())
	s.sum += h.sum.Load()
	s.valid = true
}

// minus is s with earlier's counts removed (an interval); max stays the running max.
func (s *snap) minus(earlier *snap) snap {
	out := *s
	if earlier == nil || !earlier.valid {
		return out
	}
	out.n = 0
	for i := range out.b {
		out.b[i] -= earlier.b[i]
		out.n += out.b[i]
	}
	out.sum -= earlier.sum
	return out
}

func (s *snap) quantile(q float64) time.Duration {
	if s.n == 0 {
		return 0
	}
	rank := uint64(q*float64(s.n) + 0.5)
	rank = max(1, min(rank, s.n))
	var c uint64
	for i, v := range s.b {
		c += v
		if c >= rank {
			return time.Duration(min(bucketValue(i), s.max))
		}
	}
	return time.Duration(s.max)
}

func (s *snap) mean() time.Duration {
	if s.n == 0 {
		return 0
	}
	return time.Duration(s.sum / int64(s.n))
}
