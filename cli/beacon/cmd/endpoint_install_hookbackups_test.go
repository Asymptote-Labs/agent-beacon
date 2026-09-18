package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
)

// A target that fails part-way must not lose the backups of the targets already rewritten.
func TestEndpointInstallRecordsHookBackupsEvenWhenALaterTargetFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	oldOpts := endpointOpts
	t.Cleanup(func() { endpointOpts = oldOpts })
	endpointOpts.userMode, endpointOpts.systemMode = true, false
	endpointOpts.logPath = filepath.Join(home, ".beacon", "endpoint", "logs", "runtime.jsonl")

	// A manifest, as Install would have left, and pre-existing Codex hooks pointing elsewhere.
	if err := os.MkdirAll(filepath.Join(home, ".beacon", "endpoint"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(home, ".beacon", "endpoint", "install-manifest.json")
	if err := os.WriteFile(manifestPath, []byte(`{"user_mode":true,"files":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	codex := filepath.Join(home, ".codex", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(codex), 0o755); err != nil {
		t.Fatal(err)
	}
	system := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"'/opt/hooks/beacon-hooks' --platform codex --log '/var/log/beacon-agent/runtime.jsonl' codex-session-context"}]}]}}`
	if err := os.WriteFile(codex, []byte(system), 0o600); err != nil {
		t.Fatal(err)
	}

	err := installHookTargetsFromEndpointInstall([]string{"codex", "no-such-harness"})
	if err == nil || !strings.Contains(err.Error(), "no-such-harness") {
		t.Fatalf("the unknown target must still fail the install: %v", err)
	}
	m, err := lifecycle.ReadManifest(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Backups) != 1 || !strings.HasPrefix(m.Backups[0], codex+".beacon.") {
		var raw map[string]any
		data, _ := os.ReadFile(manifestPath)
		_ = json.Unmarshal(data, &raw)
		t.Fatalf("the Codex backup must be registered despite the later failure: %v (%v)", m.Backups, raw)
	}
	kept, _ := os.ReadFile(m.Backups[0])
	if string(kept) != system {
		t.Fatalf("backup must hold the pre-install hooks: %s", kept)
	}
}
