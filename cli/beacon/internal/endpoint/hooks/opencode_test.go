package hooks

import (
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
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
	// Only the values the installer substitutes are asserted; the template itself is guarded by
	// TestOpenCodeEmbeddedPluginMatchesRootSource.
	for _, want := range []string{
		opencodeManagedPluginMarker,
		"--platform opencode",
		"opencode-event",
		"--log '/tmp/runtime.jsonl'",
		"--config '/tmp/config.json'",
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
	if err == nil {
		t.Fatal("install overwrote an unmanaged plugin")
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

func mustOpenCodePluginPath(t *testing.T, level Level) string {
	t.Helper()
	path, err := opencodePluginPath(level)
	if err != nil {
		t.Fatalf("opencodePluginPath returned error: %v", err)
	}
	return path
}

// OpenCode roots its config directory at $XDG_CONFIG_HOME/opencode when that is set
// (packages/util/src/global-roots.ts). An install that wrote ~/.config/opencode/plugins would report
// success while OpenCode loaded plugins from the moved directory.
func TestOpenCodePluginPathHonorsXDGConfigHome(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	project := t.TempDir()
	t.Chdir(project)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	xdg := filepath.Join(t.TempDir(), "xdg")
	for name, tc := range map[string]struct{ value, want string }{
		"absolute": {xdg, filepath.Join(xdg, "opencode", "plugins", "beacon.ts")},
		"relative": {"rel-xdg", filepath.Join(cwd, "rel-xdg", "opencode", "plugins", "beacon.ts")},
		"empty":    {"", filepath.Join(home, ".config", "opencode", "plugins", "beacon.ts")},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", tc.value)
			if got := mustOpenCodePluginPath(t, LevelUser); got != tc.want {
				t.Fatalf("user plugin path = %q, want %q", got, tc.want)
			}
			// The project plugin never moves.
			if got, want := mustOpenCodePluginPath(t, LevelProject), filepath.Join(cwd, ".opencode", "plugins", "beacon.ts"); got != want {
				t.Fatalf("project plugin path = %q, want %q", got, want)
			}
		})
	}

	// The status check goes through the same resolver as the installer.
	t.Setenv("XDG_CONFIG_HOME", xdg)
	want := filepath.Join(xdg, "opencode", "plugins", "beacon.ts")
	if err := os.MkdirAll(filepath.Dir(want), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte("// "+opencodeManagedPluginMarker), 0600); err != nil {
		t.Fatal(err)
	}
	opts := OpenCodeOptions{Level: LevelUser, UserMode: true}
	if status := OpenCodeHookStatus(opts); status.PluginPath != want {
		t.Fatalf("OpenCodeHookStatus plugin path = %q, want %q", status.PluginPath, want)
	}
	if !IsOpenCodeInstalled(opts) {
		t.Fatalf("IsOpenCodeInstalled = false for a managed plugin at %q", want)
	}
}

// OPENCODE_CONFIG_DIR replaces OpenCode 2.x's global config directory outright, so a plugin
// written under XDG_CONFIG_HOME or ~/.config never loads while it is set.
func TestOpenCodePluginPathHonorsOpenCodeConfigDir(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	t.Chdir(t.TempDir())
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	xdg := filepath.Join(t.TempDir(), "xdg")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	custom := filepath.Join(t.TempDir(), "opencode-config")
	for name, tc := range map[string]struct{ value, want string }{
		"absolute": {custom, filepath.Join(custom, "plugins", "beacon.ts")},
		"relative": {"rel-config", filepath.Join(cwd, "rel-config", "plugins", "beacon.ts")},
		"empty":    {"", filepath.Join(xdg, "opencode", "plugins", "beacon.ts")},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("OPENCODE_CONFIG_DIR", tc.value)
			if got := mustOpenCodePluginPath(t, LevelUser); got != tc.want {
				t.Fatalf("user plugin path = %q, want %q", got, tc.want)
			}
		})
	}
}

// OpenCode 2.x runs only the first plugin with a given ID and drops the rest without a log line, so
// a second Beacon copy (a project install under a user install, or a stale one in plugin/) never
// receives telemetry while its file looks installed.
func TestOpenCodeStatusReportsCopiesOpenCodeNeverRuns(t *testing.T) {
	// The backslash puts the hook binary on a path the plugin's string literal escapes, as every
	// Windows path is.
	home := filepath.Join(t.TempDir(), `back\slash`)
	if err := os.MkdirAll(home, 0755); err != nil {
		t.Fatal(err)
	}
	testenv.SetHome(t, home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	system := t.TempDir()
	defaultSystemDir := opencodeSystemDir
	opencodeSystemDir = func() string { return system }
	t.Cleanup(func() { opencodeSystemDir = defaultSystemDir })
	outer := t.TempDir()
	inner := filepath.Join(outer, "repo")
	if err := os.MkdirAll(inner, 0755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(inner)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	outer = filepath.Dir(cwd)

	binary, err := endpointHookBinaryPath(true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(binary), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("hooks"), 0755); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(home, ".config", "opencode")
	write := func(path string) string {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := installOpenCodePlugin(path, binary, "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
			t.Fatal(err)
		}
		return path
	}
	user := filepath.Join(global, "plugins", "beacon.ts")
	project := write(filepath.Join(cwd, ".opencode", "plugins", "beacon.ts"))
	outerProject := write(filepath.Join(outer, ".opencode", "plugins", "beacon.ts"))
	systemCopy := write(filepath.Join(system, "plugins", "beacon.ts"))

	// Without a user copy, the outermost project directory loads first.
	status := OpenCodeHookStatus(OpenCodeOptions{Level: LevelProject, UserMode: true})
	if status.Installed || !strings.Contains(status.Message, outerProject) {
		t.Fatalf("shadowed project copy reported as %+v", status)
	}

	write(user)
	if got, want := opencodeLoadedPlugins(home, cwd), []string{user, outerProject, project, systemCopy}; strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("load order = %q, want %q", got, want)
	}
	status = OpenCodeHookStatus(OpenCodeOptions{Level: LevelUser, UserMode: true})
	if !status.Installed || strings.Join(status.IgnoredPluginPaths, "\n") != strings.Join([]string{outerProject, project, systemCopy}, "\n") {
		t.Fatalf("running user copy reported as %+v", status)
	}

	// plugin/ sorts before plugins/, so a stale copy there shadows the one Beacon writes.
	stale := filepath.Join(global, "plugin", "beacon.ts")
	if err := os.MkdirAll(filepath.Dir(stale), 0755); err != nil {
		t.Fatal(err)
	}
	if err := installOpenCodePlugin(stale, "/old/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatal(err)
	}
	status = OpenCodeHookStatus(OpenCodeOptions{Level: LevelUser, UserMode: true})
	if status.Installed || !strings.Contains(status.Message, stale) {
		t.Fatalf("user copy shadowed by %s reported as %+v", stale, status)
	}
}

// A Beacon from before OPENCODE_CONFIG_DIR was honored wrote the user plugin to the default
// directory. Nothing reports that copy once Beacon manages the one under the override, yet OpenCode
// 1.x still runs it beside the new one, so install and uninstall both remove it.
func TestOpenCodeInstallRemovesCopyFromOverriddenDefaultDir(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Chdir(t.TempDir())
	stale := filepath.Join(home, ".config", "opencode", "plugins", "beacon.ts")
	leaveStale := func() {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(stale), 0755); err != nil {
			t.Fatal(err)
		}
		if err := installOpenCodePlugin(stale, "/old/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
			t.Fatal(err)
		}
	}
	custom := filepath.Join(t.TempDir(), "custom")
	t.Setenv("OPENCODE_CONFIG_DIR", custom)
	opts := OpenCodeOptions{Level: LevelUser, LogPath: filepath.Join(home, "runtime.jsonl"), UserMode: true}

	leaveStale()
	status, err := InstallOpenCode(opts)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(custom, "plugins", "beacon.ts"); !status.Installed || status.PluginPath != want || status.RemovedPluginPath != stale {
		t.Fatalf("install = %+v, want installed at %s and %s removed", status, want, stale)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("install left %s: %v", stale, err)
	}

	leaveStale()
	if status, err = UninstallOpenCode(opts); err != nil {
		t.Fatal(err)
	}
	if status.RemovedPluginPath != stale || !strings.Contains(status.Message, stale) {
		t.Fatalf("uninstall = %+v, want %s removed", status, stale)
	}
	for _, path := range []string{stale, status.PluginPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("uninstall left %s: %v", path, err)
		}
	}
}
