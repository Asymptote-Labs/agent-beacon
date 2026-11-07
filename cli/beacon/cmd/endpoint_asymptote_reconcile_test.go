package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"

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
	// A current config is still worth a line when the forwarder was restarted, or failed to be.
	out.Reset()
	reportManagedForwarder(out, errOut, &asymptote.ReconcileResult{Unchanged: true, Restarted: true})
	if !strings.Contains(out.String(), "restarted") {
		t.Fatalf("restart printed %q", out.String())
	}
	out.Reset()
	errOut.Reset()
	reportManagedForwarder(out, errOut, &asymptote.ReconcileResult{Unchanged: true, Warning: "the forwarder could not be restarted"})
	if out.Len() != 0 || !strings.Contains(errOut.String(), "could not be restarted") {
		t.Fatalf("failed restart printed %q / %q", out.String(), errOut.String())
	}
}

// The macOS package installs with --no-start and then runs this command, so it is the upgrade's
// only chance to restart a forwarder still reading a log the upgrade replaced.
func TestAsymptoteReconcileCommandAsksForARestart(t *testing.T) {
	originalOpts, originalReconcile := endpointOpts, reconcileManagedForwarder
	t.Cleanup(func() { endpointOpts, reconcileManagedForwarder = originalOpts, originalReconcile })
	endpointOpts.userMode = true
	endpointOpts.systemMode = false
	endpointOpts.logPath = "/tmp/runtime.jsonl"
	endpointOpts.jsonOutput = false
	var got asymptote.ReconcileOptions
	reconcileManagedForwarder = func(opts asymptote.ReconcileOptions) (asymptote.ReconcileResult, error) {
		got = opts
		return asymptote.ReconcileResult{Unchanged: true, Restarted: true}, nil
	}
	cmd := &cobra.Command{}
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	if err := runAsymptoteReconcile(cmd, nil); err != nil {
		t.Fatal(err)
	}
	if !got.Restart || got.LogPath != "/tmp/runtime.jsonl" || !got.UserMode {
		t.Fatalf("options = %+v, want a user-mode restart of the named log", got)
	}
	if !strings.Contains(out.String(), "restarted") {
		t.Fatalf("output %q does not report the restart", out.String())
	}
}
