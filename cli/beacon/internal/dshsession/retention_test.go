package dshsession

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionsync/sessionsynctest"
)

// A sync larger than the rotation window used to rotate its own first events away while moving
// every cursor past them, and report success (#973).
func TestCollectOnceKeepsWhatItWritesAcrossTheRotationWindow(t *testing.T) {
	const sessions = 60
	root := t.TempDir()
	for i := 0; i < sessions; i++ {
		id := fmt.Sprintf("s%03d", i)
		writeSession(t, filepath.Join(root, "sessions", id), SessionFileJSON,
			record("session", map[string]interface{}{"id": id, "cwd": "/repo"}),
			record("user/message", map[string]interface{}{"message": map[string]interface{}{"content": "hello"}}),
		)
	}
	opts := CollectOptions{DSHHome: root, Write: true, UserMode: true}
	sessionsynctest.CheckSweepKeepsItsOutput(t, func(statePath, logPath string, rotateBytes int64) (sessionsynctest.Result, error) {
		opts.StatePath, opts.LogPath, opts.RotateBytes = statePath, logPath, rotateBytes
		s, err := CollectOnce(opts)
		return sessionsynctest.Result{EventsEmitted: s.EventsEmitted, Errors: s.Errors, Retention: s.Retention}, err
	})
}
