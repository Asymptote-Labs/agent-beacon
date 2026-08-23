package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
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

// piRenderedArgv extracts the argv the installer substituted into the extension.
//
// Parsed back out of the source rather than compared as a string: argv is the whole point of this
// install shape, so the test asserts on the decoded array a host would spawn. The optional carriage
// return is load-bearing for the same reason it is in the Cline test -- the extension source is a
// checked-in file, and a Windows checkout converts its line endings.
func piRenderedArgv(t *testing.T, source string) []string {
	t.Helper()
	match := regexp.MustCompile(`(?m)^const beaconArgv: string\[\] = (\[.*?\])\r?$`).FindStringSubmatch(source)
	if len(match) != 2 {
		t.Fatalf("no beaconArgv assignment in rendered extension:\n%s", source)
	}
	var argv []string
	if err := json.Unmarshal([]byte(match[1]), &argv); err != nil {
		t.Fatalf("decode beaconArgv %q: %v", match[1], err)
	}
	return argv
}

func TestInstallPiExtensionWritesManagedExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beacon.ts")
	if err := installPiExtension(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installPiExtension returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read installed extension: %v", err)
	}
	source := string(data)
	if !strings.Contains(source, PiManagedExtensionMarker) {
		t.Fatal("installed extension is missing the managed marker, so uninstall would refuse to remove it")
	}
	if strings.Contains(source, "__BEACON_") {
		t.Fatalf("installed extension still contains an unresolved placeholder:\n%s", source)
	}

	argv := piRenderedArgv(t, source)
	want := []string{"/tmp/beacon-hooks", "--platform", "pi", "--log", "/tmp/runtime.jsonl", "--config", "/tmp/config.json"}
	for i, value := range want {
		if i >= len(argv) || argv[i] != value {
			t.Fatalf("rendered argv = %v, want it to begin with %v", argv, want)
		}
	}
	// The subcommand must be last: the hook adapter dispatches on it, and an argv that ends with a
	// flag value instead would spawn the root command and write nothing.
	if argv[len(argv)-1] != "pi-event" {
		t.Fatalf("rendered argv ends with %q, want pi-event", argv[len(argv)-1])
	}
}

// A Windows path is the case that a hand-rolled quoting rule gets wrong, and the case a JSON-encoded
// argv gets right for free.
func TestRenderPiExtensionEncodesWindowsPaths(t *testing.T) {
	source, err := renderPiExtension(`C:\Program Files\beacon\beacon-hooks.exe`, `C:\ProgramData\beacon\runtime.jsonl`, "")
	if err != nil {
		t.Fatalf("renderPiExtension returned error: %v", err)
	}
	argv := piRenderedArgv(t, source)
	if argv[0] != `C:\Program Files\beacon\beacon-hooks.exe` {
		t.Fatalf("argv[0] = %q, want the unescaped Windows path back", argv[0])
	}
}

func TestInstallPiExtensionRefusesToOverwriteUnmanagedExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beacon.ts")
	// Somebody else's extension that happens to share the filename. Overwriting it would delete
	// their work, and Pi loads exactly one file per path.
	if err := os.WriteFile(path, []byte("export default function () {}\n"), 0644); err != nil {
		t.Fatalf("seed unmanaged extension: %v", err)
	}
	if err := installPiExtension(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", ""); err == nil {
		t.Fatal("installPiExtension overwrote an unmanaged extension")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "export default function () {}\n" {
		t.Fatalf("unmanaged extension was modified: %q", string(data))
	}
}

func TestRemovePiExtensionOnlyRemovesManagedExtension(t *testing.T) {
	dir := t.TempDir()
	managed := filepath.Join(dir, "managed.ts")
	unmanaged := filepath.Join(dir, "unmanaged.ts")
	if err := installPiExtension(managed, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", ""); err != nil {
		t.Fatalf("installPiExtension returned error: %v", err)
	}
	if err := os.WriteFile(unmanaged, []byte("export default function () {}\n"), 0644); err != nil {
		t.Fatalf("seed unmanaged extension: %v", err)
	}

	removed, err := removePiExtension(managed)
	if err != nil || !removed {
		t.Fatalf("removePiExtension(managed) = %v, %v; want true, nil", removed, err)
	}
	if _, err := os.Stat(managed); !os.IsNotExist(err) {
		t.Fatal("managed extension survived removal")
	}

	removed, err = removePiExtension(unmanaged)
	if err != nil {
		t.Fatalf("removePiExtension(unmanaged) returned error: %v", err)
	}
	if removed {
		t.Fatal("removePiExtension removed an extension Beacon does not manage")
	}
	if _, err := os.Stat(unmanaged); err != nil {
		t.Fatalf("unmanaged extension was deleted: %v", err)
	}
}

func TestRemovePiExtensionIsQuietWhenNothingIsInstalled(t *testing.T) {
	removed, err := removePiExtension(filepath.Join(t.TempDir(), "absent.ts"))
	if err != nil {
		t.Fatalf("removePiExtension on a missing file returned error: %v", err)
	}
	if removed {
		t.Fatal("removePiExtension reported removing a file that does not exist")
	}
}

// The placeholder check is what turns a template rename into a loud failure instead of an install
// that reports success and spawns nothing.
func TestRenderPiExtensionRejectsATemplateMissingItsPlaceholder(t *testing.T) {
	if _, err := renderPiExtensionTemplate("// __BEACON_MANAGED_MARKER__\nconst x = 1\n", "/tmp/beacon-hooks", "", ""); err == nil {
		t.Fatal("renderPiExtensionTemplate accepted a template with no argv placeholder")
	}
}

func TestRenderPiExtensionRejectsUnresolvedPlaceholders(t *testing.T) {
	template := "// __BEACON_MANAGED_MARKER__\nconst beaconArgv: string[] = [\"__BEACON_ARGV__\"]\nconst leftover = \"__BEACON_SOMETHING_ELSE__\"\n"
	if _, err := renderPiExtensionTemplate(template, "/tmp/beacon-hooks", "", ""); err == nil {
		t.Fatal("renderPiExtensionTemplate accepted a template with an unresolved placeholder")
	}
}

func TestPiEmbeddedExtensionMatchesRootSource(t *testing.T) {
	embedded, err := os.ReadFile(piEmbeddedExtensionSourcePath())
	if err != nil {
		t.Fatalf("read embedded extension source: %v", err)
	}
	root, err := os.ReadFile(piRootExtensionSourcePath())
	if err != nil {
		t.Fatalf("read root extension source: %v", err)
	}
	if string(embedded) != string(root) {
		t.Fatal("embedded Pi extension source drifted from root package source; run `bun run sync` in plugins/pi-beacon")
	}
}

// The marker alone is not enough to call an install healthy. An extension file outlives the binary
// it was rendered against -- a Beacon uninstall, a partial update, a restored home directory -- and
// in each case Pi loads an extension that spawns nothing.
func TestPiStatusRejectsAnExtensionWithNoBinary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "beacon.ts")
	missing := filepath.Join(dir, "beacon-hooks")
	if err := installPiExtension(path, missing, "/tmp/runtime.jsonl", ""); err != nil {
		t.Fatalf("installPiExtension returned error: %v", err)
	}
	status := piStatusFromRuntime(runtimeStatus{Installed: true, BinaryPath: missing, ConfigPath: path})
	if status.Installed {
		t.Fatal("status reported installed while the hook binary is absent")
	}
	if !strings.Contains(status.Message, "missing") {
		t.Fatalf("status message = %q, want it to name the missing binary", status.Message)
	}
}

func TestPiStatusRejectsAnExtensionPointingElsewhere(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "beacon.ts")
	stale := filepath.Join(dir, "old-beacon-hooks")
	current := filepath.Join(dir, "beacon-hooks")
	for _, binary := range []string{stale, current} {
		if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0755); err != nil {
			t.Fatalf("seed binary: %v", err)
		}
	}
	if err := installPiExtension(path, stale, "/tmp/runtime.jsonl", ""); err != nil {
		t.Fatalf("installPiExtension returned error: %v", err)
	}
	status := piStatusFromRuntime(runtimeStatus{Installed: true, BinaryPath: current, ConfigPath: path})
	if status.Installed {
		t.Fatal("status reported installed while the extension spawns a different binary")
	}
}

func TestPiStatusAcceptsAHealthyInstall(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "beacon.ts")
	binary := filepath.Join(dir, "beacon-hooks")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatalf("seed binary: %v", err)
	}
	if err := installPiExtension(path, binary, "/tmp/runtime.jsonl", ""); err != nil {
		t.Fatalf("installPiExtension returned error: %v", err)
	}
	status := piStatusFromRuntime(runtimeStatus{Installed: true, BinaryPath: binary, ConfigPath: path})
	if !status.Installed {
		t.Fatalf("status reported not installed for a healthy install: %q", status.Message)
	}
	if status.ExtensionPath != path {
		t.Fatalf("status.ExtensionPath = %q, want %q", status.ExtensionPath, path)
	}
}

func TestIsPiInstalledAtRequiresTheManagedMarker(t *testing.T) {
	dir := t.TempDir()
	managed := filepath.Join(dir, "managed.ts")
	unmanaged := filepath.Join(dir, "unmanaged.ts")
	if err := installPiExtension(managed, "/tmp/beacon-hooks", "", ""); err != nil {
		t.Fatalf("installPiExtension returned error: %v", err)
	}
	if err := os.WriteFile(unmanaged, []byte("export default function () {}\n"), 0644); err != nil {
		t.Fatalf("seed unmanaged extension: %v", err)
	}
	if !isPiInstalledAt(managed) {
		t.Fatal("isPiInstalledAt did not recognize Beacon's own extension")
	}
	if isPiInstalledAt(unmanaged) {
		t.Fatal("isPiInstalledAt claimed an unmanaged extension as Beacon's")
	}
	if isPiInstalledAt(filepath.Join(dir, "absent.ts")) {
		t.Fatal("isPiInstalledAt claimed a file that does not exist")
	}
}

func TestPiExtensionReferencesBinaryHandlesWindowsPaths(t *testing.T) {
	binary := `C:\Program Files\beacon\beacon-hooks.exe`
	source, err := renderPiExtension(binary, `C:\ProgramData\beacon\runtime.jsonl`, "")
	if err != nil {
		t.Fatalf("renderPiExtension returned error: %v", err)
	}
	if !piExtensionReferencesBinary(source, binary) {
		t.Fatal("a correctly installed Windows extension was reported as not referencing its own binary")
	}
	if piExtensionReferencesBinary(source, `C:\Other\beacon-hooks.exe`) {
		t.Fatal("piExtensionReferencesBinary matched a binary the extension does not spawn")
	}
	if piExtensionReferencesBinary(source, "") {
		t.Fatal("piExtensionReferencesBinary matched an empty binary path")
	}
}
