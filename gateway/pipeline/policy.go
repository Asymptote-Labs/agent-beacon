package pipeline

import "github.com/asymptote-labs/agent-beacon/gateway"

// Policy turns a turn's findings into a verdict.
type Policy interface {
	Decide(t *Turn, findings []Finding) gateway.Verdict
}

// AllowAll is the open build's policy: every call is allowed, and findings are only recorded.
type AllowAll struct{}

// Decide implements Policy.
func (AllowAll) Decide(*Turn, []Finding) gateway.Verdict {
	return gateway.Verdict{Action: gateway.Allow}
}
