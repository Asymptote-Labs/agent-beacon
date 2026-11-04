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

// sessionIDs lists the sessions in the runtime log in the order their first event was written.
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

// The install backfill (`beacon endpoint backfill`, b1d2b9de) runs this collector with a Since
// window and a byte budget. Neither is enforced by the shared sync machinery: the collector has to
// pass Since to the window filter and Budget to the sink, and treat a spent budget as "come back
// later", not as a failed session. A sweep that ignored either would ship a machine's whole Codex
// history on first connect; one that advanced past the budget would lose sessions for good.
func TestBackfillReadsTheWindowNewestFirstWithinBudgetAndLeavesTheRestForSync(t *testing.T) {
	now := time.Now()
	since := now.Add(-30 * 24 * time.Hour)

	// Learn how many bytes the newest session takes, then give the real sweep exactly that.
	probeDir := t.TempDir()
	probe := writer.NewBudget(1 << 30)
	writeDatedSession(t, filepath.Join(probeDir, ".codex"), "newest", now.Add(-time.Hour))
	if _, err := CollectOnce(CollectOptions{CodexDir: filepath.Join(probeDir, ".codex"), StatePath: filepath.Join(probeDir, "state.json"), LogPath: filepath.Join(probeDir, "runtime.jsonl"), Write: true, UserMode: true, Since: since, Budget: probe}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	codexDir := filepath.Join(dir, ".codex")
	writeDatedSession(t, codexDir, "old", now.Add(-60*24*time.Hour))
	writeDatedSession(t, codexDir, "recent", now.Add(-2*time.Hour))
	writeDatedSession(t, codexDir, "newest", now.Add(-time.Hour))
	statePath := filepath.Join(dir, "state", "codex.json")
	logPath := filepath.Join(dir, "runtime.jsonl")

	summary, err := CollectOnce(CollectOptions{CodexDir: codexDir, StatePath: statePath, LogPath: logPath, Write: true, UserMode: true, Since: since, Budget: writer.NewBudget(probe.Used())})
	if !errors.Is(err, writer.ErrBudgetSpent) {
		t.Fatalf("budgeted backfill = %v, want ErrBudgetSpent", err)
	}
	if summary.Errors != 0 {
		t.Fatalf("a spent budget counted as %d session errors", summary.Errors)
	}
	if summary.Sessions != 2 {
		t.Fatalf("backfill considered %d sessions, want the 2 inside the window", summary.Sessions)
	}
	if got := sessionIDs(t, logPath); len(got) != 1 || got[0] != "newest" {
		t.Fatalf("budgeted backfill wrote %v, want only the newest session", got)
	}

	// A second windowed run picks up where the budget stopped, and still leaves the old session.
	if _, err := CollectOnce(CollectOptions{CodexDir: codexDir, StatePath: statePath, LogPath: logPath, Write: true, UserMode: true, Since: since}); err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if got := sessionIDs(t, logPath); len(got) != 2 || got[1] != "recent" {
		t.Fatalf("second backfill wrote %v, want recent added and old left alone", got)
	}

	// The session outside the window was never consumed: an ordinary sync still reads it, and
	// nothing the backfill wrote is written again.
	if _, err := CollectOnce(CollectOptions{CodexDir: codexDir, StatePath: statePath, LogPath: logPath, Write: true, UserMode: true}); err != nil {
		t.Fatalf("ordinary sync: %v", err)
	}
	if got := sessionIDs(t, logPath); len(got) != 3 || got[2] != "old" {
		t.Fatalf("ordinary sync after the backfill wrote %v, want old last", got)
	}
	perSession := map[string]int{}
	for _, ev := range readLog(t, logPath) {
		perSession[ev.Session.ID]++
	}
	if perSession["newest"] != perSession["recent"] || perSession["recent"] != perSession["old"] {
		t.Fatalf("a session was written twice or partly: %v", perSession)
	}
}
