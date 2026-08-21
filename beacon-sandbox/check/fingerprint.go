package check

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Fingerprint is a compact summary of what a run was able to capture.
//
// Absolute thresholds answer "is this run acceptable"; a fingerprint answers "did we get
// worse". Those are different questions: a change can keep every expectation passing while
// quietly dropping a field that nothing asserts yet. Comparing fingerprints across runs makes
// that visible.
type Fingerprint struct {
	Scenario string  `json:"scenario"`
	Outcome  Outcome `json:"outcome"`
	// Actions counts events per event.action.
	Actions map[string]int `json:"actions"`
	// Harnesses counts events per harness.name. More than one entry for a single runtime is
	// the known OTLP-vs-hooks inconsistency.
	Harnesses map[string]int `json:"harnesses"`
	// PopulatedFields counts how many events carry each interesting field. This is the part
	// that catches silent regressions -- an action can keep firing while the field threat
	// rules match on goes empty.
	PopulatedFields map[string]int `json:"populated_fields"`
	// EventCount is the total number of lines.
	EventCount int `json:"event_count"`
	// QuiescenceSeconds is how long the log took to stop growing after the session, i.e.
	// telemetry arrival latency. Recorded because Beacon sets no explicit OTel export
	// interval, so this is the empirical answer to "how long must a checker wait".
	QuiescenceSeconds string `json:"quiescence_seconds,omitempty"`
}

// TrackedFields are the leaves worth trending. Deliberately weighted toward the scalar paths
// threat rules can actually match on (spec/threat-rules/FIELDS.md excludes e.raw.*), since
// those are where a silent capture regression does the most damage.
var TrackedFields = []string{
	"session.id",
	"harness.name",
	"prompt.text",
	"command.command",
	"command.exit_code",
	"command.duration_ms",
	"file.path",
	"file.operation",
	"tool.name",
	"tool.command",
	"approval.decision",
	"model",
	"repository",
	"branch",
	"trace.id",
	"gen_ai.usage.input_tokens",
	"gen_ai.usage.output_tokens",
	"gen_ai.usage.cost_usd",
}

// Fingerprint builds the summary for a verdict.
func (v Verdict) Fingerprint(log Log) Fingerprint {
	fp := Fingerprint{
		Scenario:          v.Scenario,
		Outcome:           v.Outcome,
		Actions:           log.ActionHistogram(),
		Harnesses:         log.HarnessNames(),
		PopulatedFields:   map[string]int{},
		EventCount:        len(log.Events),
		QuiescenceSeconds: v.Meta["quiescence_seconds"],
	}
	for _, f := range TrackedFields {
		n := 0
		for _, e := range log.Events {
			if e.ParseErr != nil {
				continue
			}
			if _, ok := e.Field(f); ok {
				n++
			}
		}
		// Record zeros too: "this field was never populated" is the finding, and omitting it
		// would make a regression from 5 to 0 look like a missing key rather than a loss.
		fp.PopulatedFields[f] = n
	}
	return fp
}

// WriteJSON persists a fingerprint.
func (f Fingerprint) WriteJSON(path string) error {
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// LoadFingerprint reads one back.
func LoadFingerprint(path string) (Fingerprint, error) {
	var f Fingerprint
	b, err := os.ReadFile(path)
	if err != nil {
		return f, err
	}
	return f, json.Unmarshal(b, &f)
}

// FingerprintDiff is the comparison between two runs of the same scenario.
type FingerprintDiff struct {
	Scenario string
	// Regressions are strictly-worse changes: a field or action that used to be captured
	// and now is not. These are the reason this exists.
	Regressions []string
	// Improvements are the inverse, worth showing so a fix is visibly confirmed.
	Improvements []string
	// Neutral covers count changes that are expected to vary run to run.
	Neutral []string
}

// HasRegression reports whether anything got worse.
func (d FingerprintDiff) HasRegression() bool { return len(d.Regressions) > 0 }

// CompareFingerprints diffs two runs.
//
// Only presence transitions are treated as regressions, not count changes: an agent that
// takes three turns instead of two legitimately produces different counts, and flagging that
// would drown the signal. Going from "captured at all" to "never captured" cannot be
// explained by model nondeterminism.
func CompareFingerprints(before, after Fingerprint) FingerprintDiff {
	d := FingerprintDiff{Scenario: after.Scenario}

	for _, f := range TrackedFields {
		b, a := before.PopulatedFields[f], after.PopulatedFields[f]
		switch {
		case b > 0 && a == 0:
			d.Regressions = append(d.Regressions,
				fmt.Sprintf("field %s no longer populated (was on %d event(s))", f, b))
		case b == 0 && a > 0:
			d.Improvements = append(d.Improvements,
				fmt.Sprintf("field %s now populated on %d event(s)", f, a))
		case b != a:
			d.Neutral = append(d.Neutral, fmt.Sprintf("field %s count %d -> %d", f, b, a))
		}
	}

	for _, action := range union(before.Actions, after.Actions) {
		b, a := before.Actions[action], after.Actions[action]
		switch {
		case b > 0 && a == 0:
			d.Regressions = append(d.Regressions,
				fmt.Sprintf("action %s no longer emitted (was %d time(s))", action, b))
		case b == 0 && a > 0:
			d.Improvements = append(d.Improvements,
				fmt.Sprintf("action %s now emitted %d time(s)", action, a))
		case b != a:
			d.Neutral = append(d.Neutral, fmt.Sprintf("action %s count %d -> %d", action, b, a))
		}
	}

	for _, h := range union(before.Harnesses, after.Harnesses) {
		if before.Harnesses[h] == 0 && after.Harnesses[h] > 0 {
			d.Improvements = append(d.Improvements, fmt.Sprintf("harness name %s appeared", h))
		}
		if before.Harnesses[h] > 0 && after.Harnesses[h] == 0 {
			d.Regressions = append(d.Regressions, fmt.Sprintf("harness name %s disappeared", h))
		}
	}

	if before.Outcome != after.Outcome {
		msg := fmt.Sprintf("outcome %s -> %s", before.Outcome, after.Outcome)
		switch {
		case before.Outcome == Pass && after.Outcome == Fail:
			d.Regressions = append(d.Regressions, msg)
		case before.Outcome == Fail && after.Outcome == Pass:
			d.Improvements = append(d.Improvements, msg)
		default:
			// One side is INCONCLUSIVE, which means the agent never did the work, so that
			// run measured nothing. A measurement cannot be compared against a
			// non-measurement in either direction: treating the transition as an
			// improvement previously made INCONCLUSIVE -> FAIL read as progress, and
			// PASS -> INCONCLUSIVE hide a lost signal.
			d.Neutral = append(d.Neutral, msg+" (not comparable: one run was inconclusive)")
		}
	}

	sort.Strings(d.Regressions)
	sort.Strings(d.Improvements)
	sort.Strings(d.Neutral)
	return d
}

// Describe renders the diff.
func (d FingerprintDiff) Describe() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", d.Scenario)
	if len(d.Regressions) == 0 && len(d.Improvements) == 0 {
		fmt.Fprintf(&b, "  no capability change\n")
	}
	for _, r := range d.Regressions {
		fmt.Fprintf(&b, "  REGRESSION  %s\n", r)
	}
	for _, i := range d.Improvements {
		fmt.Fprintf(&b, "  improved    %s\n", i)
	}
	for _, n := range d.Neutral {
		fmt.Fprintf(&b, "  (varies)    %s\n", n)
	}
	return b.String()
}

func union(a, b map[string]int) []string {
	seen := map[string]bool{}
	var out []string
	for k := range a {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	for k := range b {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
