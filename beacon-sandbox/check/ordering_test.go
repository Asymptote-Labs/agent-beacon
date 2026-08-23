package check

import (
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/scenario"
)

// orderingScenario expects only the event these tests emit, so the outcome reflects the
// ordering invariants rather than unmet capture expectations.
func orderingScenario() scenario.Scenario {
	sc := demoScenario()
	sc.Sentinel = ""
	sc.Expect = []scenario.Expect{{Action: "session.activity", Why: "baseline"}}
	return sc
}

// activity builds a session.activity event at ts, optionally carrying a sequence.
func activity(ts string, sequence string) string {
	line := `{"timestamp":"` + ts + `",`
	if sequence != "" {
		line += `"sequence":` + sequence + `,`
	}
	return line + `"vendor":"beacon","product":"endpoint-agent","schema_version":"1.0",` +
		`"event":{"kind":"agent_runtime","action":"session.activity","category":"session"},` +
		`"severity":"info","endpoint":{"os":"linux","hostname":"sandbox"},` +
		`"harness":{"name":"claude_code"},"session":{"id":"s1"},"message":"m"}`
}

func TestSecondResolutionLogFailsPrecision(t *testing.T) {
	// The measured regression: not one event in the log carries a sub-second timestamp, so
	// nothing downstream can order two events in the same second.
	v := run(t, []string{
		activity("2026-08-02T18:00:00Z", ""),
		activity("2026-08-02T18:00:01Z", ""),
	}, orderingScenario(), Sentinel{}, nil)

	if !hasCheck(v, "invariant.timestamp_precision") {
		t.Fatalf("a log with no sub-second timestamp must be reported:\n%s", v.Report())
	}
	if v.Outcome != Fail {
		t.Fatalf("outcome = %v, want Fail:\n%s", v.Outcome, v.Report())
	}
}

func TestMixedPrecisionLogWarnsWithoutFailing(t *testing.T) {
	// A log written across an agent upgrade holds both forms. That is expected, so it is
	// worth saying and not worth failing.
	v := run(t, []string{
		activity("2026-08-02T18:00:00Z", ""),
		activity("2026-08-02T18:00:01.500000000Z", ""),
	}, orderingScenario(), Sentinel{}, nil)

	if !hasCheck(v, "invariant.timestamp_precision") {
		t.Fatalf("a partly second-resolution log must be reported:\n%s", v.Report())
	}
	if v.Outcome == Fail {
		t.Fatalf("a mixed-precision log is a warning, not a failure:\n%s", v.Report())
	}
	if !strings.Contains(v.Report(), "second-resolution timestamp") {
		t.Fatalf("report should name what is second-resolution:\n%s", v.Report())
	}
}

func TestCanonicalLogReportsNoOrderingFinding(t *testing.T) {
	v := run(t, []string{
		activity("2026-08-02T18:00:00.000000000Z", "1"),
		activity("2026-08-02T18:00:00.250000000Z", "2"),
	}, orderingScenario(), Sentinel{}, nil)

	if hasCheck(v, "invariant.timestamp_precision") || hasCheck(v, "invariant.event_orderable") {
		t.Fatalf("a canonically stamped log must report no ordering finding:\n%s", v.Report())
	}
	// Also the baseline for the tests above: this scenario passes on a canonical log, so a
	// Fail there is attributable to the ordering invariant rather than to the scenario.
	if v.Outcome != Pass {
		t.Fatalf("outcome = %v, want Pass:\n%s", v.Outcome, v.Report())
	}
}

func TestSharedTimestampWithoutSequenceIsReported(t *testing.T) {
	// Two events tied on the timestamp and neither numbered: only the order they landed in
	// the file separates them, which is exactly what this workstream stops relying on.
	tie := "2026-08-02T18:00:00.000000000Z"
	v := run(t, []string{activity(tie, ""), activity(tie, "")}, orderingScenario(), Sentinel{}, nil)

	if !hasCheck(v, "invariant.event_orderable") {
		t.Fatalf("an unorderable pair must be reported:\n%s", v.Report())
	}
	if v.Outcome == Fail {
		t.Fatalf("an unorderable pair is a warning, not a failure:\n%s", v.Report())
	}
}

func TestSharedTimestampWithSequenceIsOrderable(t *testing.T) {
	// The tie a more precise clock cannot break -- one metric export's datapoints -- is
	// resolved by the sequence, so it is not a finding.
	tie := "2026-08-02T18:00:00.000000000Z"
	v := run(t, []string{activity(tie, "11"), activity(tie, "12")}, orderingScenario(), Sentinel{}, nil)

	if hasCheck(v, "invariant.event_orderable") {
		t.Fatalf("a sequenced tie is orderable and must not be reported:\n%s", v.Report())
	}
}

func TestSharedTimestampAcrossSessionsIsNotATie(t *testing.T) {
	// Ordering is only ever asked within a session, so two sessions stamping the same
	// instant is not a collision.
	tie := "2026-08-02T18:00:00.000000000Z"
	other := strings.Replace(activity(tie, ""), `"id":"s1"`, `"id":"s2"`, 1)
	v := run(t, []string{activity(tie, ""), other}, orderingScenario(), Sentinel{}, nil)

	if hasCheck(v, "invariant.event_orderable") {
		t.Fatalf("timestamps shared across sessions are not a tie:\n%s", v.Report())
	}
}
