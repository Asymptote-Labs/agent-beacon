package onboarding

import (
	"sort"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/harness"
)

// discoverAll is indirected so tests can supply a fake without shelling out.
var discoverAll = harness.DiscoverAll

// RuntimeProbe is a background agent-runtime discovery pass.
//
// harness.DiscoverAll runs `--version` against every supported runtime with a two
// second timeout each, so a machine with several installed can take tens of seconds
// in the worst case. That is far too long to sit in front of a prompt. Starting the
// probe before the first question and collecting it at submit time hides the whole
// cost behind the time it takes a human to read the disclosure and type an email,
// and reuses the existing discovery logic rather than maintaining a second copy.
type RuntimeProbe struct {
	done chan []string
}

// StartRuntimeProbe begins discovery immediately and returns a handle to collect it.
func StartRuntimeProbe() *RuntimeProbe {
	// Read the indirection on the caller's goroutine rather than inside the worker.
	// Wait can return before discovery finishes, so the worker may outlive the caller;
	// capturing here means a test swapping discoverAll back cannot race a live probe.
	discover := discoverAll
	p := &RuntimeProbe{done: make(chan []string, 1)}
	go func() {
		found := []string{}
		for _, h := range discover() {
			if h.Detected {
				found = append(found, h.Name)
			}
		}
		sort.Strings(found)
		p.done <- found
	}()
	return p
}

// Wait collects the probe, giving up after timeout.
//
// A probe that has not finished in time yields an empty list rather than delaying the
// install. Missing runtime detail on one row is a far better outcome than a user
// staring at a stalled prompt.
func (p *RuntimeProbe) Wait(timeout time.Duration) []string {
	if p == nil {
		return []string{}
	}
	select {
	case found := <-p.done:
		return found
	case <-time.After(timeout):
		return []string{}
	}
}
