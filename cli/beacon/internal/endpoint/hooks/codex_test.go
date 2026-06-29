package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallCodexHooksUsesInventoryAndUsageSync(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	existing := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo keep"}]}]}}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	if err := installCodexHooks(path, "/tmp/beacon hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installCodexHooks returned error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hooks: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"echo keep",
		"SessionStart",
		"UserPromptSubmit",
		"Stop",
		"SessionEnd",
		"--platform codex",
		"inventory-heartbeat",
		"codex-usage-sync",
		"BEACON_ENDPOINT_LOG='/tmp/runtime.jsonl'",
		"BEACON_ENDPOINT_CONFIG='/tmp/config.json'",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("Codex hooks missing %q:\n%s", want, text)
		}
	}
	for _, forbidden := range []string{"session-start", "prompt-submit", " stop", " session-end"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("Codex hooks should not emit runtime lifecycle events %q:\n%s", forbidden, text)
		}
	}
}

func TestRemoveCodexEndpointHooksPreservesOtherHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	existing := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo keep"}]},{"hooks":[{"type":"command","command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform codex inventory-heartbeat"}]}]}}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	updated, err := removeCodexEndpointHooks(path)
	if err != nil {
		t.Fatalf("removeCodexEndpointHooks returned error: %v", err)
	}
	if !updated {
		t.Fatal("expected Codex endpoint hook to be removed")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hooks: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "echo keep") {
		t.Fatalf("non-Beacon hook was not preserved:\n%s", text)
	}
	if strings.Contains(text, "--platform codex") {
		t.Fatalf("Codex endpoint hook was not removed:\n%s", text)
	}
}
