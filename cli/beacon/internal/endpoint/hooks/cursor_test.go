package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallCursorHooksJSONPreservesNonBeaconHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	existing := `{"version":1,"hooks":{"sessionStart":[{"command":"echo keep"}]}}`
	if err := os.WriteFile(path, []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}
	if err := installCursorHooksJSON(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "echo keep") {
		t.Fatalf("non-Beacon hook was not preserved: %s", text)
	}
	if !strings.Contains(text, "BEACON_ENDPOINT_MODE=1") {
		t.Fatalf("endpoint env was not added: %s", text)
	}
	if !strings.Contains(text, "BEACON_ENDPOINT_LOG='/tmp/runtime.jsonl'") {
		t.Fatalf("endpoint log env was not added: %s", text)
	}
}

func TestRemoveEndpointHooksPreservesOtherHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	existing := `{"version":1,"hooks":{"sessionStart":[{"command":"echo keep"},{"command":"BEACON_ENDPOINT_MODE=1 beacon-hooks session-start"}]}}`
	if err := os.WriteFile(path, []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}
	changed, err := removeEndpointHooks(path)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected endpoint hook removal")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "echo keep") {
		t.Fatalf("non-Beacon hook was not preserved: %s", text)
	}
	if strings.Contains(text, "BEACON_ENDPOINT_MODE") {
		t.Fatalf("endpoint hook was not removed: %s", text)
	}
}

func TestCursorTargetDirProjectLevel(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	target, err := cursorTargetDir(LevelProject)
	if err != nil {
		t.Fatalf("cursorTargetDir returned error: %v", err)
	}
	if got, want := target, filepath.Join(dir, ".cursor"); got != want {
		t.Fatalf("project target dir = %q, want %q", got, want)
	}
}

func TestReadHooksJSONTreatsCorruptJSONAsEmptyConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	if err := os.WriteFile(path, []byte("{not json"), 0644); err != nil {
		t.Fatalf("write corrupt hooks.json: %v", err)
	}

	hooksJSON, err := readHooksJSON(path)
	if err != nil {
		t.Fatalf("readHooksJSON returned error: %v", err)
	}
	if hooksJSON.Version != 1 {
		t.Fatalf("Version = %d, want 1", hooksJSON.Version)
	}
	if len(hooksJSON.Hooks) != 0 {
		t.Fatalf("Hooks = %#v, want empty map", hooksJSON.Hooks)
	}
}

func TestRemoveEndpointHooksDeletesFileWhenOnlyBeaconHooksRemain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	existing := `{"version":1,"hooks":{"sessionStart":[{"command":"BEACON_ENDPOINT_MODE=1 beacon-hooks session-start"}],"stop":[{"command":"beacon-hooks stop"}]}}`
	if err := os.WriteFile(path, []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}

	changed, err := removeEndpointHooks(path)
	if err != nil {
		t.Fatalf("removeEndpointHooks returned error: %v", err)
	}
	if !changed {
		t.Fatal("expected endpoint hook removal")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("hooks.json still exists or unexpected error: %v", err)
	}
}

func TestInstallCursorHooksJSONReplacesExistingBeaconHook(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	existing := `{"version":1,"hooks":{"preToolUse":[{"command":"BEACON_ENDPOINT_MODE=1 old-beacon-hooks pre-tool"},{"command":"echo keep"}]}}`
	if err := os.WriteFile(path, []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}

	if err := installCursorHooksJSON(path, "/tmp/new beacon-hooks", "/tmp/runtime's.jsonl"); err != nil {
		t.Fatalf("installCursorHooksJSON returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}
	if strings.Contains(string(data), "old-beacon-hooks") {
		t.Fatalf("old endpoint hook was not replaced: %s", string(data))
	}
	if !strings.Contains(string(data), "echo keep") {
		t.Fatalf("non-endpoint hook was not preserved: %s", string(data))
	}
	if !strings.Contains(string(data), "'/tmp/new beacon-hooks'") || !strings.Contains(string(data), "BEACON_ENDPOINT_LOG=") || !strings.Contains(string(data), "runtime") {
		t.Fatalf("command values were not shell-quoted: %s", string(data))
	}
}

func TestInstallCursorWithUnknownLevelFailsBeforeWritingTarget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	status, err := InstallCursor(CursorOptions{Level: Level("workspace"), UserMode: true})
	if err == nil {
		t.Fatal("expected unknown hook level error")
	}
	if status.Installed {
		t.Fatalf("status should not be installed on error: %#v", status)
	}
}
