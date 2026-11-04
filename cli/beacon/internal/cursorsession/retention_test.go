package cursorsession

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionsync/sessionsynctest"
)

// A sync larger than the rotation window used to rotate its own first events away while moving
// every cursor past them, and report success (#973).
func TestCollectOnceKeepsWhatItWritesAcrossTheRotationWindow(t *testing.T) {
	const sessions = 60
	dir := t.TempDir()
	projectsDir := filepath.Join(dir, "projects")
	transcripts := filepath.Join(projectsDir, "-tmp-cursor-project", "agent-transcripts")
	if err := os.MkdirAll(transcripts, 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)
	for i := 0; i < sessions; i++ {
		ts := start.Add(time.Duration(i) * time.Minute)
		lines := []string{
			fmt.Sprintf(`{"type":"user_message","id":"u1","text":"run the tests","timestamp":%q}`, ts.Format(time.RFC3339)),
			fmt.Sprintf(`{"type":"message","id":"a1","message":{"role":"assistant","content":[{"type":"text","text":"Tests are green"}],"model":"claude-sonnet-4"},"timestamp":%q}`, ts.Add(time.Second).Format(time.RFC3339)),
		}
		if err := os.WriteFile(filepath.Join(transcripts, fmt.Sprintf("session-%03d.jsonl", i)), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	opts := CollectOptions{GlobalDBPath: filepath.Join(dir, "missing.vscdb"), ProjectsDir: projectsDir, Write: true, UserMode: true}
	sessionsynctest.CheckSweepKeepsItsOutput(t, func(statePath, logPath string, rotateBytes int64) (sessionsynctest.Result, error) {
		opts.StatePath, opts.LogPath, opts.RotateBytes = statePath, logPath, rotateBytes
		s, err := CollectOnce(opts)
		return sessionsynctest.Result{EventsEmitted: s.EventsEmitted, Errors: s.Errors, Retention: s.Retention}, err
	})
}
