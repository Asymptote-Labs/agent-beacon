package opencodesession

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionsync/sessionsynctest"
)

// A sync larger than the rotation window used to rotate its own first events away while moving
// every cursor past them, and report success (#973).
func TestCollectOnceKeepsWhatItWritesAcrossTheRotationWindow(t *testing.T) {
	const sessions = 60
	dir := t.TempDir()
	db := newOpenCodeDB(t, filepath.Join(dir, "opencode.db"))
	mustExec(t, db, `DELETE FROM session`)
	for i := 0; i < sessions; i++ {
		id := fmt.Sprintf("ses_%03d", i)
		created := int64(1770000000000 + i*10000)
		mustExec(t, db, `INSERT INTO session (id, title, directory, time_created, time_updated) VALUES (?, 'Fix tests', '/repo', ?, ?)`, id, created, created+5000)
		for p := 0; p < 3; p++ {
			msg := fmt.Sprintf("msg_%03d_%d", i, p)
			at := created + int64(p+1)*1000
			insertJSON(t, db, `INSERT INTO message (id, session_id, time_created, data) VALUES (?, ?, ?, ?)`, msg, id, at, map[string]interface{}{"id": msg, "role": "user"})
			insertJSON(t, db, `INSERT INTO part (id, message_id, session_id, time_created, data) VALUES (?, ?, ?, ?, ?)`, "prt_"+msg, msg, id, at, map[string]interface{}{
				"id": "prt_" + msg, "type": "text", "text": fmt.Sprintf("prompt %d of %s %s", p, id, strings.Repeat("x", 200)),
			})
		}
	}
	opts := CollectOptions{DataDir: dir, Write: true, UserMode: true}
	sessionsynctest.CheckSweepKeepsItsOutput(t, func(statePath, logPath string, rotateBytes int64) (sessionsynctest.Result, error) {
		opts.StatePath, opts.LogPath, opts.RotateBytes = statePath, logPath, rotateBytes
		s, err := CollectOnce(opts)
		return sessionsynctest.Result{EventsEmitted: s.EventsEmitted, Errors: s.Errors, Retention: s.Retention}, err
	})
}
