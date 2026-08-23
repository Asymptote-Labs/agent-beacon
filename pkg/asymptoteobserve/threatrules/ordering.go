package threatrules

import (
	"sort"
	"time"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// SortEvents puts a raw event stream into the order the events actually happened, in
// place, so correlation stops inheriting the order the lines happened to land in the log.
//
// Append order is not emission order. The hook adapter writes synchronously the moment a
// tool call is intercepted, while the collector exporter writes on its export interval,
// so a hook event for 10:00:01 routinely lands in the file after a collector event for
// 10:00:06. The rules engine evaluates an ordered sequence by contract (see
// spec/threat-rules/SPEC.md), which makes putting the stream in order the caller's job:
// every ordered-window rule silently misses when it is handed append order instead.
//
// The key is (timestamp, sequence, input position):
//
//   - Timestamp first, because it is the only field both writers stamp from the same
//     clock and is therefore the one thing comparable across capture paths.
//   - Sequence second, but only when both events carry one, since it orders a single
//     writer's stream rather than the log as a whole (see asymptoteobserve.Event.Sequence).
//     It is what separates a batch of metric datapoints that all carry one export's
//     collection instant.
//   - Input position last, so the result is a total order and one log always sorts one
//     way, rather than depending on the sort implementation.
//
// An event whose timestamp is absent or unparseable inherits the last timestamp seen
// before it. That keeps it beside the events it arrived with instead of collapsing every
// such event to the epoch and dragging it to the front of the session.
func SortEvents(events []asymptoteobserve.Event) {
	if len(events) < 2 {
		return
	}
	type keyed struct {
		event asymptoteobserve.Event
		at    time.Time
		seq   uint64
		idx   int
	}
	keys := make([]keyed, len(events))
	var carried time.Time
	for i := range events {
		if at, ok := eventTime(events[i]); ok {
			carried = at
		}
		keys[i] = keyed{event: events[i], at: carried, seq: events[i].Sequence, idx: i}
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if !a.at.Equal(b.at) {
			return a.at.Before(b.at)
		}
		if a.seq != 0 && b.seq != 0 && a.seq != b.seq {
			return a.seq < b.seq
		}
		return a.idx < b.idx
	})
	for i := range keys {
		events[i] = keys[i].event
	}
}
