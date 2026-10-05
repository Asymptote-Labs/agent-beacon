package codexsession

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
)

// writeDatedSession writes a one-prompt rollout for id and sets its modification time to at.
func writeDatedSession(t *testing.T, codexDir, id string, at time.Time) {
	t.Helper()
	path := filepath.Join(codexDir, "sessions", "2026", "09", "19", "rollout-2026-09-19T20-00-00-"+id+".jsonl")
	writeFile(t, path, sessionMetaLine(id, "/tmp/repo")+"\n"+messageLine("user", id+"-u1", "prompt for "+id)+"\n")
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func sessionIDs(t *testing.T, logPath string) []string {
	t.Helper()
	var ids []string
	seen := map[string]bool{}
	for _, ev := range readLog(t, logPath) {
		if ev.Session == nil || seen[ev.Session.ID] {
			continue
		}
		seen[ev.Session.ID] = true
		ids = append(ids, ev.Session.ID)
	}
	return ids
}

func TestCollectOnceSinceReadsRecentSessionsNewestFirstAndLeavesOlderOnes(t *testing.T) {
	dir := t.TempDir()
	codexDir := filepath.Join(dir, ".codex")
	now := time.Now()
	writeDatedSession(t, codexDir, "old", now.Add(-60*24*time.Hour))
	writeDatedSession(t, codexDir, "recent", now.Add(-2*time.Hour))
	writeDatedSession(t, codexDir, "newest", now.Add(-time.Hour))
	statePath := filepath.Join(dir, "state", "codex.json")
	logPath := filepath.Join(dir, "runtime.jsonl")

	opts := CollectOptions{CodexDir: codexDir, StatePath: statePath, LogPath: logPath, Write: true, UserMode: true, Since: now.Add(-30 * 24 * time.Hour)}
	if _, err := CollectOnce(opts); err != nil {
		t.Fatalf("windowed CollectOnce: %v", err)
	}
	if got := sessionIDs(t, logPath); len(got) != 2 || got[0] != "newest" || got[1] != "recent" {
		t.Fatalf("windowed sweep read sessions %v, want [newest recent]", got)
	}

	// The session outside the window was never consumed: an ordinary sync still reads it.
	if _, err := CollectOnce(CollectOptions{CodexDir: codexDir, StatePath: statePath, LogPath: logPath, Write: true, UserMode: true}); err != nil {
		t.Fatalf("ordinary CollectOnce: %v", err)
	}
	if got := sessionIDs(t, logPath); len(got) != 3 || got[2] != "old" {
		t.Fatalf("ordinary sweep after the window read %v, want old last", got)
	}
}

func TestCollectOnceStopsAtTheBudgetAndLeavesTheRestForTheNextSweep(t *testing.T) {
	dir := t.TempDir()
	codexDir := filepath.Join(dir, ".codex")
	now := time.Now()
	writeDatedSession(t, codexDir, "first", now.Add(-time.Hour))
	writeDatedSession(t, codexDir, "second", now.Add(-2*time.Hour))
	statePath := filepath.Join(dir, "state", "codex.json")
	logPath := filepath.Join(dir, "runtime.jsonl")

	// Learn how many bytes the newest session takes, then give a fresh sweep exactly that.
	probeDir := t.TempDir()
	probe := writer.NewBudget(1 << 30)
	writeDatedSession(t, filepath.Join(probeDir, ".codex"), "first", now.Add(-time.Hour))
	if _, err := CollectOnce(CollectOptions{CodexDir: filepath.Join(probeDir, ".codex"), StatePath: filepath.Join(probeDir, "state.json"), LogPath: filepath.Join(probeDir, "runtime.jsonl"), Write: true, UserMode: true, Since: now.Add(-24 * time.Hour), Budget: probe}); err != nil {
		t.Fatal(err)
	}

	budget := writer.NewBudget(probe.Used())
	summary, err := CollectOnce(CollectOptions{CodexDir: codexDir, StatePath: statePath, LogPath: logPath, Write: true, UserMode: true, Since: now.Add(-24 * time.Hour), Budget: budget})
	if !errors.Is(err, writer.ErrBudgetSpent) {
		t.Fatalf("budgeted CollectOnce = %v, want ErrBudgetSpent", err)
	}
	if summary.Errors != 0 {
		t.Fatalf("a spent budget counted as %d session errors", summary.Errors)
	}
	if got := sessionIDs(t, logPath); len(got) != 1 || got[0] != "first" {
		t.Fatalf("budgeted sweep read %v, want only the newest session", got)
	}

	if _, err := CollectOnce(CollectOptions{CodexDir: codexDir, StatePath: statePath, LogPath: logPath, Write: true, UserMode: true}); err != nil {
		t.Fatalf("follow-up CollectOnce: %v", err)
	}
	events := readLog(t, logPath)
	if got := sessionIDs(t, logPath); len(got) != 2 || got[1] != "second" {
		t.Fatalf("follow-up sweep read %v, want second added", got)
	}
	perSession := map[string]int{}
	for _, ev := range events {
		perSession[ev.Session.ID]++
	}
	if perSession["first"] != perSession["second"] {
		t.Fatalf("a session was written twice or partly: %v", perSession)
	}
}
