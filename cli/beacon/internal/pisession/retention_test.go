package pisession

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
	sessionsDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < sessions; i++ {
		id := fmt.Sprintf("session-%03d", i)
		lines := []string{fmt.Sprintf(`{"type":"session","id":%q,"cwd":"/work/project"}`, id)}
		for p := 0; p < 3; p++ {
			text := fmt.Sprintf("prompt %d of %s %s", p, id, strings.Repeat("x", 200))
			lines = append(lines, fmt.Sprintf(`{"type":"message","message":{"role":"user","content":[{"text":%q}]}}`, text))
		}
		writePiJSONL(t, filepath.Join(sessionsDir, id+".jsonl"), lines)
	}
	opts := CollectOptions{SessionsDir: sessionsDir, Write: true, UserMode: true}
	sessionsynctest.CheckSweepKeepsItsOutput(t, func(statePath, logPath string, rotateBytes int64) (sessionsynctest.Result, error) {
		opts.StatePath, opts.LogPath, opts.RotateBytes = statePath, logPath, rotateBytes
		s, err := CollectOnce(opts)
		return sessionsynctest.Result{EventsEmitted: s.EventsEmitted, Errors: s.Errors, Retention: s.Retention}, err
	})
}
