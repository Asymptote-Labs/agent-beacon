package writer

import (
	"errors"
	"sync"
)

// ErrBudgetSpent is returned by an append whose line would take its Budget past the limit. Nothing
// is written when it is returned.
var ErrBudgetSpent = errors.New("the byte budget for this backfill is spent")

// Budget caps the bytes one bounded run of appends may add to the runtime log. The install-time
// session backfill uses it so a machine with years of agent history fills a bounded part of the
// log's rotation window instead of rotating out everything else it holds, live telemetry included.
//
// A collector that hits the limit stops where it is. Its cursor has advanced only past what was
// written, so a later sync carries on from there. The zero value has no room; use NewBudget. A
// Budget is safe for concurrent use.
type Budget struct {
	mu      sync.Mutex
	limit   int64
	used    int64
	refused bool
	parent  *Budget
}

// NewBudget returns a budget of limit bytes.
func NewBudget(limit int64) *Budget {
	return &Budget{limit: limit}
}

// Sub returns a budget of at most limit bytes whose appends also count against b, so one share of a
// shared budget cannot take all of it.
func (b *Budget) Sub(limit int64) *Budget {
	return &Budget{limit: limit, parent: b}
}

// Used is the number of bytes written against the budget.
func (b *Budget) Used() int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}

// Remaining is the number of bytes still available.
func (b *Budget) Remaining() int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.used >= b.limit {
		return 0
	}
	return b.limit - b.used
}

// Refused reports whether an append was turned away because the budget had no room for it.
func (b *Budget) Refused() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.refused
}

// reserve takes n bytes if they fit in b and in every budget above it.
func (b *Budget) reserve(n int64) bool {
	b.mu.Lock()
	if b.used+n > b.limit {
		b.refused = true
		b.mu.Unlock()
		return false
	}
	b.used += n
	b.mu.Unlock()
	if b.parent != nil && !b.parent.reserve(n) {
		b.mu.Lock()
		b.used -= n
		b.refused = true
		b.mu.Unlock()
		return false
	}
	return true
}

// release returns n bytes reserved for an append that did not happen.
func (b *Budget) release(n int64) {
	b.mu.Lock()
	b.used -= n
	b.mu.Unlock()
	if b.parent != nil {
		b.parent.release(n)
	}
}
