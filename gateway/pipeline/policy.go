package pipeline

import "github.com/asymptote-labs/agent-beacon/gateway"

// Policy turns what analysis found into a verdict, and decides the calls analysis could not
// judge. Its verdict is computed for every call, mirrored ones included, so that records show
// what it decided; only an adapter whose Call is Enforce acts on it.
type Policy interface {
	// Decide is the verdict for a turn, given its new and standing findings.
	Decide(t *Turn, findings []Finding) gateway.Verdict
	// Incomplete is the verdict for a turn whose analysis did not complete (its content did not
	// decode, an analyzer or the store failed), given what Decide made of what was found.
	Incomplete(t *Turn, decided gateway.Verdict, errs []error) gateway.Verdict
	// Overloaded is the verdict for a call refused for lack of capacity, before it was read.
	Overloaded() gateway.Verdict
}

// AllowAll is the default policy: every call is allowed, whatever was found, and findings are
// only recorded.
type AllowAll struct{}

var allow = gateway.Verdict{Action: gateway.Allow}

// Decide implements Policy.
func (AllowAll) Decide(*Turn, []Finding) gateway.Verdict { return allow }

// Incomplete implements Policy.
func (AllowAll) Incomplete(*Turn, gateway.Verdict, []error) gateway.Verdict { return allow }

// Overloaded implements Policy.
func (AllowAll) Overloaded() gateway.Verdict { return allow }
