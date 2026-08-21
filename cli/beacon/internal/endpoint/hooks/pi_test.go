package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// An empty level means "the default", and the default has to be the user level. Every caller that
// does not care about scope passes the zero value, so treating "" as unknown would turn status,
// repair and uninstall into errors for the ordinary install.
func TestPiExtensionPathDefaultsToUserLevel(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)

	want := filepath.Join(home, ".pi", "agent", "extensions", "beacon.ts")
	for _, level := range []Level{"", LevelUser} {
		got, err := PiExtensionPath(level)
		if err != nil {
			t.Fatalf("PiExtensionPath(%q) returned error: %v", level, err)
		}
		if got != want {
			t.Fatalf("PiExtensionPath(%q) = %q, want %q", level, got, want)
		}
	}
}

// Pi's project-level extension directory is `.pi/extensions`, not `.pi/agent/extensions`: the
// `agent` segment exists only under the home directory. Deriving one path from the other would
// write the project extension where Pi does not look for it, so the install would report success
// and collect nothing.
func TestPiExtensionPathProjectLevelOmitsAgentSegment(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)

	got, err := PiExtensionPath(LevelProject)
	if err != nil {
		t.Fatalf("PiExtensionPath(project) returned error: %v", err)
	}
	want := filepath.Join(cwd, ".pi", "extensions", "beacon.ts")
	if got != want {
		t.Fatalf("PiExtensionPath(project) = %q, want %q", got, want)
	}
}

func TestPiExtensionPathRejectsUnknownLevel(t *testing.T) {
	if _, err := PiExtensionPath(Level("machine")); err == nil {
		t.Fatal("PiExtensionPath accepted an unknown level; a typo in a scope flag must fail loudly " +
			"rather than silently installing at the default scope")
	}
}

// The marker is the only thing that distinguishes a file Beacon may overwrite from one it must
// not, and it is read by two other packages. Pinning the literal here means a change to it is a
// deliberate edit to a test rather than a silent break of install/uninstall/status agreement.
func TestPiManagedExtensionMarkerIsStable(t *testing.T) {
	if PiManagedExtensionMarker != "beacon-managed-pi-extension:v1" {
		t.Fatalf("PiManagedExtensionMarker = %q; changing it strands extensions installed by "+
			"earlier builds, which uninstall then refuses to remove", PiManagedExtensionMarker)
	}
}

func TestInstallPiExtensionWritesManagedExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beacon.ts")
	if err := installPiExtension(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installPiExtension returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read extension: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		PiManagedExtensionMarker,
		"beaconEndpointExtension",
		"BEACON_PI_DEBUG",
		`"--platform","pi"`,
		`"pi-event"`,
		`"--log","/tmp/runtime.jsonl"`,
		`"--config","/tmp/config.json"`,
		"session_shutdown",
		"tool_call",
		"tool_result",
		"message_end",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("extension missing %q", want)
		}
	}
}

// The argv is a JSON array, so nothing in the rendered file may be shell-quoted. A single-quoted
// path would be spawned literally, with the quotes as part of the filename.
func TestRenderPiExtensionEmitsArgvNotShellQuoting(t *testing.T) {
	source, err := renderPiExtension("/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json")
	if err != nil {
		t.Fatalf("renderPiExtension returned error: %v", err)
	}
	if strings.Contains(source, "'/tmp/beacon-hooks'") {
		t.Fatal("rendered extension shell-quotes the binary path; argv elements must be literal")
	}
	if !strings.Contains(source, `["/tmp/beacon-hooks","--platform","pi"`) {
		t.Fatalf("rendered extension does not begin the argv with the binary and platform:\n%s", source)
	}
}

// A path containing a space is the ordinary case on Windows (%ProgramFiles%, a user profile) and
// happens on macOS too. It has to survive as one argv element, which is the whole reason this
// runtime uses argv rather than a shell string.
func TestRenderPiExtensionKeepsPathsWithSpacesIntact(t *testing.T) {
	binary := `C:\Program Files\Beacon\beacon-hooks.exe`
	source, err := renderPiExtension(binary, "/tmp/runtime.jsonl", "/tmp/config.json")
	if err != nil {
		t.Fatalf("renderPiExtension returned error: %v", err)
	}
	encoded, err := json.Marshal(binary)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(source, string(encoded)) {
		t.Fatalf("rendered extension does not carry the JSON-encoded binary path:\n%s", source)
	}
	// The rendered argv must still be a parseable JSON array; a raw backslash would break it.
	start := strings.Index(source, "[\"")
	end := strings.Index(source[start:], "]") + start + 1
	var argv []string
	if err := json.Unmarshal([]byte(source[start:end]), &argv); err != nil {
		t.Fatalf("rendered argv is not valid JSON: %v\n%s", err, source[start:end])
	}
	if argv[0] != binary {
		t.Fatalf("argv[0] = %q, want the binary path unchanged", argv[0])
	}
}

func TestInstallPiExtensionRefusesToOverwriteUnmanagedExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beacon.ts")
	original := "export default function (pi) { /* someone else's extension */ }"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	err := installPiExtension(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json")
	if err == nil || !strings.Contains(err.Error(), "refusing to overwrite unmanaged") {
		t.Fatalf("install error = %v, want a refusal", err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != original {
		t.Fatalf("unmanaged extension changed: err=%v body=%q", readErr, data)
	}
}

// Reinstalling over Beacon's own extension must work: that is what repair does, and what an upgrade
// to a new marker version relies on.
func TestInstallPiExtensionOverwritesItsOwnExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beacon.ts")
	if err := installPiExtension(path, "/tmp/beacon-hooks", "/tmp/a.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if err := installPiExtension(path, "/tmp/beacon-hooks", "/tmp/b.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("reinstall over managed extension: %v", err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"--log","/tmp/b.jsonl"`) {
		t.Fatal("reinstall did not update the log path")
	}
}

func TestRenderPiExtensionRejectsUnresolvedPlaceholders(t *testing.T) {
	if _, err := renderPiExtensionTemplate("__BEACON_UNKNOWN__", "/tmp/beacon-hooks", "/tmp/r.jsonl", "/tmp/c.json"); err == nil {
		t.Fatal("renderPiExtensionTemplate accepted a template with an unresolved placeholder; it " +
			"would install a file that spawns a literal placeholder string")
	}
}

func TestRemovePiExtensionOnlyRemovesManagedFiles(t *testing.T) {
	dir := t.TempDir()
	managed := filepath.Join(dir, "managed.ts")
	if err := installPiExtension(managed, "/tmp/beacon-hooks", "/tmp/r.jsonl", "/tmp/c.json"); err != nil {
		t.Fatal(err)
	}
	unmanaged := filepath.Join(dir, "unmanaged.ts")
	if err := os.WriteFile(unmanaged, []byte("export default () => {}"), 0644); err != nil {
		t.Fatal(err)
	}

	removed, err := removePiExtension(managed)
	if err != nil || !removed {
		t.Fatalf("removePiExtension(managed) = %t, %v; want true, nil", removed, err)
	}
	if _, err := os.Stat(managed); !os.IsNotExist(err) {
		t.Fatal("managed extension was not removed")
	}

	removed, err = removePiExtension(unmanaged)
	if err != nil || removed {
		t.Fatalf("removePiExtension(unmanaged) = %t, %v; want false, nil", removed, err)
	}
	if _, err := os.Stat(unmanaged); err != nil {
		t.Fatal("an extension Beacon did not write was removed")
	}
}

// Uninstalling something that was never installed is a normal operation -- `endpoint uninstall`
// runs over every target -- and must not be an error.
func TestRemovePiExtensionIsQuietWhenAbsent(t *testing.T) {
	removed, err := removePiExtension(filepath.Join(t.TempDir(), "missing.ts"))
	if err != nil || removed {
		t.Fatalf("removePiExtension(absent) = %t, %v; want false, nil", removed, err)
	}
}

// Carrying the marker is not enough to be working. An extension pointing at a binary that is gone
// collects nothing, and reporting it as installed is the failure where status is green and the log
// stays empty.
func TestPiStatusReportsNotInstalledWhenBinaryIsMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "beacon.ts")
	missingBinary := filepath.Join(dir, "gone", "beacon-hooks")
	if err := installPiExtension(path, missingBinary, "/tmp/r.jsonl", "/tmp/c.json"); err != nil {
		t.Fatal(err)
	}

	status := piStatusFromRuntime(runtimeStatus{Installed: true, BinaryPath: missingBinary, ConfigPath: path})
	if status.Installed {
		t.Fatal("status reported installed with no hook binary on disk")
	}
	if !strings.Contains(status.Message, "missing") {
		t.Fatalf("message = %q, want it to name the missing binary", status.Message)
	}
}

// An extension left over from an install at a different scope points at that scope's binary. It
// still carries the marker, so only the argv distinguishes it.
func TestPiStatusReportsNotInstalledWhenExtensionNamesAnotherBinary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "beacon.ts")
	active := filepath.Join(dir, "beacon-hooks")
	if err := os.WriteFile(active, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := installPiExtension(path, filepath.Join(dir, "other-beacon-hooks"), "/tmp/r.jsonl", "/tmp/c.json"); err != nil {
		t.Fatal(err)
	}

	status := piStatusFromRuntime(runtimeStatus{Installed: true, BinaryPath: active, ConfigPath: path})
	if status.Installed {
		t.Fatal("status reported installed for an extension naming a different binary")
	}
}

func TestPiStatusReportsInstalledForCurrentExtension(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "beacon.ts")
	binary := filepath.Join(dir, "beacon-hooks")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := installPiExtension(path, binary, "/tmp/r.jsonl", "/tmp/c.json"); err != nil {
		t.Fatal(err)
	}

	status := piStatusFromRuntime(runtimeStatus{Installed: true, BinaryPath: binary, ConfigPath: path, Message: "ok"})
	if !status.Installed {
		t.Fatalf("status reported not installed for a current extension: %+v", status)
	}
	if status.ExtensionPath != path {
		t.Fatalf("ExtensionPath = %q, want %q", status.ExtensionPath, path)
	}
}

// The extension Go embeds and the one the plugin directory tests are the same file. If they drift,
// the tested source is not the installed source -- so the drift is a test failure, not a surprise
// discovered from a user's machine.
func TestPiEmbeddedExtensionMatchesPluginSource(t *testing.T) {
	embedded, err := os.ReadFile(piEmbeddedExtensionSourcePath())
	if err != nil {
		t.Fatalf("read embedded extension: %v", err)
	}
	root, err := os.ReadFile(piRootExtensionSourcePath())
	if err != nil {
		t.Fatalf("read plugin source: %v", err)
	}
	if string(embedded) != string(root) {
		t.Fatal("the embedded Pi extension and plugins/pi-beacon/src/beacon.ts have drifted; " +
			"run `bun run sync` in plugins/pi-beacon")
	}
}

// Install and uninstall through the exported entry points, at the level a user actually gets.
func TestInstallAndUninstallPiRoundTrip(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)

	installed, err := InstallPi(PiOptions{Level: LevelUser, LogPath: filepath.Join(home, "runtime.jsonl"), UserMode: true})
	if err != nil {
		t.Fatalf("InstallPi returned error: %v", err)
	}
	want := filepath.Join(home, ".pi", "agent", "extensions", "beacon.ts")
	if installed.ExtensionPath != want {
		t.Fatalf("ExtensionPath = %q, want %q", installed.ExtensionPath, want)
	}
	if !installed.Installed {
		t.Fatalf("InstallPi reported not installed: %+v", installed)
	}
	if !IsPiInstalled(PiOptions{Level: LevelUser, UserMode: true}) {
		t.Fatal("IsPiInstalled = false right after a successful install")
	}
	if status := PiHookStatus(PiOptions{Level: LevelUser, UserMode: true}); !status.Installed {
		t.Fatalf("PiHookStatus reported not installed: %+v", status)
	}

	removed, err := UninstallPi(PiOptions{Level: LevelUser, UserMode: true})
	if err != nil {
		t.Fatalf("UninstallPi returned error: %v", err)
	}
	if removed.Installed {
		t.Fatalf("UninstallPi reported still installed: %+v", removed)
	}
	if _, err := os.Stat(want); !os.IsNotExist(err) {
		t.Fatal("the extension file survived uninstall")
	}
}
