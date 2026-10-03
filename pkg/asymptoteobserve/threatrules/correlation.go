package threatrules

import (
	"sort"
	"time"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// evaluateCorrelation runs the windowed step matcher. Events are grouped by session.id
// (scope: session) and each group is evaluated independently; the rule matches if any
// session satisfies the steps.
func (c *CompiledRule) evaluateCorrelation(events []asymptoteobserve.Event) (Verdict, error) {
	groups, order := groupBySession(events)
	for _, sid := range order {
		matched, err := c.matchSession(groups[sid])
		if err != nil {
			return "", err
		}
		if matched != nil {
			return VerdictMatch, nil
		}
	}
	return VerdictNoMatch, nil
}

// matchSession returns the matched step events for the first alignment in one session
// that satisfies the steps within the window, or nil if none. It dispatches on the
// rule's order: sequence (the default) or any.
func (c *CompiledRule) matchSession(events []asymptoteobserve.Event) ([]asymptoteobserve.Event, error) {
	if c.anyOrder {
		return c.matchSessionAnyOrder(events)
	}
	return c.matchSessionInSequence(events)
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
func (c *CompiledRule) matchSessionInSequence(events []asymptoteobserve.Event) ([]asymptoteobserve.Event, error) {
	// Try every step-0 match as a candidate anchor. A single greedy pass is not enough:
	// an early anchor whose final step falls outside the window must not mask a later
	// anchor that completes in-window with the same downstream event.
	for start := range events {
		matched, err := c.eval(c.steps[0], events[start])
		if err != nil {
			return nil, err
		}
		if !matched {
			continue
		}
		seq, err := c.completeFrom(events, start)
		if err != nil {
			return nil, err
		}
		if seq != nil {
			return seq, nil
		}
	}
	return nil, nil
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
func (c *CompiledRule) completeFrom(events []asymptoteobserve.Event, start int) ([]asymptoteobserve.Event, error) {
	startTime, startKnown := eventTime(events[start])
	final := len(c.steps) - 1
	stepIdx := 1
	seq := make([]asymptoteobserve.Event, 1, len(c.steps))
	seq[0] = events[start]
	for j := start + 1; j < len(events); j++ {
		matched, err := c.eval(c.steps[stepIdx], events[j])
		if err != nil {
			return nil, err
		}
		if !matched {
			continue
		}
		seq = append(seq, events[j])
		if stepIdx == final {
			t, known := eventTime(events[j])
			if startKnown && known && t.Sub(startTime) > c.window {
				return nil, nil
			}
			return seq, nil
		}
		stepIdx++
	}
	return nil, nil
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
func (c *CompiledRule) matchSessionAnyOrder(events []asymptoteobserve.Event) ([]asymptoteobserve.Event, error) {
	// hits[s] lists, ascending, the indices of the events that satisfy step s.
	hits := make([][]int, len(c.steps))
	anchorable := make([]bool, len(events))
	for s, prog := range c.steps {
		for i := range events {
			matched, err := c.eval(prog, events[i])
			if err != nil {
				return nil, err
			}
			if matched {
				hits[s] = append(hits[s], i)
				anchorable[i] = true
			}
		}
		if len(hits[s]) == 0 {
			return nil, nil // some step never matches in this session
		}
	}
	for start := range events {
		if !anchorable[start] {
			continue
		}
		if picked := c.assignFrom(events, hits, start); picked != nil {
			sort.Ints(picked)
			seq := make([]asymptoteobserve.Event, len(picked))
			for k, i := range picked {
				seq[k] = events[i]
			}
			return seq, nil
		}
	}
	return nil, nil
}

// assignFrom assigns each step a distinct event at or after start and within the window
// of events[start], returning the chosen event indices (one per step, in step order), or
// nil when no complete assignment exists. It is Kuhn's augmenting-path matching, which is
// exact and cheap at the handful of steps a rule carries. The anchor is always part of the
// result: it is the earliest eligible hit of every step it satisfies, and augmenting only
// moves a step off an event when another step takes that event. So the window is measured
// from the anchor, and, as for a sequence, is not enforced when the anchor has no timestamp.
func (c *CompiledRule) assignFrom(events []asymptoteobserve.Event, hits [][]int, start int) []int {
	startTime, startKnown := eventTime(events[start])
	eligible := func(i int) bool {
		if !startKnown {
			return true
		}
		t, known := eventTime(events[i])
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

// groupBySession partitions events by session.id, preserving per-group input order and
// recording the order in which sessions first appear for deterministic evaluation.
func groupBySession(events []asymptoteobserve.Event) (map[string][]asymptoteobserve.Event, []string) {
	groups := make(map[string][]asymptoteobserve.Event)
	var order []string
	for i := range events {
		sid := ""
		if events[i].Session != nil {
			sid = events[i].Session.ID
		}
		if _, seen := groups[sid]; !seen {
			order = append(order, sid)
		}
		groups[sid] = append(groups[sid], events[i])
	}
	return groups, order
}

// eventTime parses an event's timestamp. The bool is false when the timestamp is absent
// or unparseable, in which case window enforcement is skipped for that event.
func eventTime(e asymptoteobserve.Event) (time.Time, bool) {
	if e.Timestamp == "" {
		return time.Time{}, false
	}
	t, err := asymptoteobserve.ParseTimestamp(e.Timestamp)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
