package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallOpenCodePluginWritesManagedPlugin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beacon.ts")
	if err := installOpenCodePlugin(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installOpenCodePlugin returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read plugin: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		opencodeManagedPluginMarker,
		"BeaconEndpointPlugin",
		"BEACON_OPENCODE_DEBUG",
		"BEACON_ENDPOINT_MODE=1",
		"--platform opencode",
		"opencode-event",
		"BEACON_ENDPOINT_LOG='/tmp/runtime.jsonl'",
		"BEACON_ENDPOINT_CONFIG='/tmp/config.json'",
		"forwardedEvents",
		"tool.execute.before",
		"tool.execute.after",
		"message.updated",
		"message.part.updated",
		"session.created",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("plugin missing %q:\n%s", want, text)
		}
	}
}

func TestInstallOpenCodePluginRefusesToOverwriteUnmanagedPlugin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beacon.ts")
	original := "export const UserPlugin = async () => ({})"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	err := installOpenCodePlugin(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json")
	if err == nil || !strings.Contains(err.Error(), "refusing to overwrite unmanaged") {
		t.Fatalf("install error = %v", err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != original {
		t.Fatalf("unmanaged plugin changed: err=%v body=%q", readErr, data)
	}
}

func TestRenderOpenCodePluginRejectsUnresolvedPlaceholders(t *testing.T) {
	_, err := renderOpenCodePluginTemplate("__BEACON_UNKNOWN__", "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json")
	if err == nil {
		t.Fatal("expected unresolved placeholder error")
	}
}

func TestOpenCodeEmbeddedPluginMatchesRootSource(t *testing.T) {
	embedded, err := os.ReadFile(opencodeEmbeddedPluginSourcePath())
	if err != nil {
		t.Fatalf("read embedded plugin source: %v", err)
	}
	root, err := os.ReadFile(opencodeRootPluginSourcePath())
	if err != nil {
		t.Fatalf("read root plugin source: %v", err)
	}
	if string(embedded) != string(root) {
		t.Fatalf("embedded opencode plugin source drifted from root package source")
	}
}

func TestRemoveOpenCodePluginOnlyRemovesManagedPlugin(t *testing.T) {
	dir := t.TempDir()
	userPlugin := filepath.Join(dir, "user.ts")
	if err := os.WriteFile(userPlugin, []byte("export const UserPlugin = async () => ({})"), 0644); err != nil {
		t.Fatalf("write user plugin: %v", err)
	}
	changed, err := removeOpenCodePlugin(userPlugin)
	if err != nil {
		t.Fatalf("removeOpenCodePlugin returned error: %v", err)
	}
	if changed {
		t.Fatal("user plugin should not be removed")
	}
	if _, err := os.Stat(userPlugin); err != nil {
		t.Fatalf("user plugin was removed: %v", err)
	}

	managed := filepath.Join(dir, "beacon.ts")
	if err := installOpenCodePlugin(managed, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installOpenCodePlugin returned error: %v", err)
	}
	changed, err = removeOpenCodePlugin(managed)
	if err != nil {
		t.Fatalf("removeOpenCodePlugin returned error: %v", err)
	}
	if !changed {
		t.Fatal("expected managed plugin removal")
	}
	if _, err := os.Stat(managed); !os.IsNotExist(err) {
		t.Fatalf("managed plugin still exists or unexpected error: %v", err)
	}
}

func TestOpenCodeStatusWarnsWhenHookBinaryMissing(t *testing.T) {
	status := opencodeStatusFromRuntime(runtimeStatus{
		Installed:  true,
		BinaryPath: filepath.Join(t.TempDir(), "missing-beacon-hooks"),
		ConfigPath: "/tmp/beacon.ts",
		Message:    "opencode endpoint hooks are installed",
	})
	if status.Installed {
		t.Fatal("status should not be fully installed when hook binary is missing")
	}
	if !strings.Contains(status.Message, "hook binary is missing") {
		t.Fatalf("status message = %q, want missing binary warning", status.Message)
	}
}

func TestOpenCodeInstalledUsesManagedMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beacon.ts")
	if err := os.WriteFile(path, []byte("BEACON_ENDPOINT_MODE=1 opencode-event"), 0644); err != nil {
		t.Fatalf("write unmarked plugin: %v", err)
	}
	if isOpenCodeInstalledAt(path) {
		t.Fatal("unmarked plugin should not be detected as installed")
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove unmarked plugin: %v", err)
	}
	if err := installOpenCodePlugin(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installOpenCodePlugin returned error: %v", err)
	}
	if !isOpenCodeInstalledAt(path) {
		t.Fatal("managed plugin should be detected by marker")
	}
}

func TestOpenCodePluginPathLevels(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got, want := mustOpenCodePluginPath(t, LevelUser), filepath.Join(home, ".config", "opencode", "plugins", "beacon.ts"); got != want {
		t.Fatalf("user plugin path = %q, want %q", got, want)
	}

	project := t.TempDir()
	t.Chdir(project)
	if got, want := mustOpenCodePluginPath(t, LevelProject), filepath.Join(project, ".opencode", "plugins", "beacon.ts"); got != want {
		t.Fatalf("project plugin path = %q, want %q", got, want)
	}
}

func mustOpenCodePluginPath(t *testing.T, level Level) string {
	t.Helper()
	path, err := opencodePluginPath(level)
	if err != nil {
		t.Fatalf("opencodePluginPath returned error: %v", err)
	}
	return path
}
