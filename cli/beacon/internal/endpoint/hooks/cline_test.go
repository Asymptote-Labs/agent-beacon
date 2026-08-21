package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// clineRenderedArgv extracts the argv the installer substituted into the plugin.
//
// Parsed back out of the source rather than compared as a string: argv is the whole point of this
// install shape, so the test asserts on the decoded array a host would spawn.
func clineRenderedArgv(t *testing.T, source string) []string {
	t.Helper()
	match := regexp.MustCompile(`(?m)^const beaconArgv: string\[\] = (\[.*\])$`).FindStringSubmatch(source)
	if len(match) != 2 {
		t.Fatalf("no beaconArgv assignment in rendered plugin:\n%s", source)
	}
	var argv []string
	if err := json.Unmarshal([]byte(match[1]), &argv); err != nil {
		t.Fatalf("decode beaconArgv %q: %v", match[1], err)
	}
	return argv
}

func TestInstallClinePluginWritesManagedPlugin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beacon.ts")
	if err := installClinePlugin(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installClinePlugin returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read plugin: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		clineManagedPluginMarker,
		"BeaconEndpointPlugin",
		"BEACON_CLINE_DEBUG",
		"beforeTool",
		"afterTool",
		"beforeRun",
		"afterRun",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("plugin missing %q:\n%s", want, text)
		}
	}

	argv := clineRenderedArgv(t, text)
	if argv[0] != "/tmp/beacon-hooks" {
		t.Errorf("argv[0] = %q, want the hook binary", argv[0])
	}
	// The subcommand is the whole point of the invocation: without it the binary runs its root
	// command, exits, and every Cline event is silently dropped.
	if argv[len(argv)-1] != "cline-event" {
		t.Errorf("argv = %v, want it to end with cline-event", argv)
	}
	for _, want := range [][2]string{
		{"--platform", "cline"},
		{"--log", "/tmp/runtime.jsonl"},
		{"--config", "/tmp/config.json"},
	} {
		if !clineArgvHasFlag(argv, want[0], want[1]) {
			t.Errorf("argv = %v, want %s %s", argv, want[0], want[1])
		}
	}
}

func clineArgvHasFlag(argv []string, flag, value string) bool {
	for i, arg := range argv {
		if arg == flag {
			return i+1 < len(argv) && argv[i+1] == value
		}
	}
	return false
}

// Every path reaches the plugin through JSON encoding, which is why a Windows path needs no
// per-shell quoting: the backslashes and spaces that break a POSIX-quoted command line survive a
// round trip unchanged.
func TestRenderClinePluginEncodesWindowsPaths(t *testing.T) {
	binary := `C:\Program Files\Beacon\hooks\beacon-hooks.exe`
	logPath := `C:\ProgramData\Beacon\logs\runtime.jsonl`
	source, err := renderClinePlugin(binary, logPath, `C:\ProgramData\Beacon\config.json`)
	if err != nil {
		t.Fatalf("renderClinePlugin returned error: %v", err)
	}
	argv := clineRenderedArgv(t, source)
	if argv[0] != binary {
		t.Errorf("argv[0] = %q, want %q", argv[0], binary)
	}
	if !clineArgvHasFlag(argv, "--log", logPath) {
		t.Errorf("argv = %v, want the unmangled log path", argv)
	}
}

func TestInstallClinePluginRefusesToOverwriteUnmanagedPlugin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beacon.ts")
	original := "export default { name: \"someone-elses-plugin\" }"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	err := installClinePlugin(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json")
	if err == nil || !strings.Contains(err.Error(), "refusing to overwrite unmanaged") {
		t.Fatalf("install error = %v, want a refusal", err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != original {
		t.Fatalf("unmanaged plugin changed: err=%v body=%q", readErr, data)
	}
}

func TestRemoveClinePluginOnlyRemovesManagedPlugin(t *testing.T) {
	dir := t.TempDir()
	userPlugin := filepath.Join(dir, "user.ts")
	if err := os.WriteFile(userPlugin, []byte("export default { name: \"mine\" }"), 0644); err != nil {
		t.Fatalf("write user plugin: %v", err)
	}
	changed, err := removeClinePlugin(userPlugin)
	if err != nil {
		t.Fatalf("removeClinePlugin returned error: %v", err)
	}
	if changed {
		t.Fatal("user plugin should not be removed")
	}
	if _, err := os.Stat(userPlugin); err != nil {
		t.Fatalf("user plugin was removed: %v", err)
	}

	managed := filepath.Join(dir, "beacon.ts")
	if err := installClinePlugin(managed, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installClinePlugin returned error: %v", err)
	}
	changed, err = removeClinePlugin(managed)
	if err != nil || !changed {
		t.Fatalf("removeClinePlugin(managed) = %t, %v", changed, err)
	}
	if _, err := os.Stat(managed); !os.IsNotExist(err) {
		t.Fatalf("managed plugin still present: %v", err)
	}
}

func TestRemoveClinePluginIsQuietWhenNothingIsInstalled(t *testing.T) {
	changed, err := removeClinePlugin(filepath.Join(t.TempDir(), "beacon.ts"))
	if err != nil {
		t.Fatalf("removeClinePlugin returned error: %v", err)
	}
	if changed {
		t.Fatal("changed = true with no plugin present")
	}
}

func TestRenderClinePluginRejectsUnresolvedPlaceholders(t *testing.T) {
	if _, err := renderClinePluginTemplate("const x = \"__BEACON_UNKNOWN__\"", "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err == nil {
		t.Fatal("expected an error for a template with no argv placeholder")
	}
	template := clineArgvPlaceholder + "\nconst leftover = \"__BEACON_SOMETHING__\"\n"
	if _, err := renderClinePluginTemplate(template, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err == nil {
		t.Fatal("expected an error for a leftover Beacon placeholder")
	}
}

func TestClineEmbeddedPluginMatchesRootSource(t *testing.T) {
	embedded, err := os.ReadFile(clineEmbeddedPluginSourcePath())
	if err != nil {
		t.Fatalf("read embedded plugin source: %v", err)
	}
	root, err := os.ReadFile(clineRootPluginSourcePath())
	if err != nil {
		t.Fatalf("read root plugin source: %v", err)
	}
	if string(embedded) != string(root) {
		t.Fatalf("embedded Cline plugin source drifted from root package source")
	}
}

func TestClinePluginPathsPerLevel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	userPath, err := clinePluginPath(LevelUser)
	if err != nil {
		t.Fatalf("clinePluginPath(user) returned error: %v", err)
	}
	if want := filepath.Join(home, ".cline", "plugins", "beacon.ts"); userPath != want {
		t.Errorf("user plugin path = %q, want %q", userPath, want)
	}

	projectPath, err := clinePluginPath(LevelProject)
	if err != nil {
		t.Fatalf("clinePluginPath(project) returned error: %v", err)
	}
	if !strings.HasSuffix(projectPath, filepath.Join(".cline", "plugins", "beacon.ts")) {
		t.Errorf("project plugin path = %q, want it under .cline/plugins", projectPath)
	}

	if _, err := clinePluginPath("nonsense"); err == nil {
		t.Error("clinePluginPath accepted an unknown level")
	}
}

// A plugin file outlives a Beacon uninstall, a half-applied update, or a home directory restored
// onto a machine where the binary lives elsewhere. In each case Cline loads a plugin that spawns
// nothing, and reporting that as installed tells an operator telemetry is being collected when none
// is.
func TestClineStatusRejectsAPluginWithNoBinary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "beacon.ts")
	missingBinary := filepath.Join(dir, "absent", "beacon-hooks")
	if err := installClinePlugin(path, missingBinary, "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installClinePlugin returned error: %v", err)
	}

	status := clineStatusFromRuntime(runtimeStatus{Installed: true, BinaryPath: missingBinary, ConfigPath: path})
	if status.Installed {
		t.Error("status reports installed with no hook binary present")
	}
	if !strings.Contains(status.Message, "missing") {
		t.Errorf("message = %q, want it to name the missing binary", status.Message)
	}
}

// A plugin left behind by an older install points at a binary that has since moved. It parses, it
// loads, and it sends nothing.
func TestClineStatusRejectsAPluginPointingElsewhere(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "beacon.ts")
	currentBinary := filepath.Join(dir, "beacon-hooks")
	if err := os.WriteFile(currentBinary, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := installClinePlugin(path, filepath.Join(dir, "old", "beacon-hooks"), "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installClinePlugin returned error: %v", err)
	}

	status := clineStatusFromRuntime(runtimeStatus{Installed: true, BinaryPath: currentBinary, ConfigPath: path})
	if status.Installed {
		t.Error("status reports installed while the plugin points at a different binary")
	}
}

func TestIsClineInstalledAtRequiresTheManagedMarker(t *testing.T) {
	dir := t.TempDir()
	unmanaged := filepath.Join(dir, "other.ts")
	if err := os.WriteFile(unmanaged, []byte("export default {}"), 0644); err != nil {
		t.Fatal(err)
	}
	if isClineInstalledAt(unmanaged) {
		t.Error("an unmanaged plugin was reported as Beacon's")
	}
	if isClineInstalledAt(filepath.Join(dir, "absent.ts")) {
		t.Error("a missing plugin was reported as installed")
	}
}
