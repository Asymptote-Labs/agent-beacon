package factorysession

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionsync/sessionsynctest"
)

// A sync larger than the rotation window used to rotate its own first events away while moving
// every cursor past them, and report success (#973).
func TestCollectOnceKeepsWhatItWritesAcrossTheRotationWindow(t *testing.T) {
	const sessions = 60
	dir := t.TempDir()
	root := filepath.Join(dir, "sessions")
	project := filepath.Join(root, "-repo")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < sessions; i++ {
		id := fmt.Sprintf("session-%03d", i)
		writeFile(t, filepath.Join(project, id+".jsonl"),
			`{"type":"session_start","id":"`+id+`","title":"hi","cwd":"/repo"}`+"\n"+
				`{"type":"message","id":"u1","timestamp":"2026-05-16T22:11:04.611Z","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`+"\n"+
				`{"type":"message","id":"a1","timestamp":"2026-05-16T22:11:05.611Z","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`+"\n")
		writeFile(t, filepath.Join(project, id+".settings.json"), `{"model":"claude-opus-4-7","tokenUsage":{"inputTokens":1,"outputTokens":2}}`)
	}
	opts := CollectOptions{SessionsDir: root, Write: true, UserMode: true}
	sessionsynctest.CheckSweepKeepsItsOutput(t, func(statePath, logPath string, rotateBytes int64) (sessionsynctest.Result, error) {
		opts.StatePath, opts.LogPath, opts.RotateBytes = statePath, logPath, rotateBytes
		s, err := CollectOnce(opts)
		return sessionsynctest.Result{EventsEmitted: s.EventsEmitted, Errors: s.Errors, Retention: s.Retention}, err
	})
}
