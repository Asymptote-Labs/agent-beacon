package onboarding

import (
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/harness"
)

func TestRuntimeProbeReportsDetectedNamesSorted(t *testing.T) {
	discoverAll = func() []harness.Harness {
		return []harness.Harness{
			{Name: "cursor", Detected: true},
			{Name: "claude_code", Detected: true},
			{Name: "gemini_cli", Detected: false},
		}
	}
	t.Cleanup(func() { discoverAll = harness.DiscoverAll })

	got := StartRuntimeProbe().Wait(2 * time.Second)
	if len(got) != 2 || got[0] != "claude_code" || got[1] != "cursor" {
		t.Fatalf("Wait() = %v, want sorted detected names", got)
	}
}

func TestRuntimeProbeOmitsUndetectedRuntimes(t *testing.T) {
	discoverAll = func() []harness.Harness {
		return []harness.Harness{{Name: "codex", Detected: false}}
	}
	t.Cleanup(func() { discoverAll = harness.DiscoverAll })

	if got := StartRuntimeProbe().Wait(2 * time.Second); len(got) != 0 {
		t.Fatalf("Wait() = %v, want no entries", got)
	}
}

// Discovery shells out to every installed runtime and can take tens of seconds. It
// must never hold up the install: a slow probe yields nothing rather than blocking.
func TestRuntimeProbeGivesUpOnSlowDiscovery(t *testing.T) {
	release := make(chan struct{})
	discoverAll = func() []harness.Harness {
		<-release
		return []harness.Harness{{Name: "claude_code", Detected: true}}
	}
	t.Cleanup(func() {
		close(release)
		discoverAll = harness.DiscoverAll
	})

	start := time.Now()
	got := StartRuntimeProbe().Wait(50 * time.Millisecond)
	elapsed := time.Since(start)

	if len(got) != 0 {
		t.Fatalf("Wait() = %v, want empty on timeout", got)
	}
	if elapsed > time.Second {
		t.Fatalf("Wait() blocked for %s, want it to give up promptly", elapsed)
	}
}

func TestRuntimeProbeNilIsSafe(t *testing.T) {
	var p *RuntimeProbe
	if got := p.Wait(time.Second); len(got) != 0 {
		t.Fatalf("Wait() on a nil probe = %v, want empty", got)
	}
}

// The probe runs concurrently with the prompt, so collecting it twice (retry paths)
// must not deadlock or panic.
func TestRuntimeProbeSecondWaitTimesOutCleanly(t *testing.T) {
	discoverAll = func() []harness.Harness {
		return []harness.Harness{{Name: "claude_code", Detected: true}}
	}
	t.Cleanup(func() { discoverAll = harness.DiscoverAll })

	probe := StartRuntimeProbe()
	if got := probe.Wait(2 * time.Second); len(got) != 1 {
		t.Fatalf("first Wait() = %v, want one entry", got)
	}
	if got := probe.Wait(10 * time.Millisecond); len(got) != 0 {
		t.Fatalf("second Wait() = %v, want empty", got)
	}
}
