package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/handoff"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

func TestHandoffListIgnoresAmbientRuntimeLog(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	ambient := filepath.Join(home, ".beacon", "endpoint", "logs", "runtime.jsonl")
	data, err := os.ReadFile(handoffLog(t, "gemini_cli", "ambient-session", "synthetic ambient prompt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(ambient), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ambient, data, 0600); err != nil {
		t.Fatal(err)
	}
	stubHandoffSources(t)
	out, _, err := runHandoff(t, "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "ambient-session") {
		t.Fatalf("test read ambient session: %s", out)
	}
}

func TestShareListDoesNotReintroduceStoredSubagent(t *testing.T) {
	env := newShareEnv(t)
	stubHandoffSources(t, stubHandoffSource{harness: handoff.HarnessClaude, sessions: []handoff.Session{{Harness: handoff.HarnessClaude, ID: "child", Subagent: true}}})
	prev := shareOpts
	t.Cleanup(func() { shareOpts = prev })
	shareOpts.logPath = handoffLog(t, "claude_code", "child", "child prompt")
	candidates, err := shareCandidateList(handoffSources(handoff.StoreDirs{}), "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Fatalf("hidden child reappeared: %+v", candidates)
	}
	if err := shareNoSessionError(handoffSources(handoff.StoreDirs{}), "", env.projectDir); strings.Contains(err.Error(), "child") {
		t.Fatalf("hidden child in suggestions: %v", err)
	}
}

func TestShareListSkipsUnreadableStoreWhenEmpty(t *testing.T) {
	newShareEnv(t)
	sentinel := errors.New("synthetic unreadable store")
	stubHandoffSources(t, stubHandoffSource{harness: handoff.HarnessClaude, err: sentinel})
	previous := shareOpts
	t.Cleanup(func() { shareOpts = previous })
	shareOpts.logPath = filepath.Join(t.TempDir(), "absent.jsonl")
	got, err := shareCandidateList(handoffSources(handoff.StoreDirs{}), "", "", 0)
	if len(got) != 0 || err != nil {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestShareListLogErrorCompatibility(t *testing.T) {
	newShareEnv(t)
	previous := shareOpts
	t.Cleanup(func() { shareOpts = previous })
	shareOpts.logPath = t.TempDir() // a directory cannot be read as JSONL
	if _, err := shareCandidateList(nil, "", "", 0); err == nil {
		t.Fatal("missing log read error")
	}
	source := stubHandoffSource{harness: handoff.HarnessClaude, sessions: []handoff.Session{{Harness: handoff.HarnessClaude, ID: "store-only"}}}
	got, err := shareCandidateList([]handoff.Source{source}, "", "", 0)
	if err != nil || len(got) != 1 || got[0].fromLog {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}
