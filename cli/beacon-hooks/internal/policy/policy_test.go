package policy

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/policycontract"
)

// fakeProvider points execCommandContext at this test binary running
// TestHelperProcess with the given behavior, so the client's exec path is
// exercised without an external binary.
func fakeProvider(t *testing.T, behavior string) {
	t.Helper()
	orig := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cs := append([]string{"-test.run=TestHelperProcess", "--", name}, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], cs...)
		cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1", "HELPER_BEHAVIOR="+behavior)
		return cmd
	}
	t.Cleanup(func() { execCommandContext = orig })
}

func sampleRequest() Request {
	return Request{
		Phase:    policycontract.PhasePreTool,
		Platform: "claude",
		Event: asymptoteobserve.Event{
			Event:   asymptoteobserve.EventInfo{Action: "command.executed", Category: "command"},
			Command: &asymptoteobserve.CommandInfo{Command: "claude --dangerously-skip-permissions"},
		},
	}
}

func TestEvaluateNoProviderAllows(t *testing.T) {
	os.Unsetenv(ProviderEnv)
	if Enabled() {
		t.Fatal("Enabled() should be false with no provider")
	}
	if got := Evaluate(context.Background(), sampleRequest()); got.Denied() {
		t.Fatalf("expected allow with no provider, got %+v", got)
	}
}

func TestEvaluateProviderDenies(t *testing.T) {
	t.Setenv(ProviderEnv, "fake")
	fakeProvider(t, "deny")
	got := Evaluate(context.Background(), sampleRequest())
	if !got.Denied() {
		t.Fatalf("expected deny, got %+v", got)
	}
	if got.RuleID != "test-rule" || got.Reason != "blocked" {
		t.Fatalf("deny fields not propagated: %+v", got)
	}
}

func TestEvaluateProviderAllows(t *testing.T) {
	t.Setenv(ProviderEnv, "fake")
	fakeProvider(t, "allow")
	if got := Evaluate(context.Background(), sampleRequest()); got.Denied() {
		t.Fatalf("expected allow, got %+v", got)
	}
}

func TestEvaluateFailsOpenOnError(t *testing.T) {
	for _, behavior := range []string{"exit-error", "garbage"} {
		t.Run(behavior, func(t *testing.T) {
			t.Setenv(ProviderEnv, "fake")
			fakeProvider(t, behavior)
			if got := Evaluate(context.Background(), sampleRequest()); got.Denied() {
				t.Fatalf("expected fail-open allow for %q, got %+v", behavior, got)
			}
		})
	}
}

func TestEvaluateFailsOpenOnTimeout(t *testing.T) {
	t.Setenv(ProviderEnv, "fake")
	fakeProvider(t, "sleep")
	orig := Timeout
	Timeout = 100 * time.Millisecond
	t.Cleanup(func() { Timeout = orig })

	start := time.Now()
	got := Evaluate(context.Background(), sampleRequest())
	if got.Denied() {
		t.Fatalf("expected fail-open allow on timeout, got %+v", got)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("timeout not enforced, took %s", elapsed)
	}
}

// TestHelperProcess is not a real test: it is the fake provider invoked by
// fakeProvider via the test binary.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	switch os.Getenv("HELPER_BEHAVIOR") {
	case "deny":
		os.Stdout.WriteString(`{"decision":"deny","reason":"blocked","rule_id":"test-rule","severity":"high","mode":"enforce"}`)
	case "allow":
		os.Stdout.WriteString(`{"decision":"allow"}`)
	case "garbage":
		os.Stdout.WriteString("not json at all")
	case "exit-error":
		os.Stdout.WriteString(`{"decision":"deny"}`) // output ignored because exit is non-zero
		os.Exit(3)
	case "sleep":
		time.Sleep(3 * time.Second)
		os.Stdout.WriteString(`{"decision":"deny"}`)
	}
	os.Exit(0)
}
