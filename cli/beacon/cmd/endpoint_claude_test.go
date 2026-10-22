package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/claudesession"
)

type claudeCommandFixture struct {
	projectsDir string
	statePath   string
	logPath     string
}

func newClaudeCommandFixture(t *testing.T) claudeCommandFixture {
	t.Helper()
	root := t.TempDir()
	f := claudeCommandFixture{
		projectsDir: filepath.Join(root, "projects"),
		statePath:   filepath.Join(root, "state", "claude.json"),
		logPath:     filepath.Join(root, "logs", "runtime.jsonl"),
	}
	session := filepath.Join(f.projectsDir, "-tmp-repo", "sess-1.jsonl")
	if err := os.MkdirAll(filepath.Dir(session), 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"parentUuid":null,"isSidechain":false,"type":"user","message":{"role":"user","content":"hello"},"uuid":"u1","timestamp":"2026-09-19T22:00:00.000Z","cwd":"/tmp/repo","sessionId":"sess-1","version":"2.1.154","gitBranch":"main"}`
	if err := os.WriteFile(session, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Command flags are package-level, so each test sets every one it depends on.
	endpointClaudeOpts.projectsDir = f.projectsDir
	endpointClaudeOpts.statePath = f.statePath
	endpointClaudeOpts.logPath = f.logPath
	endpointClaudeOpts.print = false
	endpointClaudeOpts.watch = false
	endpointClaudeOpts.rotateBytes = 0
	endpointOpts.jsonOutput = false
	endpointOpts.userMode = true
	endpointOpts.systemMode = false
	t.Cleanup(func() {
		endpointClaudeOpts.projectsDir = ""
		endpointClaudeOpts.statePath = ""
		endpointClaudeOpts.logPath = ""
		endpointClaudeOpts.rotateBytes = 0
		endpointOpts.jsonOutput = false
	})
	return f
}

func runClaudeSync(t *testing.T) (string, error) {
	t.Helper()
	var out bytes.Buffer
	endpointClaudeSyncCmd.SetOut(&out)
	endpointClaudeSyncCmd.SetErr(&out)
	t.Cleanup(func() {
		endpointClaudeSyncCmd.SetOut(nil)
		endpointClaudeSyncCmd.SetErr(nil)
	})
	err := runEndpointClaudeSync(endpointClaudeSyncCmd, nil)
	return out.String(), err
}

// A window smaller than the one every other Beacon writer rotates at would only shrink what the
// log keeps, so it is refused before anything is written.
func TestEndpointClaudeSyncRejectsARotateBytesBelowTheDefault(t *testing.T) {
	f := newClaudeCommandFixture(t)
	endpointClaudeOpts.rotateBytes = 1024

	_, err := runClaudeSync(t)
	if err == nil || !strings.Contains(err.Error(), "--rotate-bytes") {
		t.Fatalf("err = %v, want a --rotate-bytes error", err)
	}
	if _, statErr := os.Stat(f.logPath); !os.IsNotExist(statErr) {
		t.Error("a refused --rotate-bytes still wrote the runtime log")
	}
	if _, statErr := os.Stat(f.statePath); !os.IsNotExist(statErr) {
		t.Error("a refused --rotate-bytes still advanced the cursor")
	}
}

func TestReportClaudeSweepJSONCarriesTheRetentionFields(t *testing.T) {
	newClaudeCommandFixture(t)
	endpointOpts.jsonOutput = true
	var out bytes.Buffer
	endpointClaudeSyncCmd.SetOut(&out)
	t.Cleanup(func() { endpointClaudeSyncCmd.SetOut(nil) })

	reportClaudeSweep(endpointClaudeSyncCmd, claudesession.Summary{EventsEmitted: 2, EventsRetained: 2})
	var decoded map[string]any
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("decode %q: %v", out.String(), err)
	}
	for _, key := range []string{"events_emitted", "events_retained", "events_rotated_out", "retention_limited", "sessions_pending"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("JSON summary has no %q: %s", key, out.String())
		}
	}
}
