package check

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Outcome is the verdict for a run.
//
// INCONCLUSIVE is the load-bearing one: it separates "Beacon missed it" from "the agent
// never did it". Without that distinction a model that declines a prompt looks identical to
// a capture bug, and the suite becomes untrustworthy.
type Outcome string

const (
	Pass         Outcome = "PASS"
	Fail         Outcome = "FAIL"
	Inconclusive Outcome = "INCONCLUSIVE"
)

// Severity distinguishes a real failure from a recorded-but-tolerated gap.
type Severity string

const (
	SevFail Severity = "fail"
	SevWarn Severity = "warn"
	SevInfo Severity = "info"
)

// Finding is one thing worth a human's attention, with evidence attached.
type Finding struct {
	Check    string   `json:"check"`
	Severity Severity `json:"severity"`
	Summary  string   `json:"summary"`
	// Why carries the scenario author's rationale, so a failure explains what broke.
	Why string `json:"why,omitempty"`
	// Evidence points at exact artifact locations, e.g. "runtime.jsonl:88".
	Evidence []string `json:"evidence,omitempty"`
}

// Verdict is the whole machine-readable result. Kept small on purpose: an agent or a human
// should be able to read this without wading through logs, and drill in via Evidence.
type Verdict struct {
	Scenario   string         `json:"scenario"`
	Outcome    Outcome        `json:"outcome"`
	Reason     string         `json:"reason,omitempty"`
	Findings   []Finding      `json:"findings,omitempty"`
	Histogram  map[string]int `json:"action_histogram,omitempty"`
	Harnesses  map[string]int `json:"harness_names,omitempty"`
	EventCount int            `json:"event_count"`
	// Meta records what produced this verdict, for reproducibility.
	Meta map[string]string `json:"meta,omitempty"`
}

// Add appends a finding.
func (v *Verdict) Add(f Finding) { v.Findings = append(v.Findings, f) }

// Resolve computes the outcome from the findings. Any fail-severity finding fails the run.
func (v *Verdict) Resolve() {
	// A fail-severity finding always wins, including over INCONCLUSIVE.
	//
	// Resolve used to return early on INCONCLUSIVE, which meant an inactive agent suppressed
	// every hard failure alongside it: a leaked credential, a corrupt line, a host escape, or
	// an argv disclosure all exited as inconclusive-and-therefore-not-a-failure. Those
	// findings are invariants and safety properties -- they do not depend on the agent having
	// done the scenario's work, so an idle agent is no reason to stop reporting them.
	// INCONCLUSIVE only means "capture cannot be judged"; it never means "nothing is wrong".
	for _, f := range v.Findings {
		if f.Severity == SevFail {
			// Overriding INCONCLUSIVE has to take its reason with it. Leaving "the agent did not
			// perform the requested work" attached to a FAIL misattributes the failure to an idle
			// model when something independent actually broke, which would send a reader off to
			// retry instead of to the finding.
			if v.Outcome == Inconclusive {
				v.Reason = "the agent did not perform the requested work, but independent " +
					"failures were found regardless -- see the findings below, and do not " +
					"dismiss this as a retry"
			}
			v.Outcome = Fail
			return
		}
	}
	if v.Outcome == Inconclusive {
		return
	}
	v.Outcome = Pass
}

// Failures returns just the fail-severity findings.
func (v Verdict) Failures() []Finding {
	var out []Finding
	for _, f := range v.Findings {
		if f.Severity == SevFail {
			out = append(out, f)
		}
	}
	return out
}

// WriteJSON persists the verdict.
func (v Verdict) WriteJSON(path string) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// Report renders a compact human-readable summary.
func (v Verdict) Report() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s", v.Outcome, v.Scenario)
	if v.Reason != "" {
		fmt.Fprintf(&b, "  (%s)", v.Reason)
	}
	fmt.Fprintf(&b, "\n  %d events captured\n", v.EventCount)

	if len(v.Histogram) > 0 {
		fmt.Fprintf(&b, "  actions: %s\n", sortedCounts(v.Histogram))
	}
	if len(v.Harnesses) > 0 {
		fmt.Fprintf(&b, "  harness names: %s\n", sortedCounts(v.Harnesses))
		// Only flag multiple *runtime* harness names. Beacon's own lifecycle events carry
		// harness.name "endpoint", so counting those would report the inconsistency on every
		// install scenario and train the reader to ignore it.
		if runtimes := runtimeHarnessNames(v.Harnesses); len(runtimes) > 1 {
			fmt.Fprintf(&b, "  note: %d runtime harness names for one session (%s) -- the OTLP "+
				"path normalizes to claude_code while hooks emit the raw --platform value\n",
				len(runtimes), strings.Join(runtimes, ", "))
		}
	}

	for _, sev := range []Severity{SevFail, SevWarn, SevInfo} {
		for _, f := range v.Findings {
			if f.Severity != sev {
				continue
			}
			fmt.Fprintf(&b, "  [%s] %s: %s\n", strings.ToUpper(string(sev)), f.Check, f.Summary)
			if f.Why != "" {
				fmt.Fprintf(&b, "        why: %s\n", f.Why)
			}
			for _, e := range f.Evidence {
				fmt.Fprintf(&b, "        at: %s\n", e)
			}
		}
	}
	return b.String()
}

// beaconOwnHarnessNames are harness.name values Beacon itself emits for its own lifecycle
// events, as opposed to an agent runtime it observed.
var beaconOwnHarnessNames = map[string]bool{
	"endpoint": true, "beacon": true, "test_harness": true, "": true,
}

func runtimeHarnessNames(m map[string]int) []string {
	var out []string
	for name := range m {
		if !beaconOwnHarnessNames[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func sortedCounts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] != m[keys[j]] {
			return m[keys[i]] > m[keys[j]]
		}
		return keys[i] < keys[j]
	})
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	return strings.Join(parts, " ")
}
