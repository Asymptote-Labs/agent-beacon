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
	match    cel.Program   // set for single-event rules
	steps    []cel.Program // set for correlation rules
	window   time.Duration // parsed correlation window
	anyOrder bool          // correlation steps may match in any order
}

// Compile validates a rule and compiles its CEL expressions into a reusable evaluator.
// Validation (including CEL type-checking) runs first, so Compile fails on any rule that
// would not load.
func Compile(rule *Rule) (*CompiledRule, error) {
	if err := rule.Validate(); err != nil {
		return nil, err
	}
	c := &CompiledRule{rule: rule}
	if rule.Correlation != nil {
		c.steps = make([]cel.Program, len(rule.Correlation.Steps))
		for i, step := range rule.Correlation.Steps {
			prog, err := CompileMatch(step.Match)
			if err != nil {
				return nil, fmt.Errorf("correlation.steps[%d] (%s): %w", i, step.ID, err)
			}
			c.steps[i] = prog
		}
		window, err := rule.Correlation.ParseWindow()
		if err != nil {
			return nil, fmt.Errorf("correlation.window: %w", err)
		}
		c.window = window
		c.anyOrder = rule.Correlation.AnyOrder()
		return c, nil
	}
	prog, err := CompileMatch(rule.Match)
	if err != nil {
		return nil, fmt.Errorf("match: %w", err)
	}
	c.match = prog
	return c, nil
}

// Rule returns the underlying rule.
func (c *CompiledRule) Rule() *Rule { return c.rule }

// Evaluate reports whether the rule fires over the given ordered event sequence.
//
// A single-event rule matches if any event in the sequence satisfies its expression. A
// correlation rule matches if any session (events grouped by session.id) satisfies the
// windowed steps, in listed order unless the rule declares order: any.
func (c *CompiledRule) Evaluate(events []asymptoteobserve.Event) (Verdict, error) {
	if c.steps != nil {
		return c.evaluateCorrelation(events)
	}
	for i := range events {
		matched, err := EvalMatch(c.match, events[i])
		if err != nil {
			return "", err
		}
		if matched {
			return VerdictMatch, nil
		}
	}
	return VerdictNoMatch, nil
}
