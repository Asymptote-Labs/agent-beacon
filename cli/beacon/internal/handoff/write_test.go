package handoff

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

func TestWriteBriefIsPrivateAndNeverOverwrites(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "handoffs")
	brief := BuildBrief(briefSession, []schema.Event{prompt("fix it")}, FromSessionStore, briefNow)
	path, err := WriteBrief(dir, brief)
	if err != nil {
		t.Fatalf("WriteBrief: %v", err)
	}
	if filepath.Dir(path) != dir || filepath.Base(path) != "codex_cli-codex-thread-1-20260925T120000.000Z.md" {
		t.Fatalf("path = %q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != brief.Render() {
		t.Fatalf("written brief differs from Render: %v", err)
	}
	if testenv.HasPOSIXFileModes() {
		assertMode(t, dir, 0o700)
		assertMode(t, path, 0o600)
	}
	if _, err := WriteBrief(dir, brief); err == nil || !strings.Contains(err.Error(), "create handoff brief") {
		t.Fatalf("a second brief for the same session and instant must not overwrite the first, got %v", err)
	}
}

func TestWriteBriefNarrowsAWidenedDirectory(t *testing.T) {
	testenv.RequirePOSIXFileModes(t)
	dir := filepath.Join(t.TempDir(), "handoffs")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteBrief(dir, BuildBrief(briefSession, nil, FromSessionStore, briefNow)); err != nil {
		t.Fatal(err)
	}
	assertMode(t, dir, 0o700)
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}

func TestBriefFileName(t *testing.T) {
	at := time.Date(2026, 9, 25, 12, 30, 45, 678_000_000, time.FixedZone("x", 3600))
	for _, tc := range []struct {
		id, want string
	}{
		{"ses_1", "cline-ses_1-20260925T113045.678Z.md"},
		{"subagent:lead-1:child/../x", "cline-subagent_lead-1_child_.._x-20260925T113045.678Z.md"},
		{"..", "cline-session-20260925T113045.678Z.md"},
		{strings.Repeat("a", 100), "cline-" + strings.Repeat("a", 64) + "-20260925T113045.678Z.md"},
	} {
		if got := BriefFileName(Session{Harness: HarnessCline, ID: tc.id}, at); got != tc.want {
			t.Fatalf("BriefFileName(%q) = %q, want %q", tc.id, got, tc.want)
		}
		if strings.ContainsAny(BriefFileName(Session{Harness: HarnessCline, ID: tc.id}, at), `/\:`) {
			t.Fatalf("file name for %q carries a path separator", tc.id)
		}
	}
}
