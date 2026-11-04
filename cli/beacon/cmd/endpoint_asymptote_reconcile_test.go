package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/asymptote"
)

func TestReportManagedForwarderStaysQuietUnlessSomethingChanged(t *testing.T) {
	for _, result := range []*asymptote.ReconcileResult{nil, {Skipped: "not connected"}, {Unchanged: true}} {
		out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
		reportManagedForwarder(out, errOut, result)
		if out.Len() != 0 || errOut.Len() != 0 {
			t.Fatalf("result %+v printed %q / %q", result, out.String(), errOut.String())
		}
	}
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	reportManagedForwarder(out, errOut, &asymptote.ReconcileResult{Warning: "kept the existing config because Vector rejected the new one"})
	if out.Len() != 0 || !strings.Contains(errOut.String(), "kept the existing config") {
		t.Fatalf("warning printed %q / %q", out.String(), errOut.String())
	}
	out.Reset()
	reportManagedForwarder(out, errOut, &asymptote.ReconcileResult{VectorConfig: "/v.toml", Updated: true, Restarted: true})
	if !strings.Contains(out.String(), "/v.toml") {
		t.Fatalf("update printed %q", out.String())
	}
}
