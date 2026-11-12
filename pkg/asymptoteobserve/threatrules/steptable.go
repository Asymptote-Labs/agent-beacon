package threatrules

import (
	"slices"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// stepTable holds, for the correlation rules of one scan, whether each of their steps holds for
// each event. It is filled while the scan passes over the events, from the activation the
// single-event rules read the event through, so an event's derived fields are worked out once
// for the whole scan rather than once for every step that is asked about the event, and a step
// several rules share (the credential read of the read-then-egress rules) is evaluated once.
//
// A rule's matcher then asks the table instead of evaluating (see stepEval), in the order it
// always did, so which events match, which evidence a finding carries and which error a scan
// returns are what each rule finds on its own: an evaluation that fails is recorded with its
// error, and the error is returned only when the matcher asks for that step on that event.
//
// Only what a matcher can ask is evaluated ahead of it: the first step of a sequence and every
// step of an order: any rule on every event, a later step of a sequence on the events that
// follow, in their session, one the step before it holds for (nextHit looks nowhere else).
// That is a matter of cost alone: what the table does not hold is evaluated when it is asked.
type stepTable struct {
	events    []asymptoteobserve.Event
	columns   []stepColumn
	of        map[*CompiledRule][]int // a rule's steps, as columns
	byAction  map[string][]int        // as EventScanner's, over columns
	anyAction []int
	// known and hit are one bit per event for each column: the step was evaluated on the event,
	// and it held. failed holds the evaluations that returned an error.
	known, hit [][]uint64
	failed     map[[2]int]error // (column, event)
	// held lists, by session, the feeding columns that have held for an earlier event.
	held map[string][]bool
	// sessions are the events of each session, in the order the sessions first appear.
	sessions []sessionEvents
}

// stepColumn is one distinct step: an expression, read on one form of the event.
type stepColumn struct {
	match   matcher
	derived bool
	// always is set when the step is asked on every event; otherwise it is asked on the events
	// after one that a column in after holds for. feeds is set on a column listed in an after.
	always bool
	after  []int
	feeds  bool
}

// sessionEvents are the events of one session and where each is in the scanned sequence.
type sessionEvents struct {
	events []asymptoteobserve.Event
	index  []int
}

// stepKey identifies a step by what is evaluated: the expression and the definitions it was
// compiled with.
type stepKey struct {
	lib   *Library
	match string
}

// newStepTable returns the table for the correlation rules among rules over events, or nil when
// there are none.
func newStepTable(rules []*CompiledRule, events []asymptoteobserve.Event) *stepTable {
	t := &stepTable{events: events, of: map[*CompiledRule][]int{}, byAction: map[string][]int{}}
	columns := map[stepKey]int{}
	for _, r := range rules {
		if r.steps == nil {
			continue
		}
		if _, done := t.of[r]; done {
			continue
		}
		own := make([]int, len(r.steps))
		for s, step := range r.rule.Correlation.Steps {
			// One column for a step however many rules write it. Which form of the event a
			// step reads follows its rule (CompiledRule.eval); a step shared by a rule that
			// reads derived fields and one that does not selects none itself, so it is the
			// same on either form and reads the derived one.
			key := stepKey{lib: r.rule.lib, match: step.Match}
			c, ok := columns[key]
			if !ok {
				c = len(t.columns)
				columns[key] = c
				t.columns = append(t.columns, stepColumn{match: r.steps[s]})
			}
			t.columns[c].derived = t.columns[c].derived || r.derived
			own[s] = c
			switch {
			case r.anyOrder || s == 0:
				t.columns[c].always = true
			case !slices.Contains(t.columns[c].after, own[s-1]):
				t.columns[c].after = append(t.columns[c].after, own[s-1])
				t.columns[own[s-1]].feeds = true
			}
		}
		t.of[r] = own
	}
	if len(t.columns) == 0 {
		return nil
	}
	for c, col := range t.columns {
		if col.match.actions == nil {
			t.anyAction = append(t.anyAction, c)
		}
	}
	for c, col := range t.columns {
		for action := range col.match.actions {
			if _, ok := t.byAction[action]; !ok {
				t.byAction[action] = slices.Clone(t.anyAction)
			}
			t.byAction[action] = append(t.byAction[action], c)
		}
	}
	for _, cs := range t.byAction {
		slices.Sort(cs)
	}
	words := (len(events) + 63) / 64
	t.known, t.hit = make([][]uint64, len(t.columns)), make([][]uint64, len(t.columns))
	for c := range t.columns {
		t.known[c], t.hit[c] = make([]uint64, words), make([]uint64, words)
	}
	return t
}

func sessionID(e *asymptoteobserve.Event) string {
	if e.Session == nil {
		return ""
	}
	return e.Session.ID
}

// fill evaluates on event i, read through in, the steps a matcher can ask about it.
func (t *stepTable) fill(i int, in *eventInput) {
	candidates, ok := t.byAction[in.event.Event.Action]
	if !ok {
		candidates = t.anyAction
	}
	if len(candidates) == 0 {
		return
	}
	sid := sessionID(in.event)
	held := t.held[sid] // as it was before this event
	var fed []int
	for _, c := range candidates {
		col := &t.columns[c]
		if !col.always && !slices.ContainsFunc(col.after, func(before int) bool { return held != nil && held[before] }) {
			continue
		}
		if t.record(c, i, in) && col.feeds {
			fed = append(fed, c)
		}
	}
	if len(fed) == 0 {
		return
	}
	if held == nil {
		if t.held == nil {
			t.held = map[string][]bool{}
		}
		held = make([]bool, len(t.columns))
		t.held[sid] = held
	}
	for _, c := range fed {
		held[c] = true
	}
}

// record evaluates column c on event i and reports whether it held.
func (t *stepTable) record(c, i int, in *eventInput) bool {
	col := &t.columns[c]
	act := in.activation(col.derived)
	matched, err := evalObserved(col.match.program(act), act)
	word, bit := i/64, uint64(1)<<(i%64)
	t.known[c][word] |= bit
	if err != nil {
		if t.failed == nil {
			t.failed = map[[2]int]error{}
		}
		t.failed[[2]int{c, i}] = err
		return false
	}
	if matched {
		t.hit[c][word] |= bit
	}
	return matched
}

// holds reports whether column c holds for event i, as evaluating the step on the event does.
func (t *stepTable) holds(c, i int) (bool, error) {
	word, bit := i/64, uint64(1)<<(i%64)
	if t.known[c][word]&bit == 0 {
		if t.columns[c].match.excludes(t.events[i].Event.Action) {
			return false, nil
		}
		t.record(c, i, &eventInput{event: &t.events[i]})
	}
	if t.hit[c][word]&bit != 0 {
		return true, nil
	}
	if t.failed != nil {
		if err, failed := t.failed[[2]int{c, i}]; failed {
			return false, err
		}
	}
	return false, nil
}

// findings returns what rule.correlationFindings returns for the scanned events, asking the
// table for every step.
func (t *stepTable) findings(rule *CompiledRule) ([]Finding, error) {
	if t.sessions == nil {
		at := map[string]int{}
		for i := range t.events {
			sid := sessionID(&t.events[i])
			k, seen := at[sid]
			if !seen {
				k = len(t.sessions)
				at[sid] = k
				t.sessions = append(t.sessions, sessionEvents{})
			}
			t.sessions[k].events = append(t.sessions[k].events, t.events[i])
			t.sessions[k].index = append(t.sessions[k].index, i)
		}
	}
	columns := t.of[rule]
	var findings []Finding
	for k := range t.sessions {
		session := &t.sessions[k]
		seq, err := rule.matchSession(session.events, func(step, event int) (bool, error) {
			return t.holds(columns[step], session.index[event])
		})
		if err != nil {
			return nil, err
		}
		if seq != nil {
			findings = append(findings, rule.newFinding(seq))
		}
	}
	return findings, nil
}
