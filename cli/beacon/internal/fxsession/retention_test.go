package fxsession

import (
	"fmt"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionsync/sessionsynctest"
)

// A sync larger than the rotation window used to rotate its own first events away while moving
// every cursor past them, and report success (#973).
func TestCollectOnceKeepsWhatItWritesAcrossTheRotationWindow(t *testing.T) {
	const sessions = 60
	f := newSweepFixture(t)
	// An assistant turn maps to many events, so some sweeps stop inside one and the next writes
	// that turn again under the same ids.
	for i := 0; i < sessions; i++ {
		f.writeSessionLog(t, fmt.Sprintf("s%03d", i), []string{sessionStartedLine(1, "/repo"), assistantTurnLine(2), usageCheckpointLine(3, 4200, 880, 0.12)})
	}
	opts := f.options()
	sessionsynctest.CheckSweepKeepsItsOutput(t, func(statePath, logPath string, rotateBytes int64) (sessionsynctest.Result, error) {
		opts.StatePath, opts.LogPath, opts.RotateBytes = statePath, logPath, rotateBytes
		s, err := CollectOnce(opts)
		return sessionsynctest.Result{EventsEmitted: s.EventsEmitted, Errors: s.Errors, Retention: s.Retention}, err
	})
}
