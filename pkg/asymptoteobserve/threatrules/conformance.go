package threatrules

import "fmt"

// FixtureResult is the outcome of running one fixture through an evaluator.
type FixtureResult struct {
	RuleID   string
	Fixture  string
	Expected Verdict
	Actual   Verdict
	Err      error
}

// OK reports whether the fixture produced its declared verdict without error.
func (r FixtureResult) OK() bool {
	return r.Err == nil && r.Actual == r.Expected
}

func (r FixtureResult) String() string {
	if r.Err != nil {
		return fmt.Sprintf("%s/%s: error: %v", r.RuleID, r.Fixture, r.Err)
	}
	return fmt.Sprintf("%s/%s: expected %s, got %s", r.RuleID, r.Fixture, r.Expected, r.Actual)
}

// CheckRule validates a rule, enforces its maturity gate, compiles it, and runs every
// embedded fixture. It returns one FixtureResult per fixture (in declaration order). A
// non-nil error is a rule-level failure (invalid rule, maturity-gate violation, or
// compile error) that prevents fixtures from running; in that case the results slice is
// nil. A rule a loader returned has passed Validate already and is not validated again.
//
// A fixture fails when the rule could not be evaluated on one of its events, even where
// another event matches: a scan reports that rule as failing on those events, and lint has to
// say what a scan would. The verdict is still recorded.
//
// This is the engine-agnostic core of the conformance harness: a test wrapper turns each
// FixtureResult into a subtest assertion, but the logic here has no test dependency so it
// can also back a CLI lint command later.
func CheckRule(rule *Rule) (results []FixtureResult, err error) {
	if err := rule.validate(); err != nil {
		return nil, fmt.Errorf("rule %q: %w", rule.ID, err)
	}
	if err := CheckMaturity(rule); err != nil {
		return nil, err
	}
	compiled, err := Compile(rule)
	if err != nil {
		return nil, fmt.Errorf("rule %q: compile: %w", rule.ID, err)
	}
	for _, f := range rule.Tests {
		res := FixtureResult{RuleID: rule.ID, Fixture: f.Name, Expected: f.Verdict}
		scan := Scan([]*CompiledRule{compiled}, f.EventList())
		switch {
		case len(scan.Failures) > 0:
			res.Err = scan.Failures[0].Err
			if len(scan.Findings) > 0 {
				res.Actual = VerdictMatch
			}
		case len(scan.Findings) > 0:
			res.Actual = VerdictMatch
		default:
			res.Actual = VerdictNoMatch
		}
		results = append(results, res)
	}
	return results, nil
}
