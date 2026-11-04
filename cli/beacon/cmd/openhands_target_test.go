package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	endpointhooks "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
)

// The install -> status -> uninstall round trip for OpenHands, through the CLI entry points rather
// than the package API.
//
// Wiring a runtime into `beacon endpoint hooks` means five separate switches (install, uninstall,
// status collection, status printing, repair) plus a registry row, and every one of them fails by
// omission rather than by error. TestEveryHookTargetIsWired proves each switch has a case; this
// proves the cases do the thing their name claims.
func TestEndpointHooksInstallAndUninstallOpenHands(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("OH_PERSISTENCE_DIR", "")
	cfg := endpointconfig.Config{LogPath: filepath.Join(home, "runtime.jsonl"), UserMode: true}

	origLevel := endpointOpts.hookLevel
	t.Cleanup(func() { endpointOpts.hookLevel = origLevel })
	endpointOpts.hookLevel = "user"

	hooksPath := filepath.Join(home, ".openhands", "hooks.json")
	if err := installEndpointHookTarget("openhands", cfg); err != nil {
		t.Fatalf("installEndpointHookTarget(openhands) returned error: %v", err)
	}
	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("install did not write %s: %v", hooksPath, err)
	}
	if !strings.Contains(string(data), "--platform openhands") {
		t.Fatalf("hooks.json does not carry the OpenHands hook command:\n%s", data)
	}
	// The file OpenHands reads is the event map itself, and it forbids fields it does not know --
	// so a stray top-level key is not untidy, it is the whole file failing to load.
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("hooks.json is not valid JSON: %v", err)
	}
	for _, event := range []string{"session_start", "user_prompt_submit", "pre_tool_use", "post_tool_use", "stop", "session_end"} {
		if _, ok := document[event]; !ok {
			t.Errorf("hooks.json is missing the %q event:\n%s", event, data)
		}
	}
	if len(document) != 6 {
		t.Fatalf("hooks.json has %d top-level keys, want exactly the six events:\n%s", len(document), data)
	}

	if !endpointhooks.IsOpenHandsInstalled(endpointhooks.OpenHandsOptions{
		Level: endpointhooks.LevelUser, LogPath: cfg.LogPath, UserMode: true,
	}) {
		t.Fatal("IsOpenHandsInstalled = false after installEndpointHookTarget")
	}

	if err := uninstallEndpointHookTarget("openhands", cfg); err != nil {
		t.Fatalf("uninstallEndpointHookTarget(openhands) returned error: %v", err)
	}
	if _, err := os.Stat(hooksPath); !os.IsNotExist(err) {
		t.Fatalf("uninstall left an empty hooks.json behind: %v", err)
	}
}
