package threatrules

import (
	"fmt"
	"time"

	"github.com/google/cel-go/cel"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// Evaluator produces a Verdict for an ordered event sequence. A CompiledRule is the
// reference implementation; a separate (closed) engine satisfies the conformance
// contract by implementing this interface and returning the verdict each fixture
// declares.
type Evaluator interface {
	Evaluate(events []asymptoteobserve.Event) (Verdict, error)
}

// CompiledRule is a validated, CEL-compiled rule ready to evaluate event sequences. It
// is built once (programs are compiled up front) and is safe to reuse across many
// Evaluate calls.
type CompiledRule struct {
	rule     *Rule
	match    matcher       // set for single-event rules
	steps    []matcher     // set for correlation rules
	window   time.Duration // parsed correlation window
	anyOrder bool          // correlation steps may match in any order
	// derived is set when any of the rule's expressions references an engine-derived field
	// (see withDerivedFields). Only those rules pay to derive it, so the rest of a pack runs
	// exactly as it did before the field existed. Compile gives every expression of the rule
	// this form (matcher.derived).
	derived bool
}

// Compile validates a rule and compiles its CEL expressions into a reusable evaluator.
// Validation (including CEL type-checking) runs first, so Compile fails on any rule that
// would not load; a rule that has passed Validate, as every rule a loader returns has, is not
// validated again.
func Compile(rule *Rule) (*CompiledRule, error) {
	if err := rule.validate(); err != nil {
		return nil, err
	}
	c := &CompiledRule{rule: rule}
	if rule.Correlation != nil {
		c.steps = make([]matcher, len(rule.Correlation.Steps))
		for i, step := range rule.Correlation.Steps {
			m, err := compileMatcher(rule.lib, step.Match)
			if err != nil {
				return nil, fmt.Errorf("correlation.steps[%d] (%s): %w", i, step.ID, err)
			}
			c.steps[i] = m
			c.derived = c.derived || m.derived
		}
		for i := range c.steps {
			c.steps[i].derived = c.derived
		}
		window, err := rule.Correlation.ParseWindow()
		if err != nil {
			return nil, fmt.Errorf("correlation.window: %w", err)
		}
		c.window = window
		c.anyOrder = rule.Correlation.AnyOrder()
		return c, nil
	}
	m, err := compileMatcher(rule.lib, rule.Match)
	if err != nil {
		return nil, fmt.Errorf("match: %w", err)
	}
	c.match = m
	c.derived = m.derived
	return c, nil
}

// Rule returns the underlying rule.
func (c *CompiledRule) Rule() *Rule { return c.rule }

// CheckedExpressions returns the rule's expressions as the engine evaluates them: the match of
// a single-event rule, or the steps of a correlation rule in their order. A caller that analyses
// what a rule reads (which fields, which actions) must use these rather than compile the rule's
// text itself: the text is what the author wrote, and only the engine knows what it stands for.
func (c *CompiledRule) CheckedExpressions() []*cel.Ast {
	if c.steps == nil {
		return []*cel.Ast{c.match.ast}
	}
	asts := make([]*cel.Ast, len(c.steps))
	for i, step := range c.steps {
		asts[i] = step.ast
	}
	return asts
}

// Evaluate reports whether the rule fires over the given ordered event sequence: whether Scan
// finds it there.
//
// A single-event rule matches if any event in the sequence satisfies its expression. A
// correlation rule matches if any session (events grouped by session.id) satisfies the
// windowed steps, in listed order unless the rule declares order: any.
//
// An evaluation that fails is the rule's failure on that event alone, as in a scan: the rule
// still matches when another event or session does. When nothing matches and an evaluation
// failed, the verdict is unknown, and the first failure is returned instead of one.
func (c *CompiledRule) Evaluate(events []asymptoteobserve.Event) (Verdict, error) {
	result := Scan([]*CompiledRule{c}, events)
	switch {
	case len(result.Findings) > 0:
		return VerdictMatch, nil
	case len(result.Failures) > 0:
		return "", result.Failures[0].Err
	}
	return VerdictNoMatch, nil
}
