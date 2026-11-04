package copilotsession

import (
	"fmt"
	"os"
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
	copilotDir := filepath.Join(dir, ".copilot")
	for i := 0; i < sessions; i++ {
		id := fmt.Sprintf("s%03d", i)
		ts := 1770000000000 + int64(i)*60000
		sessionDir := filepath.Join(copilotDir, "session-state", id)
		if err := os.MkdirAll(sessionDir, 0o755); err != nil {
			t.Fatal(err)
		}
		lines := []string{
			fmt.Sprintf(`{"id":"s1","timestamp":%d,"type":"session.start","data":{"sessionId":%q,"selectedModel":"gpt-5.3-codex","context":{"cwd":"/repo"}}}`, ts, id),
			fmt.Sprintf(`{"id":"u1","timestamp":%d,"type":"user.message","data":{"content":"run tests"}}`, ts+100),
			fmt.Sprintf(`{"id":"a1","timestamp":%d,"type":"assistant.message","data":{"content":"Done.","messageId":"msg1","model":"gpt-5.3-codex","turnId":"turn1"}}`, ts+200),
		}
		if err := os.WriteFile(filepath.Join(sessionDir, EventsFile), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	opts := CollectOptions{CopilotDir: copilotDir, Write: true, UserMode: true}
	sessionsynctest.CheckSweepKeepsItsOutput(t, func(statePath, logPath string, rotateBytes int64) (sessionsynctest.Result, error) {
		opts.StatePath, opts.LogPath, opts.RotateBytes = statePath, logPath, rotateBytes
		s, err := CollectOnce(opts)
		return sessionsynctest.Result{EventsEmitted: s.EventsEmitted, Errors: s.Errors, Retention: s.Retention}, err
	})
}
