package clinesession

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
	for i := 0; i < sessions; i++ {
		start := int64(1700000000000 + i*60000)
		f.writeHistoryTask(t, fmt.Sprintf("task-%03d", i), []interface{}{
			userEntry(start, fmt.Sprintf("<task>Update src/file%03d.go</task>", i)),
			assistantEntry(start+1000, "Updated the file"),
		})
	}
	opts := f.options()
	sessionsynctest.CheckSweepKeepsItsOutput(t, func(statePath, logPath string, rotateBytes int64) (sessionsynctest.Result, error) {
		opts.StatePath, opts.LogPath, opts.RotateBytes = statePath, logPath, rotateBytes
		s, err := CollectOnce(opts)
		return sessionsynctest.Result{EventsEmitted: s.EventsEmitted, Errors: s.Errors, Retention: s.Retention}, err
	})
}
