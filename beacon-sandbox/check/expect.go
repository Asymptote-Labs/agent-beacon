package check

import (
	"fmt"
	"sort"
	"strings"

	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/scenario"
	obs "github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// DuplicateWindow is the writer's own base suppression window, not a copy of it.
//
// Identical events written within this window are collapsed to one, so a scenario that runs
// the same command twice in quick succession legitimately yields a single event. Without
// modelling this, such a scenario would be a guaranteed false failure.
const DuplicateWindow = obs.EndpointDuplicateWindow

// Sentinel describes whether the agent demonstrably performed the scenario's work.
type Sentinel struct {
	// Declared is false when the scenario has no sentinel, in which case a missing
	// expectation cannot be disambiguated and is treated as a failure.
	Declared bool
	// Present is true when the sentinel artifact was found after the session.
	Present bool
	// Detail is shown in the verdict, e.g. the sentinel file's contents.
	Detail string
	// Probed is false when the check could not run, so nothing is known about whether the
	// agent acted. Distinct from Present: "did not act" and "cannot tell" call for different
	// responses, and conflating them let a broken guest exec masquerade as an idle model.
	Probed bool
}

// Session describes what is known about whether the agent session itself succeeded.
//
// This is the fallback gate for scenarios with no sentinel -- s01-hello and s07-denied-tool assert
// on prompt, token, cost and approval signals that leave no artifact behind. Without it a dead
// session on those scenarios produced expectation failures that read as Beacon capture gaps, which
// is precisely the ambiguity the sentinel exists to remove elsewhere. Reported by Cursor Bugbot.
type Session struct {
	// Known is false when the agent's own result could not be read, in which case OK says
	// nothing. Reported as unverified rather than assumed either way.
	Known bool
	// OK is true when the agent reported success.
	OK bool
}

// Expectations evaluates a scenario's declared assertions against the captured log.
//
// The sentinel gate runs first: if the scenario declared one and it is absent, the agent
// never did the work, so there is nothing for Beacon to have missed and the verdict is
// INCONCLUSIVE. Reporting FAIL there would blame Beacon for a model's choice, which is
// exactly the ambiguity this design exists to remove.
func Expectations(v *Verdict, log Log, sc scenario.Scenario, canary string, sent Sentinel, sess Session) {
	// An unreadable sentinel is not evidence the agent was idle, so it must not produce
	// INCONCLUSIVE -- that outcome asserts the agent did nothing and tells the reader to retry.
	// Without the gate, a missing expectation cannot be excused either, exactly as when no
	// sentinel was declared, so expectations are evaluated normally and a failure stands.
	if sc.Sentinel != "" && sent.Declared && !sent.Probed {
		v.Add(Finding{
			Check: "sentinel.agent_acted", Severity: SevWarn,
			Summary: fmt.Sprintf("sentinel %s could not be read, so whether the agent acted is unverified", sc.Sentinel),
			Why: "an unreadable probe is not evidence the agent was idle; expectations below are " +
				"judged without that excuse, and a failure here is not automatically a retry",
		})
		for i, exp := range sc.Expect {
			evaluate(v, log, exp, i, canary)
		}
		return
	}
	if sc.Sentinel != "" && sent.Declared && !sent.Present {
		v.Outcome = Inconclusive
		v.Reason = "sentinel absent: the agent did not perform the requested work, so capture cannot be judged"
		v.Add(Finding{
			Check: "sentinel.agent_acted", Severity: SevInfo,
			Summary: fmt.Sprintf("sentinel %s not found (%s)", sc.Sentinel, sent.Detail),
			Why:     "distinguishes a Beacon capture gap from the model declining or failing the task; retry the scenario",
		})
		return
	}
	if sc.Sentinel != "" {
		v.Add(Finding{
			Check: "sentinel.agent_acted", Severity: SevInfo,
			Summary: "confirmed: " + oneLine(sent.Detail),
			Why:     "a missing expectation below is therefore a genuine capture gap, not an inactive agent",
		})
	}

	// No sentinel to fall back on, so the agent's own report is the only evidence about whether
	// there was anything to capture. A failed session means the work never happened, which is a
	// retry rather than a Beacon defect.
	if sc.Sentinel == "" && sess.Known && !sess.OK {
		v.Outcome = Inconclusive
		v.Reason = "the agent session itself failed, so there was nothing for Beacon to capture"
		v.Add(Finding{
			Check: "session.agent_succeeded", Severity: SevInfo,
			Summary: "the agent reported failure (see claude-result.json)",
			Why: "this scenario has no sentinel, so the session result is the only evidence the " +
				"work happened; retry rather than investigating capture",
		})
		return
	}
	if sc.Sentinel == "" && !sess.Known {
		v.Add(Finding{
			Check: "session.agent_succeeded", Severity: SevWarn,
			Summary: "the agent's own result could not be read, so whether the work happened is unverified",
			Why: "with no sentinel and no session result there is nothing to disambiguate a " +
				"missing event, so a failure below is not automatically a Beacon defect",
		})
	}

	for i, exp := range sc.Expect {
		evaluate(v, log, exp, i, canary)
	}
}

func evaluate(v *Verdict, log Log, exp scenario.Expect, idx int, canary string) {
	want := exp.MinCount
	if want <= 0 {
		want = 1
	}
	sev := SevFail
	if exp.Optional {
		sev = SevWarn
	}
	name := fmt.Sprintf("expect[%d].%s", idx, exp.Action)

	// Candidates: events with the right action.
	var candidates []Event
	for _, e := range log.Events {
		if e.ParseErr == nil && e.Action() == exp.Action {
			candidates = append(candidates, e)
		}
	}
	if len(candidates) == 0 {
		v.Add(Finding{
			Check: name, Severity: sev,
			Summary: fmt.Sprintf("no event with action %q was captured", exp.Action),
			Why:     exp.Why,
		})
		return
	}

	// Narrow by required substrings and fields. Both are evaluated for every candidate
	// rather than short-circuiting, because "the action fired but the field threat rules
	// need is empty" is a far more actionable diagnosis than "no match", and the two
	// usually co-occur -- an empty command.command also means the canary is absent.
	var matched []Event
	missingFields := map[string]int{}
	missingSubs := map[string]int{}
	for _, e := range candidates {
		ok := true
		for _, sub := range exp.Contains {
			needle := strings.ReplaceAll(sub, "{{canary}}", canary)
			// strings.Contains(x, "") is true for every x, so an empty needle would make the
			// expectation pass vacuously -- and an offline verify reads the canary from
			// meta.json, where it can be missing or cleared. This is the exact inverse of the
			// empty-secret case the leak check already refuses to call clean, so it gets the
			// same treatment: an unrunnable match is a failure to verify, never a pass.
			if needle == "" {
				missingSubs["<empty marker: cannot be matched>"]++
				ok = false
				continue
			}
			if !strings.Contains(e.Raw, needle) {
				missingSubs[needle]++
				ok = false
			}
		}
		for _, f := range exp.Fields {
			if _, present := e.Field(f); !present {
				missingFields[f]++
				ok = false
			}
		}
		if ok {
			matched = append(matched, e)
		}
	}

	effective := collapseDuplicates(matched)
	if len(effective) >= want {
		return
	}

	// Build an explanation that distinguishes "action absent" from "action present but the
	// field threat rules need is empty" -- a distinction that matters a lot in practice.
	var detail []string
	// Fields first: an unpopulated field is the more specific and more actionable cause,
	// and it is what determines whether threat rules can match.
	if len(missingFields) > 0 {
		var fs []string
		for _, f := range sortedKeys(missingFields) {
			fs = append(fs, fmt.Sprintf("%s (empty on %d event(s))", f, missingFields[f]))
		}
		detail = append(detail, "unpopulated field(s): "+strings.Join(fs, ", "))
	}
	if len(missingSubs) > 0 {
		var ss []string
		for _, s := range sortedKeys(missingSubs) {
			ss = append(ss, fmt.Sprintf("%q (absent from %d event(s))", s, missingSubs[s]))
		}
		detail = append(detail, "missing substring(s): "+strings.Join(ss, ", "))
	}
	msg := fmt.Sprintf("action %q present %d time(s) but only %d matched, want %d",
		exp.Action, len(candidates), len(effective), want)
	if len(detail) > 0 {
		msg += "; " + strings.Join(detail, "; ")
	}

	var ev []string
	for _, e := range candidates {
		ev = append(ev, fmt.Sprintf("%s:%d", log.Path, e.Line))
	}
	v.Add(Finding{
		Check: name, Severity: sev, Summary: msg, Why: exp.Why, Evidence: cap5(ev),
	})
}

// collapseDuplicates removes matches the writer would have suppressed, so counting reflects
// what Beacon can actually be expected to have recorded.
//
// It asks the writer rather than modelling it. This used to be a hand-written mirror of
// asymptoteobserve.IsDuplicateEndpointEvent, and it drifted twice: once by collapsing any
// same-identity pair regardless of harness or call ID, which over-collapsed and could fail a
// min_count the writer would never have suppressed (reported by Cursor Bugbot); and once by
// keying on event.message, which the writer's key does not include -- so the hook and collector
// reports of one call, whose messages differ, stayed two events here and one there. Both were
// the same bug: a second copy of a rule, drifting.
//
// obs.DuplicateEndpointLines takes the raw lines, which is exactly what a verifier has, and
// applies the writer's own comparison. There is nothing left here to drift.
func collapseDuplicates(events []Event) []Event {
	if len(events) < 2 {
		return events
	}
	var out, kept []Event
	for _, e := range events {
		suppressed := false
		for _, prev := range kept {
			if obs.DuplicateEndpointLines([]byte(prev.Raw), []byte(e.Raw), DuplicateWindow) {
				suppressed = true
				break
			}
		}
		if suppressed {
			continue
		}
		kept = append(kept, e)
		out = append(out, e)
	}
	return out
}

// sortedKeys keeps findings stable across runs, so a diff between two verdicts reflects
// real change rather than map iteration order.
func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func oneLine(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) > 160 {
		return s[:160] + "..."
	}
	return s
}
