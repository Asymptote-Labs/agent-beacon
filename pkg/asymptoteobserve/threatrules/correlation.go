package threatrules

import (
	"sort"
	"time"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// sessionEvents are the events of one session, in the order they were scanned: the scanned
// events, and the position among them of each event of the session.
type sessionEvents struct {
	all []asymptoteobserve.Event
	at  []int
}

// event returns the session's k-th event.
func (s *sessionEvents) event(k int) *asymptoteobserve.Event { return &s.all[s.at[k]] }

// groupSessions returns the events of each session (session.id), in the order the sessions
// first appear, and the number of each event's session.
func groupSessions(events []asymptoteobserve.Event) (sessions []sessionEvents, of []int) {
	at := map[string]int{}
	of = make([]int, len(events))
	for i := range events {
		sid := sessionOf(&events[i])
		k, seen := at[sid]
		if !seen {
			k = len(sessions)
			at[sid] = k
			sessions = append(sessions, sessionEvents{all: events})
		}
		sessions[k].at = append(sessions[k].at, i)
		of[i] = k
	}
	return sessions, of
}

// sessionOf returns an event's session id, "" for an event of no session.
func sessionOf(e *asymptoteobserve.Event) string {
	if e.Session == nil {
		return ""
	}
	return e.Session.ID
}

// stepEval reports whether a step of a rule holds for an event of the session being matched,
// each by its index. A scan answers from what it worked out as it passed over the events
// (stepTable), and decides there what a step that could not be evaluated means.
type stepEval func(step, event int) bool

// matchSession returns the matched step events for the first alignment in one session
// that satisfies the steps within the window, or nil if none. It dispatches on the
// rule's order: sequence (the default) or any.
func (c *CompiledRule) matchSession(s *sessionEvents, holds stepEval) []asymptoteobserve.Event {
	if c.anyOrder {
		return c.matchSessionAnyOrder(s, holds)
	}
	return c.matchSessionInSequence(s, holds)
}

// matchSessionInSequence returns the matched step events (one per step, in step order)
// for the first alignment in one session that satisfies the sequence within the window,
// or nil if none.
//
// Steps must occur in order; the elapsed time from the first matched step to the final
// matched step must not exceed the window. "In order" means the order of the slice, which
// the caller is responsible for (see SortEvents); if a step's timestamp is absent the
// window is not enforced against it (positive fixtures need not carry timestamps, while
// window fixtures supply them).
func (c *CompiledRule) matchSessionInSequence(s *sessionEvents, holds stepEval) []asymptoteobserve.Event {
	// Try every step-0 match as a candidate anchor. A single greedy pass is not enough:
	// an early anchor whose final step falls outside the window must not mask a later
	// anchor that completes in-window with the same downstream event.
	//
	// The cursors are shared by every anchor, so each later step's expression runs at most
	// once per event in the session. Without them a session with many step-0 matches
	// re-evaluated every later event once per anchor, quadratic in the session (8,000
	// credential reads took over half a minute for one rule).
	cursors := make([]stepCursor, len(c.steps))
	for start := range s.at {
		if !holds(0, start) {
			continue
		}
		if seq := c.completeFrom(s, start, cursors, holds); seq != nil {
			return seq
		}
	}
	return nil
}

// completeFrom returns the matched step events (including the anchor at start) if the
// remaining steps (1..final) can be matched in order on events after start, with the
// final step within the window of the anchor; otherwise nil.
//
// Each subsequent step is matched at the earliest later event that satisfies it. Because
// every step must occur after the previous one, taking the earliest match minimizes the
// time of the final step, so if the greedy-earliest final is outside the window no other
// alignment from this anchor could do better — the anchor is abandoned and the caller
// tries the next one.
func (c *CompiledRule) completeFrom(s *sessionEvents, start int, cursors []stepCursor, holds stepEval) []asymptoteobserve.Event {
	seq := make([]asymptoteobserve.Event, 1, len(c.steps))
	seq[0] = *s.event(start)
	prev := start
	for step := 1; step < len(c.steps); step++ {
		j := c.nextHit(&cursors[step], step, s, prev, holds)
		if j < 0 {
			return nil
		}
		seq = append(seq, *s.event(j))
		prev = j
	}
	startTime, startKnown := eventTime(s.event(start))
	t, known := eventTime(s.event(prev))
	if startKnown && known && t.Sub(startTime) > c.window {
		return nil
	}
	return seq
}

// stepCursor records how far one sequence step has been evaluated across the anchors of
// one session.
//
// Anchors are tried in order, and the greedy chain from a later anchor sits, step by
// step, at or after the chain from an earlier one: the earliest hit after a later position
// is never earlier. So the positions each step is asked about only move forward, and one
// pass over the session per step answers every anchor. The zero value is a cursor that
// has evaluated nothing.
type stepCursor struct {
	scanned int // events before this index have been evaluated against the step
	hit     int // 1 + the index of the last hit found, or 0 for none
}

// nextHit returns the index of the earliest event after `after` that satisfies step s, or
// -1 when none does. Successive calls on one cursor must pass a non-decreasing `after`.
func (c *CompiledRule) nextHit(cur *stepCursor, step int, s *sessionEvents, after int, holds stepEval) int {
	if hit := cur.hit - 1; hit > after {
		// The query that found it started at or before after and saw no earlier hit, so
		// there is none between after and it either.
		return hit
	}
	j := after + 1
	if cur.scanned > j {
		// Only after an exhausted scan, which found nothing from an earlier position on.
		j = cur.scanned
	}
	for ; j < len(s.at); j++ {
		if holds(step, j) {
			cur.scanned, cur.hit = j+1, j+1
			return j
		}
	}
	cur.scanned, cur.hit = len(s.at), 0
	return -1
}

// matchSessionAnyOrder returns the matched events, in the order they occurred, for the
// first set of distinct events in one session that satisfies every step within the
// window regardless of which step's event came first, or nil if none.
//
// The window is measured from the earliest matched event, as it is for a sequence: every
// matched event must fall within window of that anchor. Each event is evaluated against
// each step once, up front. Then every event that satisfies some step is tried as the
// anchor, and the steps are assigned distinct events at or after it by bipartite
// matching, so one event never satisfies two steps and a step whose only candidate is
// already taken can displace the earlier choice onto an alternative.
func (c *CompiledRule) matchSessionAnyOrder(s *sessionEvents, holds stepEval) []asymptoteobserve.Event {
	// hits[step] lists, ascending, the indices of the events that satisfy the step.
	hits := make([][]int, len(c.steps))
	anchorable := make([]bool, len(s.at))
	for step := range c.steps {
		for k := range s.at {
			if holds(step, k) {
				hits[step] = append(hits[step], k)
				anchorable[k] = true
			}
		}
		if len(hits[step]) == 0 {
			return nil // some step never matches in this session
		}
	}
	for start := range s.at {
		if !anchorable[start] {
			continue
		}
		if picked := c.assignFrom(s, hits, start); picked != nil {
			sort.Ints(picked)
			seq := make([]asymptoteobserve.Event, len(picked))
			for k, i := range picked {
				seq[k] = *s.event(i)
			}
			return seq
		}
	}
	return nil
}

// assignFrom assigns each step a distinct event at or after start and within the window
// of the session's event start, returning the chosen event indices (one per step, in step
// order), or nil when no complete assignment exists. It is Kuhn's augmenting-path matching,
// which is exact and cheap at the handful of steps a rule carries. The anchor is always part
// of the result: it is the earliest eligible hit of every step it satisfies, and augmenting
// only moves a step off an event when another step takes that event. So the window is
// measured from the anchor, and, as for a sequence, is not enforced when the anchor has no
// timestamp.
func (c *CompiledRule) assignFrom(s *sessionEvents, hits [][]int, start int) []int {
	startTime, startKnown := eventTime(s.event(start))
	eligible := func(i int) bool {
		if !startKnown {
			return true
		}
		t, known := eventTime(s.event(i))
		return !known || t.Sub(startTime) <= c.window
	}
	owner := make(map[int]int, len(c.steps)) // event index -> step holding it
	picked := make([]int, len(c.steps))
	var visited map[int]bool
	var augment func(step int) bool
	augment = func(step int) bool {
		for _, i := range hits[step][sort.SearchInts(hits[step], start):] {
			if !eligible(i) {
				break // input is chronological: every later hit is further out
			}
			if visited[i] {
				continue
			}
			visited[i] = true
			if prev, taken := owner[i]; !taken || augment(prev) {
				owner[i] = step
				picked[step] = i
				return true
			}
		}
		return false
	}
	for step := range c.steps {
		visited = make(map[int]bool)
		if !augment(step) {
			return nil
		}
	}
	return picked
}


// eventTime parses an event's timestamp. The bool is false when the timestamp is absent
// or unparseable, in which case window enforcement is skipped for that event.
func eventTime(e *asymptoteobserve.Event) (time.Time, bool) {
	if e.Timestamp == "" {
		return time.Time{}, false
	}
	t, err := asymptoteobserve.ParseTimestamp(e.Timestamp)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
