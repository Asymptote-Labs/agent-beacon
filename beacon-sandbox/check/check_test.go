package check

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/hostguard"
	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/scenario"
)

// These tests exist because a check that cannot fail is worse than no check: it converts
// "we did not look" into "we verified". Each case deliberately damages a known-good log and
// asserts the specific check that should notice actually does.

const canary = "BEACON_E2E_TESTCANARY"

// goodLog is a minimal but schema-valid stream shaped like a real Claude Code session.
func goodLog() []string {
	return []string{
		`{"timestamp":"2026-08-02T18:00:00Z","vendor":"beacon","product":"endpoint-agent","schema_version":"1.0","event":{"kind":"agent_runtime","action":"prompt.submitted","category":"prompt"},"severity":"info","endpoint":{"os":"linux","hostname":"sandbox"},"harness":{"name":"claude_code"},"session":{"id":"s1"},"prompt":{"text":"run echo ` + canary + `"},"message":"claude_code.user_prompt"}`,
		`{"timestamp":"2026-08-02T18:00:01Z","vendor":"beacon","product":"endpoint-agent","schema_version":"1.0","event":{"kind":"agent_runtime","action":"command.executed","category":"command"},"severity":"info","endpoint":{"os":"linux","hostname":"sandbox"},"harness":{"name":"claude_code"},"session":{"id":"s1"},"tool":{"name":"Bash"},"command":{"command":"echo ` + canary + `"},"message":"claude_code.tool_result"}`,
		`{"timestamp":"2026-08-02T18:00:02Z","vendor":"beacon","product":"endpoint-agent","schema_version":"1.0","event":{"kind":"agent_runtime","action":"token.usage","category":"metric"},"severity":"info","endpoint":{"os":"linux","hostname":"sandbox"},"harness":{"name":"claude_code"},"session":{"id":"s1"},"model":"claude-opus-5","gen_ai":{"usage":{"input_tokens":100}},"message":"claude_code.token.usage"}`,
	}
}

func writeLog(t *testing.T, lines []string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func demoScenario() scenario.Scenario {
	return scenario.Scenario{
		ID:       "t-demo",
		Prompt:   "run echo {{canary}}",
		Sentinel: "/tmp/sentinel",
		Expect: []scenario.Expect{
			{Action: "prompt.submitted", Contains: []string{"{{canary}}"}},
			{Action: "command.executed", Contains: []string{"{{canary}}"}, Fields: []string{"command.command"}},
			{Action: "token.usage", Fields: []string{"gen_ai.usage.input_tokens"}},
		},
	}
}

func run(t *testing.T, lines []string, sc scenario.Scenario, sent Sentinel, secrets map[string]string) Verdict {
	t.Helper()
	log, err := ReadLog(writeLog(t, lines))
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	v := Verdict{Scenario: sc.ID}
	Invariants(&v, log, Secrets{Values: secrets})
	Expectations(&v, log, sc, canary, sent, okSession())
	v.Resolve()
	return v
}

// okSession is the default for tests not exercising the session gate: the agent reported success.
func okSession() Session { return Session{Known: true, OK: true} }

func presentSentinel() Sentinel {
	return Sentinel{Declared: true, Present: true, Probed: true, Detail: canary}
}

func TestGoodLogPasses(t *testing.T) {
	v := run(t, goodLog(), demoScenario(), presentSentinel(), nil)
	if v.Outcome != Pass {
		t.Fatalf("expected PASS, got %s:\n%s", v.Outcome, v.Report())
	}
}

// The headline self-test: removing the command event must fail exactly that expectation and
// nothing else, so a real capture gap is reported precisely rather than as a vague failure.
func TestDroppedCommandFailsExactlyThatExpectation(t *testing.T) {
	lines := goodLog()
	lines = append(lines[:1], lines[2:]...) // drop command.executed
	v := run(t, lines, demoScenario(), presentSentinel(), nil)

	if v.Outcome != Fail {
		t.Fatalf("expected FAIL, got %s:\n%s", v.Outcome, v.Report())
	}
	fails := v.Failures()
	if len(fails) != 1 {
		t.Fatalf("expected exactly 1 failing finding, got %d:\n%s", len(fails), v.Report())
	}
	if !strings.Contains(fails[0].Check, "command.executed") {
		t.Errorf("failure should name command.executed, got %q", fails[0].Check)
	}
}

// An action present but its key field empty is the most important distinction the checks
// make: threat rules match fields, not action names, so "event exists" is not enough.
func TestActionPresentButFieldEmptyStillFails(t *testing.T) {
	lines := goodLog()
	lines[1] = strings.Replace(lines[1], `"command":{"command":"echo `+canary+`"},`, "", 1)
	v := run(t, lines, demoScenario(), presentSentinel(), nil)

	if v.Outcome != Fail {
		t.Fatalf("expected FAIL when command.command is unpopulated, got %s:\n%s", v.Outcome, v.Report())
	}
	var found bool
	for _, f := range v.Failures() {
		if strings.Contains(f.Summary, "unpopulated field") && strings.Contains(f.Summary, "command.command") {
			found = true
		}
	}
	if !found {
		t.Errorf("failure should explain the unpopulated field, got:\n%s", v.Report())
	}
}

func TestCorruptLineTripsInvariant(t *testing.T) {
	lines := append(goodLog(), `{"vendor":"beacon", this is not json`)
	v := run(t, lines, demoScenario(), presentSentinel(), nil)

	if v.Outcome != Fail {
		t.Fatalf("expected FAIL on a corrupt line, got %s", v.Outcome)
	}
	if !hasCheck(v, "invariant.jsonl_parseable") {
		t.Errorf("expected invariant.jsonl_parseable to fire:\n%s", v.Report())
	}
}

func TestSchemaViolationTripsInvariant(t *testing.T) {
	lines := goodLog()
	// Wrong vendor: Validate() treats vendor/product as a release contract.
	lines[0] = strings.Replace(lines[0], `"vendor":"beacon"`, `"vendor":"asymptote"`, 1)
	v := run(t, lines, demoScenario(), presentSentinel(), nil)

	if !hasCheck(v, "invariant.schema_valid") {
		t.Errorf("expected invariant.schema_valid to fire:\n%s", v.Report())
	}
	if v.Outcome != Fail {
		t.Errorf("expected FAIL, got %s", v.Outcome)
	}
}

func TestPlantedSecretIsDetected(t *testing.T) {
	secret := "sk-ant-thisisnotarealkey-000111222"
	lines := goodLog()
	lines[0] = strings.Replace(lines[0], `"message":`, `"leaked":"`+secret+`","message":`, 1)
	v := run(t, lines, demoScenario(), presentSentinel(),
		map[string]string{"ANTHROPIC_API_KEY": secret})

	if !hasCheck(v, "invariant.no_secret_leak") {
		t.Errorf("expected the secret leak check to fire:\n%s", v.Report())
	}
	if v.Outcome != Fail {
		t.Errorf("expected FAIL, got %s", v.Outcome)
	}
}

// An empty secret must not be reported as clean. A naive substring search for "" matches
// every line; during M0 that produced a false "LEAKED" verdict, and the inverse mistake
// would produce false assurance.
func TestEmptySecretIsReportedAsUnverifiedNotClean(t *testing.T) {
	v := run(t, goodLog(), demoScenario(), presentSentinel(),
		map[string]string{"ANTHROPIC_API_KEY": ""})

	if !hasCheck(v, "invariant.no_secret_leak") {
		t.Fatalf("expected a warning that the check could not run:\n%s", v.Report())
	}
	for _, f := range v.Findings {
		if f.Check == "invariant.no_secret_leak" {
			if f.Severity != SevWarn {
				t.Errorf("expected warn severity for an unverifiable check, got %s", f.Severity)
			}
			if !strings.Contains(f.Summary, "vacuous") {
				t.Errorf("summary should say the check was vacuous, got %q", f.Summary)
			}
		}
	}
	// It must not fail the run either: nothing was proven wrong, just unproven.
	if v.Outcome == Fail {
		t.Errorf("an unverifiable secret check should not fail the run")
	}
}

// A provider-stored secret is the most secure credential path precisely because its value never
// reaches this process, which also means the leak check has nothing to search for. The resulting
// warning must name that cause: "value was empty" reads as a defect and would send someone
// looking for a bug that isn't there.
func TestWithheldSecretWarnsWithTheReasonAndDoesNotFail(t *testing.T) {
	log, err := ReadLog(writeLog(t, goodLog()))
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	sc := demoScenario()
	v := Verdict{Scenario: sc.ID}
	Invariants(&v, log, Secrets{Withheld: map[string]string{
		"ANTHROPIC_API_KEY": `it is stored as provider secret "team-key" and never enters this process`,
	}})
	Expectations(&v, log, sc, canary, presentSentinel(), okSession())
	v.Resolve()

	if !hasCheck(v, "invariant.no_secret_leak") {
		t.Fatalf("a withheld credential must still produce a finding:\n%s", v.Report())
	}
	for _, f := range v.Findings {
		if f.Check != "invariant.no_secret_leak" {
			continue
		}
		if f.Severity != SevWarn {
			t.Errorf("severity = %s, want warn: nothing was proven wrong, just unproven", f.Severity)
		}
		if !strings.Contains(f.Summary, "team-key") {
			t.Errorf("summary should carry the supplied reason, got %q", f.Summary)
		}
		if strings.Contains(f.Summary, "was empty") {
			t.Errorf("a deliberately withheld value must not be described as empty: %q", f.Summary)
		}
	}
	// Choosing the most secure credential path must not fail the run.
	if v.Outcome != Pass {
		t.Errorf("outcome = %s, want pass:\n%s", v.Outcome, v.Report())
	}
}

// The sentinel gate is what separates a capture bug from a model that declined the task.
func TestAbsentSentinelIsInconclusiveNotFail(t *testing.T) {
	lines := goodLog()[:1] // only the prompt: no command was ever run
	v := run(t, lines, demoScenario(), Sentinel{Declared: true, Present: false, Probed: true, Detail: "__MISSING__"}, nil)

	if v.Outcome != Inconclusive {
		t.Fatalf("expected INCONCLUSIVE when the agent never acted, got %s:\n%s", v.Outcome, v.Report())
	}
	if len(v.Failures()) != 0 {
		t.Errorf("an inconclusive run must not report capture failures:\n%s", v.Report())
	}
	if !strings.Contains(v.Reason, "sentinel") {
		t.Errorf("reason should explain the sentinel gate, got %q", v.Reason)
	}
}

// Without a sentinel there is nothing to disambiguate with, so a missing event has to fail.
// Otherwise a scenario could silently never assert anything.
func TestMissingEventWithoutSentinelFails(t *testing.T) {
	sc := demoScenario()
	sc.Sentinel = ""
	lines := goodLog()[:1]
	v := run(t, lines, sc, Sentinel{}, nil)

	if v.Outcome != Fail {
		t.Fatalf("expected FAIL without a sentinel to excuse the absence, got %s", v.Outcome)
	}
}

// The matcher must model asymptoteobserve.IsDuplicateEndpointEvent, not an approximation of it.
//
// This test previously asserted the approximation: that any two identical events inside the 2s
// window collapse. That is wrong for the case every sandbox run actually produces. All events
// carry harness claude_code, and the real writer preserves same-harness repeats unless both
// share an equal non-empty tool call ID -- two adjacent calls can legitimately touch the same
// file or command. The old model under-counted legitimate repeats and could fail a min_count the
// writer would never have collapsed. Cursor Bugbot caught it; the test was wrong, not the fix.
func TestSameHarnessRepeatsSurviveWithoutMatchingCallIDs(t *testing.T) {
	base := goodLog()
	dup := strings.Replace(base[1], "18:00:01Z", "18:00:02Z", 1) // 1s apart, no call IDs

	sc := demoScenario()
	sc.Expect = []scenario.Expect{{
		Action: "command.executed", Fields: []string{"command.command"}, MinCount: 2,
		Why: "two adjacent Bash calls can legitimately run the same command",
	}}

	v := run(t, []string{base[0], base[1], dup}, sc, presentSentinel(), nil)
	if v.Outcome != Pass {
		t.Errorf("same-harness repeats without call IDs are preserved by the writer, so "+
			"min_count 2 must pass:\n%s", v.Report())
	}
}

// An equal, non-empty call ID is the one case the writer really does collapse, so requiring two
// would be a guaranteed false failure.
//
// No window applies to it, which is the whole point: a hook writes when the tool runs and the
// collector writes when its batch flushes, so the two reports of one call land further apart than
// any window worth having. Both distances are pinned here because the near one used to be the only
// one that collapsed.
func TestMatchingCallIDsCollapseAtAnyDistance(t *testing.T) {
	base := goodLog()
	withID := func(line, ts, id string) string {
		line = strings.Replace(line, "18:00:01Z", ts, 1)
		return strings.Replace(line, `"tool":{"name":"Bash"}`,
			`"tool":{"name":"Bash"},"gen_ai":{"tool":{"call":{"id":"`+id+`"}}}`, 1)
	}
	first := withID(base[1], "18:00:01Z", "call-1")
	same := withID(base[1], "18:00:02Z", "call-1")  // 1s apart, identical call id
	later := withID(base[1], "18:00:11Z", "call-1") // 10s apart, well past the 2s window

	sc := demoScenario()
	sc.Expect = []scenario.Expect{{
		Action: "command.executed", Fields: []string{"command.command"}, MinCount: 2,
		Why: "pins the one case the writer really suppresses",
	}}

	within := run(t, []string{base[0], first, same}, sc, presentSentinel(), nil)
	if within.Outcome != Fail {
		t.Errorf("equal call IDs 1s apart are collapsed by the writer, so min_count 2 must "+
			"fail:\n%s", within.Report())
	}

	outside := run(t, []string{base[0], first, later}, sc, presentSentinel(), nil)
	if outside.Outcome != Fail {
		t.Errorf("equal call IDs are collapsed however far apart they are, so min_count 2 must "+
			"fail at 10s too:\n%s", outside.Report())
	}
}

// Only a fixed set of actions is eligible for suppression at all. Treating others as
// collapsible would invent suppression the writer never performs.
func TestNonCandidateActionsAreNeverCollapsed(t *testing.T) {
	base := goodLog()
	// token.usage is not in the writer's dedupe set, so two 1s apart both count.
	dup := strings.Replace(base[2], "18:00:02Z", "18:00:03Z", 1)

	sc := demoScenario()
	sc.Expect = []scenario.Expect{{
		Action: "token.usage", MinCount: 2,
		Why: "token.usage is outside the writer's dedupe set",
	}}

	v := run(t, []string{base[0], base[2], dup}, sc, presentSentinel(), nil)
	if v.Outcome != Pass {
		t.Errorf("token.usage is not a dedupe candidate, so both must count:\n%s", v.Report())
	}
}

func TestOptionalExpectationWarnsButDoesNotFail(t *testing.T) {
	sc := demoScenario()
	sc.Expect = append(sc.Expect, scenario.Expect{
		Action: "approval.denied", Optional: true, Why: "known gap",
	})
	v := run(t, goodLog(), sc, presentSentinel(), nil)

	if v.Outcome != Pass {
		t.Fatalf("an optional expectation must not fail the run, got %s:\n%s", v.Outcome, v.Report())
	}
	var warned bool
	for _, f := range v.Findings {
		if f.Severity == SevWarn && strings.Contains(f.Check, "approval.denied") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("optional expectation should still be recorded as a warning:\n%s", v.Report())
	}
}

func TestUnknownActionIsSurfaced(t *testing.T) {
	lines := goodLog()
	lines[2] = strings.Replace(lines[2], `"action":"token.usage"`, `"action":"token.usaeg"`, 1)
	v := run(t, lines, demoScenario(), presentSentinel(), nil)

	if !hasCheck(v, "invariant.known_actions") {
		t.Errorf("a typo'd action should be surfaced, since there is no enum to catch it:\n%s", v.Report())
	}
}

func TestOversizedEventFails(t *testing.T) {
	lines := goodLog()
	huge := strings.Repeat("x", MaxEventBytes+10)
	lines = append(lines, strings.Replace(lines[0], "sandbox", huge, 1))
	v := run(t, lines, demoScenario(), presentSentinel(), nil)

	if !hasCheck(v, "invariant.event_size") {
		t.Errorf("expected the size cap check to fire:\n%s", v.Report())
	}
}

func TestFieldLookupHandlesNesting(t *testing.T) {
	log, err := ReadLog(writeLog(t, goodLog()))
	if err != nil {
		t.Fatal(err)
	}
	e := log.Events[2]
	if _, ok := e.Field("gen_ai.usage.input_tokens"); !ok {
		t.Error("expected to resolve a nested numeric field")
	}
	if _, ok := e.Field("gen_ai.usage.output_tokens"); ok {
		t.Error("absent nested field must report not-present, not panic or default")
	}
	if _, ok := e.Field("command.command"); ok {
		t.Error("absent sub-object must report not-present")
	}
}

func hasCheck(v Verdict, name string) bool {
	for _, f := range v.Findings {
		if f.Check == name {
			return true
		}
	}
	return false
}

// CompareFingerprints previously treated any outcome change other than PASS -> FAIL as an
// improvement, so INCONCLUSIVE -> FAIL read as progress and PASS -> INCONCLUSIVE hid a lost
// signal. INCONCLUSIVE means the agent never did the work, so that run measured nothing and a
// measurement cannot be compared against a non-measurement in either direction.
func TestOutcomeTransitionsInvolvingInconclusiveAreNotComparable(t *testing.T) {
	cases := []struct {
		before, after Outcome
		bucket        string
	}{
		{Pass, Fail, "regression"},
		{Fail, Pass, "improvement"},
		{Inconclusive, Fail, "neutral"},
		{Pass, Inconclusive, "neutral"},
		{Inconclusive, Pass, "neutral"},
		{Fail, Inconclusive, "neutral"},
	}
	for _, c := range cases {
		d := CompareFingerprints(
			Fingerprint{Outcome: c.before}, Fingerprint{Outcome: c.after})
		got := "none"
		switch {
		case len(d.Regressions) > 0:
			got = "regression"
		case len(d.Improvements) > 0:
			got = "improvement"
		case len(d.Neutral) > 0:
			got = "neutral"
		}
		if got != c.bucket {
			t.Errorf("%s -> %s classified as %s, want %s", c.before, c.after, got, c.bucket)
		}
	}
}

// Resolve used to return early on INCONCLUSIVE, so an inactive agent suppressed every hard
// failure reported alongside it -- a leaked credential, a corrupt line, a host escape, an argv
// disclosure. Those are invariants and safety properties: they do not depend on the agent having
// done the work, so an idle agent is no reason to stop reporting them. Cursor Bugbot flagged this
// as High, and it is the worst class of bug this tool can have: a suppressed security finding.
func TestInconclusiveDoesNotMaskHardFailures(t *testing.T) {
	for _, name := range []string{
		"invariant.no_secret_leak",
		"invariant.jsonl_parseable",
		"safety.host_untouched",
		"safety.secret_not_in_argv",
	} {
		v := Verdict{Outcome: Inconclusive}
		v.Add(Finding{Check: name, Severity: SevFail, Summary: "something is genuinely wrong"})
		v.Resolve()

		if v.Outcome != Fail {
			t.Errorf("%s at fail severity must produce FAIL even when inconclusive, got %s",
				name, v.Outcome)
		}
	}
}

// A bare INCONCLUSIVE with nothing actually wrong must stay inconclusive, or the fix above would
// have turned every idle-agent run into a false Beacon bug report.
func TestInconclusiveSurvivesWhenNothingFailed(t *testing.T) {
	v := Verdict{Outcome: Inconclusive}
	v.Add(Finding{Check: "sentinel.agent_acted", Severity: SevInfo, Summary: "not found"})
	v.Add(Finding{Check: "invariant.timestamp_order", Severity: SevWarn, Summary: "some drift"})
	v.Resolve()

	if v.Outcome != Inconclusive {
		t.Errorf("outcome = %s, want inconclusive: info and warn findings are not failures", v.Outcome)
	}
}

// hostguard.Diff.Describe always returns a non-empty string, so an empty HostChanged can only
// mean the comparison never ran. Safety previously said nothing in that case, reporting an unrun
// check as a passing one -- the same mistake the argv branch already avoided, which is the
// inconsistency Cursor Bugbot spotted.
func TestUnrecordedHostComparisonWarnsRatherThanReadingClean(t *testing.T) {
	var v Verdict
	Safety(&v, HostSafety{HostChanged: "", ArgvCheckRan: true})

	var found *Finding
	for i := range v.Findings {
		if v.Findings[i].Check == "safety.host_untouched" {
			found = &v.Findings[i]
		}
	}
	if found == nil {
		t.Fatalf("an unrecorded host comparison must produce a finding:\n%s", v.Report())
	}
	if found.Severity != SevWarn {
		t.Errorf("severity = %s, want warn: unproven, not failed", found.Severity)
	}
	if !strings.Contains(found.Summary, "unverified") {
		t.Errorf("summary should say unverified, got %q", found.Summary)
	}
}

// A recorded clean comparison must stay silent, or every run would carry a spurious warning.
func TestCleanHostComparisonIsSilent(t *testing.T) {
	var v Verdict
	Safety(&v, HostSafety{HostChanged: hostguard.CleanDescription, ArgvCheckRan: true})

	if hasCheck(v, "safety.host_untouched") {
		t.Errorf("a clean comparison should add nothing:\n%s", v.Report())
	}
}

// A real change is still a hard failure -- the fix above must not have softened it.
func TestChangedHostStillFails(t *testing.T) {
	var v Verdict
	Safety(&v, HostSafety{HostChanged: "modified: /var/log/beacon-agent", ArgvCheckRan: true})
	v.Resolve()

	if v.Outcome != Fail {
		t.Errorf("a host change must fail the run, got %s", v.Outcome)
	}
}

// strings.Contains(x, "") is true for every x, so an empty canary would make every marker
// expectation pass vacuously. Offline verify reads the canary from meta.json where it can be
// missing or cleared, so this is reachable in practice -- and it is the exact inverse of the
// empty-secret case this tool already refuses to call clean. Cursor Bugbot flagged it as High.
func TestEmptyCanaryCannotSatisfyAMarkerExpectation(t *testing.T) {
	sc := demoScenario()
	sc.Expect = []scenario.Expect{{
		Action: "command.executed", Contains: []string{"{{canary}}"},
		Why: "the marker is the whole point of the check",
	}}

	log, err := ReadLog(writeLog(t, goodLog()))
	if err != nil {
		t.Fatal(err)
	}
	v := Verdict{Scenario: sc.ID}
	Expectations(&v, log, sc, "", presentSentinel(), okSession()) // empty canary
	v.Resolve()

	if v.Outcome != Fail {
		t.Fatalf("an unmatchable marker must fail, not pass vacuously:\n%s", v.Report())
	}
	if !strings.Contains(v.Report(), "empty marker") {
		t.Errorf("the finding should name the cause, got:\n%s", v.Report())
	}
}

// A real canary must still match, or the guard above would break every genuine run.
func TestRealCanaryStillMatches(t *testing.T) {
	sc := demoScenario()
	sc.Expect = []scenario.Expect{{
		Action: "command.executed", Contains: []string{"{{canary}}"}, Why: "baseline",
	}}
	v := run(t, goodLog(), sc, presentSentinel(), nil)
	if v.Outcome != Pass {
		t.Errorf("a present canary must match:\n%s", v.Report())
	}
}

// Host escape and credential-in-argv come from run metadata, not from the log, so they must be
// reported even when the log cannot be read. A collection problem is the weakest reason imaginable
// to stop reporting the strongest findings. Cursor Bugbot flagged the masking as High.
func TestSafetyFindingsAreIndependentOfTheLog(t *testing.T) {
	// A leaked credential and a clobbered host, with no log at all.
	var v Verdict
	Safety(&v, HostSafety{
		HostChanged:  "modified: /Users/dev/.beacon/endpoint",
		SecretInArgv: true,
		ArgvCheckRan: true,
	})
	v.Resolve()

	if v.Outcome != Fail {
		t.Fatalf("host escape plus argv disclosure must fail, got %s", v.Outcome)
	}
	for _, want := range []string{"safety.host_untouched", "safety.secret_not_in_argv"} {
		if !hasCheck(v, want) {
			t.Errorf("%s must be reported without any log:\n%s", want, v.Report())
		}
	}
}

// Overriding INCONCLUSIVE to FAIL has to take its reason with it. Leaving "the agent did not
// perform the requested work" on a FAIL misattributes the failure to an idle model when something
// independent actually broke, sending a reader off to retry instead of to the finding.
func TestOverriddenInconclusiveDoesNotKeepItsReason(t *testing.T) {
	v := Verdict{
		Outcome: Inconclusive,
		Reason:  "sentinel absent: the agent did not perform the requested work, so capture cannot be judged",
	}
	v.Add(Finding{Check: "invariant.jsonl_parseable", Severity: SevFail, Summary: "corrupt line"})
	v.Resolve()

	if v.Outcome != Fail {
		t.Fatalf("outcome = %s, want fail", v.Outcome)
	}
	if strings.Contains(v.Reason, "capture cannot be judged") {
		t.Errorf("the inconclusive reason must not survive onto a FAIL: %q", v.Reason)
	}
	if !strings.Contains(v.Reason, "do not") {
		t.Errorf("the reason should warn against dismissing this as a retry, got %q", v.Reason)
	}
}

// A genuine INCONCLUSIVE keeps its explanation, since the retry advice is correct there.
func TestGenuineInconclusiveKeepsItsReason(t *testing.T) {
	const reason = "sentinel absent: the agent did not perform the requested work"
	v := Verdict{Outcome: Inconclusive, Reason: reason}
	v.Add(Finding{Check: "sentinel.agent_acted", Severity: SevInfo, Summary: "not found"})
	v.Resolve()

	if v.Outcome != Inconclusive || v.Reason != reason {
		t.Errorf("a real inconclusive must keep its reason, got %s / %q", v.Outcome, v.Reason)
	}
}

// An unreadable sentinel is not evidence the agent was idle. The probe used to discard its exec
// error, so a failed guest exec produced empty stdout, "__MISSING__" never appeared, and the run
// was reported INCONCLUSIVE -- telling the reader to retry when the real problem was
// infrastructure, and quietly excusing any missing event. Cursor Bugbot flagged it as High, noting
// the argv path in the same function already required a successful exec.
func TestUnreadableSentinelIsNotTreatedAsAnIdleAgent(t *testing.T) {
	sc := demoScenario()
	// The log is missing the command event the scenario requires.
	v := run(t, goodLog()[:1], sc, Sentinel{Declared: true, Probed: false}, nil)

	if v.Outcome == Inconclusive {
		t.Errorf("a failed probe must not claim the agent was idle:\n%s", v.Report())
	}
	if v.Outcome != Fail {
		t.Fatalf("without sentinel evidence a missing event cannot be excused, got %s:\n%s",
			v.Outcome, v.Report())
	}
	report := v.Report()
	if !strings.Contains(report, "could not be read") {
		t.Errorf("the verdict should say the probe failed:\n%s", report)
	}
	if !strings.Contains(report, "unverified") {
		t.Errorf("the finding should read as unverified rather than absent:\n%s", report)
	}
}

// A probe that ran and found nothing still means the agent was idle, so the retry advice stands.
func TestProbedAbsentSentinelStaysInconclusive(t *testing.T) {
	v := run(t, goodLog()[:1], demoScenario(),
		Sentinel{Declared: true, Present: false, Probed: true, Detail: "__MISSING__"}, nil)

	if v.Outcome != Inconclusive {
		t.Errorf("a successful probe finding nothing is a real inconclusive, got %s:\n%s",
			v.Outcome, v.Report())
	}
}

// SessionOK is collected specifically to separate an agent/auth failure from a capture gap, and the
// claude-out.json path fix exists to keep that signal accurate -- but judge never read it. On the
// sentinel-less scenarios (s01-hello, s07-denied-tool) a dead session therefore produced expectation
// failures that read as Beacon capture bugs, which is the exact ambiguity the sentinel removes
// elsewhere. Cursor Bugbot flagged it.
func TestFailedSessionWithoutASentinelIsInconclusiveNotFail(t *testing.T) {
	sc := demoScenario()
	sc.Sentinel = "" // s01/s07 shape: nothing left behind to check
	log, err := ReadLog(writeLog(t, goodLog()[:1]))
	if err != nil {
		t.Fatal(err)
	}
	v := Verdict{Scenario: sc.ID}
	Expectations(&v, log, sc, canary, Sentinel{}, Session{Known: true, OK: false})
	v.Resolve()

	if v.Outcome != Inconclusive {
		t.Fatalf("a dead session with no sentinel must be inconclusive, got %s:\n%s",
			v.Outcome, v.Report())
	}
	if !strings.Contains(v.Reason, "agent session itself failed") {
		t.Errorf("the reason should blame the session, not capture: %q", v.Reason)
	}
}

// A successful session must not excuse a missing event -- that would make every scenario pass.
func TestSuccessfulSessionStillFailsOnAMissingEvent(t *testing.T) {
	sc := demoScenario()
	sc.Sentinel = ""
	log, err := ReadLog(writeLog(t, goodLog()[:1]))
	if err != nil {
		t.Fatal(err)
	}
	v := Verdict{Scenario: sc.ID}
	Expectations(&v, log, sc, canary, Sentinel{}, Session{Known: true, OK: true})
	v.Resolve()

	if v.Outcome != Fail {
		t.Errorf("a working session with a missing event is a real capture gap, got %s:\n%s",
			v.Outcome, v.Report())
	}
}

// An unreadable session result says nothing, so it must warn rather than excuse or blame.
func TestUnknownSessionResultWarnsWithoutExcusing(t *testing.T) {
	sc := demoScenario()
	sc.Sentinel = ""
	log, err := ReadLog(writeLog(t, goodLog()[:1]))
	if err != nil {
		t.Fatal(err)
	}
	v := Verdict{Scenario: sc.ID}
	Expectations(&v, log, sc, canary, Sentinel{}, Session{Known: false})
	v.Resolve()

	if v.Outcome != Fail {
		t.Errorf("an unknown session result must not excuse a missing event, got %s", v.Outcome)
	}
	if !strings.Contains(v.Report(), "unverified") {
		t.Errorf("the verdict should record that the session result was unreadable:\n%s", v.Report())
	}
}

// With a sentinel present the sentinel governs: the agent demonstrably acted, whatever its own
// exit status claimed.
func TestSentinelBeatsTheSessionSignal(t *testing.T) {
	v := run(t, goodLog(), demoScenario(), presentSentinel(), nil)
	if v.Outcome != Pass {
		t.Errorf("a confirmed sentinel should govern, got %s:\n%s", v.Outcome, v.Report())
	}
}

// The timestamp bookkeeping tracks the latest timestamp per session and reports only large
// backwards jumps. A reviewer read the original `else if` as a compile error, so the restructured
// form is pinned by behavior rather than by shape: the first event of a session must be recorded,
// ordinary forward progress must not warn, and only a jump beyond the tolerance must.
func TestTimestampOrderOnlyWarnsOnLargeRegressions(t *testing.T) {
	at := func(ts string) string {
		return `{"timestamp":"` + ts + `","vendor":"beacon","product":"endpoint-agent",` +
			`"schema_version":"1.0","event":{"kind":"agent_runtime","action":"session.activity",` +
			`"category":"session"},"severity":"info","endpoint":{"os":"linux","hostname":"s"},` +
			`"harness":{"name":"claude_code"},"session":{"id":"s1"},"message":"m"}`
	}
	sc := demoScenario()
	sc.Sentinel = ""
	sc.Expect = []scenario.Expect{{Action: "session.activity", Why: "baseline"}}

	// Forward progress, plus a 1s hiccup well inside the 5s tolerance.
	fine := run(t, []string{at("2026-08-02T18:00:00Z"), at("2026-08-02T18:00:10Z"),
		at("2026-08-02T18:00:09Z")}, sc, Sentinel{}, nil)
	if hasCheck(fine, "invariant.timestamp_order") {
		t.Errorf("a 1s hiccup is inside the tolerance and must not warn:\n%s", fine.Report())
	}

	// A 30s backwards jump is outside it.
	regressed := run(t, []string{at("2026-08-02T18:00:00Z"), at("2026-08-02T18:01:00Z"),
		at("2026-08-02T18:00:30Z")}, sc, Sentinel{}, nil)
	if !hasCheck(regressed, "invariant.timestamp_order") {
		t.Errorf("a 30s backwards jump must warn:\n%s", regressed.Report())
	}
	// A warning only, never a failure: batching makes modest disorder normal.
	if regressed.Outcome == Fail {
		t.Errorf("timestamp disorder is a warning, not a failure:\n%s", regressed.Report())
	}
}

// A flaky service probe must warn, not fail. Treating it as a host escape would manufacture the
// most serious finding this tool has out of a tooling hiccup; staying silent would claim a clean
// bill of health the guard did not earn.
func TestPartiallyVerifiedHostWarnsRatherThanFailing(t *testing.T) {
	var v Verdict
	Safety(&v, HostSafety{HostChanged: hostguard.PartialDescription, ArgvCheckRan: true})
	v.Resolve()

	if v.Outcome == Fail {
		t.Errorf("an unavailable service probe is not a host escape:\n%s", v.Report())
	}
	if !hasCheck(v, "safety.host_untouched") {
		t.Errorf("the partial verification must still be reported:\n%s", v.Report())
	}
	if !strings.Contains(v.Report(), "unverified") {
		t.Errorf("the finding should read as unverified:\n%s", v.Report())
	}
}
