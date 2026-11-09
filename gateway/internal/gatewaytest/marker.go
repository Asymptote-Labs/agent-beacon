// Package gatewaytest holds test doubles for the pipeline's extension points. Marker decides
// verdicts the way a build with a blocking policy would, so that every adapter's rendering of a
// block is exercised end to end, through a real pipeline and its events, in a build whose own
// policy allows every call.
package gatewaytest

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/gateway"
	"github.com/asymptote-labs/agent-beacon/gateway/events"
	"github.com/asymptote-labs/agent-beacon/gateway/pipeline"
)

// Reason is the message a Marker block gives the client.
const Reason = "blocked by the test marker"

// Marker is an inline analyzer and a policy: a call any of whose events carries Text (in a prompt,
// a reply, a tool call or a tool result) gets a finding, and a call with a finding is blocked.
type Marker struct{ Text string }

// Name implements pipeline.Analyzer.
func (Marker) Name() string { return "marker" }

// Inline implements pipeline.Inline: the verdict waits for the marker.
func (Marker) Inline() bool { return true }

// Analyze implements pipeline.Analyzer, reading the events the events analyzer left on the turn.
func (m Marker) Analyze(_ context.Context, t *pipeline.Turn) (pipeline.Report, error) {
	var rep pipeline.Report
	for _, e := range t.Events {
		// The written form: a reply's text is in its message copy, which is left to the record.
		line, err := json.Marshal(e.Written())
		if err != nil {
			return rep, err
		}
		if strings.Contains(string(line), m.Text) {
			rep.Findings = append(rep.Findings, pipeline.Finding{Rule: "test-marker", Severity: pipeline.SeverityHigh, Reason: "marker in " + e.Beacon.Event.Action, Evidence: []string{e.ID()}})
		}
	}
	return rep, nil
}

// Decide implements pipeline.Policy.
func (Marker) Decide(_ *pipeline.Turn, findings []pipeline.Finding) gateway.Verdict {
	if len(findings) == 0 {
		return gateway.Verdict{Action: gateway.Allow}
	}
	return gateway.Verdict{Action: gateway.Block, Reason: Reason, Rules: []string{findings[0].Rule}}
}

// Incomplete implements pipeline.Policy.
func (Marker) Incomplete(_ *pipeline.Turn, decided gateway.Verdict, _ []error) gateway.Verdict {
	return decided
}

// Overloaded implements pipeline.Policy.
func (Marker) Overloaded() gateway.Verdict { return gateway.Verdict{Action: gateway.Allow} }

// Pipeline is a pipeline with the events analyzer and m, closed when the test ends.
func Pipeline(t testing.TB, m Marker) *pipeline.Pipeline {
	t.Helper()
	ev, err := events.New(events.Config{})
	if err != nil {
		t.Fatal(err)
	}
	p := pipeline.New(pipeline.Config{Analyzers: []pipeline.Analyzer{ev, m}, Policy: m})
	t.Cleanup(p.Close)
	return p
}
