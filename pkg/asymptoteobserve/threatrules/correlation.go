package threatrules

import (
	"time"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// evaluateCorrelation runs the ordered-window state machine. Events are grouped by
// session.id (scope: session) and each group is evaluated independently; the rule
// matches if any session satisfies the sequence.
func (c *CompiledRule) evaluateCorrelation(events []asymptoteobserve.Event) (Verdict, error) {
	groups, order := groupBySession(events)
	for _, sid := range order {
		matched, err := c.matchSession(groups[sid])
		if err != nil {
			return "", err
		}
		if matched {
			return VerdictMatch, nil
		}
	}
	return VerdictNoMatch, nil
}

// matchSession reports whether one session's ordered events satisfy the step sequence
// within the window. Steps must occur in order; the elapsed time from the first matched
// step to the final matched step must not exceed the window. Timestamps are RFC3339; if a
// step's timestamp is absent the window is not enforced against it (positive fixtures need
// not carry timestamps, while window fixtures supply them).
func (c *CompiledRule) matchSession(events []asymptoteobserve.Event) (bool, error) {
	// Try every step-0 match as a candidate anchor. A single greedy pass is not enough:
	// an early anchor whose final step falls outside the window must not mask a later
	// anchor that completes in-window with the same downstream event.
	for start := range events {
		matched, err := EvalMatch(c.steps[0], events[start])
		if err != nil {
			return false, err
		}
		if !matched {
			continue
		}
		ok, err := c.completeFrom(events, start)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// completeFrom reports whether the remaining steps (1..final) can be matched in order on
// events after start, with the final step within the window of the anchor at start.
//
// Each subsequent step is matched at the earliest later event that satisfies it. Because
// every step must occur after the previous one, taking the earliest match minimizes the
// time of the final step, so if the greedy-earliest final is outside the window no other
// alignment from this anchor could do better — the anchor is abandoned and the caller
// tries the next one.
func (c *CompiledRule) completeFrom(events []asymptoteobserve.Event, start int) (bool, error) {
	startTime, startKnown := eventTime(events[start])
	final := len(c.steps) - 1
	stepIdx := 1
	for j := start + 1; j < len(events); j++ {
		matched, err := EvalMatch(c.steps[stepIdx], events[j])
		if err != nil {
			return false, err
		}
		if !matched {
			continue
		}
		if stepIdx == final {
			t, known := eventTime(events[j])
			if startKnown && known && t.Sub(startTime) > c.window {
				return false, nil
			}
			return true, nil
		}
		stepIdx++
	}
	return false, nil
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

// eventTime parses an event's RFC3339 timestamp. The bool is false when the timestamp is
// absent or unparseable, in which case window enforcement is skipped for that event.
func eventTime(e asymptoteobserve.Event) (time.Time, bool) {
	if e.Timestamp == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, e.Timestamp)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
