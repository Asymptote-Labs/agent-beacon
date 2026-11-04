package threatrules

import (
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func TestSingleEventEvaluate(t *testing.T) {
	c, err := Compile(validSingleEventRule())
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	match, err := c.Evaluate([]asymptoteobserve.Event{
		{Event: asymptoteobserve.EventInfo{Action: "tool.invoked"}},
		{Event: asymptoteobserve.EventInfo{Action: "file.read"}},
	})
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if match != VerdictMatch {
		t.Fatalf("want match (one event satisfies), got %s", match)
	}

	noMatch, err := c.Evaluate([]asymptoteobserve.Event{
		{Event: asymptoteobserve.EventInfo{Action: "tool.invoked"}},
	})
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if noMatch != VerdictNoMatch {
		t.Fatalf("want no_match, got %s", noMatch)
	}
}
