package onboarding

import (
	"testing"
	"time"
)

// The env-answer path completes onboarding with no probe at all.
func TestRuntimeProbeNilIsSafe(t *testing.T) {
	var p *RuntimeProbe
	if got := p.Wait(time.Second); len(got) != 0 {
		t.Fatalf("Wait() on a nil probe = %v, want empty", got)
	}
}
