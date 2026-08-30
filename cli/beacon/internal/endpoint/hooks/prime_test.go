package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// An empty level means "the default", and the default has to be the user level. Every caller that
// does not care about scope passes the zero value, so treating "" as unknown would turn status,
// repair and uninstall into errors for the ordinary install.
func TestPrimeExtensionPathDefaultsToUserLevel(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)

	want := filepath.Join(home, ".prime", "agent", "extensions", "beacon.ts")
	for _, level := range []Level{"", LevelUser} {
		got, err := PrimeExtensionPath(level)
		if err != nil {
			t.Fatalf("PrimeExtensionPath(%q) returned error: %v", level, err)
		}
		if got != want {
			t.Fatalf("PrimeExtensionPath(%q) = %q, want %q", level, got, want)
		}
	}
}

// Prime Agent's project-level directory keeps the full `.prime/agent` config name, unlike Pi, whose
// `agent` segment exists only under the home directory. Deriving one runtime's project path from
// the other's shape would write the file where the runtime does not look for it, so the install
// would report success and collect nothing.
func TestPrimeExtensionPathProjectLevelKeepsTheAgentSegment(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)

	got, err := PrimeExtensionPath(LevelProject)
	if err != nil {
		t.Fatalf("PrimeExtensionPath(project) returned error: %v", err)
	}
	want := filepath.Join(cwd, ".prime", "agent", "extensions", "beacon.ts")
	if got != want {
		t.Fatalf("PrimeExtensionPath(project) = %q, want %q", got, want)
	}
}

func TestPrimeExtensionPathRejectsUnknownLevel(t *testing.T) {
	if _, err := PrimeExtensionPath(Level("machine")); err == nil {
		t.Fatal("PrimeExtensionPath accepted an unknown level; a typo in a scope flag must fail loudly " +
			"rather than silently installing at the default scope")
	}
}

// Two runtimes, two directories, two markers. Pinning the literal means a change to it is a
// deliberate edit to a test rather than a silent break of install/uninstall/status agreement.
func TestPrimeManagedExtensionMarkerIsStable(t *testing.T) {
	if PrimeManagedExtensionMarker != "beacon-managed-prime-extension:v1" {
		t.Fatalf("PrimeManagedExtensionMarker = %q; changing it strands extensions installed by "+
			"earlier builds, which uninstall then refuses to remove", PrimeManagedExtensionMarker)
	}
	if PrimeManagedExtensionMarker == PiManagedExtensionMarker {
		t.Fatal("Prime Agent and Pi share a managed marker; either runtime's uninstall would remove " +
			"the other's extension, and either status would report the other's install as its own")
	}
}

func TestInstallPrimeExtensionWritesManagedExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beacon.ts")
	if err := primeExtension.install(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installPrimeExtension returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read installed extension: %v", err)
	}
	source := string(data)
	if !strings.Contains(source, PrimeManagedExtensionMarker) {
		t.Fatal("installed extension is missing the managed marker, so uninstall would refuse to remove it")
	}
	if strings.Contains(source, "__BEACON_") {
		t.Fatalf("installed extension still contains an unresolved placeholder:\n%s", source)
	}

	argv := piRenderedArgv(t, source)
	want := []string{"/tmp/beacon-hooks", "--platform", "prime", "--log", "/tmp/runtime.jsonl", "--config", "/tmp/config.json"}
	for i, value := range want {
		if i >= len(argv) || argv[i] != value {
			t.Fatalf("rendered argv = %v, want it to begin with %v", argv, want)
		}
	}
	// The subcommand must be last: the hook adapter dispatches on it, and an argv that ends with a
	// flag value instead would spawn the root command and write nothing.
	if argv[len(argv)-1] != "prime-event" {
		t.Fatalf("rendered argv ends with %q, want prime-event", argv[len(argv)-1])
	}
	// The runtime name is what the extension reads to pick its subscription list. Rendered as Pi's,
	// a Prime Agent install would never observe a compaction or a harness refinement.
	if got := renderedRuntimeName(t, source); got != "prime" {
		t.Fatalf("rendered beaconRuntime = %q, want prime", got)
	}
}

// A Windows path is the case that a hand-rolled quoting rule gets wrong, and the case a JSON-encoded
// argv gets right for free.
func TestRenderPrimeExtensionEncodesWindowsPaths(t *testing.T) {
	binary := `C:\Program Files\beacon\beacon-hooks.exe`
	source, err := primeExtension.render(binary, `C:\ProgramData\beacon\runtime.jsonl`, "")
	if err != nil {
		t.Fatalf("renderPrimeExtension returned error: %v", err)
	}
	if argv := piRenderedArgv(t, source); argv[0] != binary {
		t.Fatalf("argv[0] = %q, want the unescaped Windows path back", argv[0])
	}
	if !extensionReferencesBinary(source, binary) {
		t.Fatal("a correctly installed Windows extension was reported as not referencing its own binary")
	}
}

func TestInstallPrimeExtensionRefusesToOverwriteUnmanagedExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beacon.ts")
	// Somebody else's extension that happens to share the filename. Overwriting it would delete
	// their work, and the runtime loads exactly one file per path.
	if err := os.WriteFile(path, []byte("export default function () {}\n"), 0644); err != nil {
		t.Fatalf("seed unmanaged extension: %v", err)
	}
	if err := primeExtension.install(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", ""); err == nil {
		t.Fatal("installPrimeExtension overwrote an unmanaged extension")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "export default function () {}\n" {
		t.Fatalf("unmanaged extension was modified: %q", string(data))
	}
}

// The consequence of the separate markers, asserted from both sides: neither runtime's uninstall
// may remove the other's file, and neither may claim it as installed.
func TestPrimeAndPiExtensionsDoNotClaimEachOther(t *testing.T) {
	dir := t.TempDir()
	primePath := filepath.Join(dir, "prime.ts")
	piPath := filepath.Join(dir, "pi.ts")
	if err := primeExtension.install(primePath, "/tmp/beacon-hooks", "", ""); err != nil {
		t.Fatalf("installPrimeExtension returned error: %v", err)
	}
	if err := piExtension.install(piPath, "/tmp/beacon-hooks", "", ""); err != nil {
		t.Fatalf("installPiExtension returned error: %v", err)
	}

	if piExtension.installedAt(primePath) {
		t.Fatal("Pi claimed Prime Agent's extension as its own install")
	}
	if primeExtension.installedAt(piPath) {
		t.Fatal("Prime Agent claimed Pi's extension as its own install")
	}
	if removed, _ := piExtension.remove(primePath); removed {
		t.Fatal("a Pi uninstall removed Prime Agent's extension")
	}
	if removed, _ := primeExtension.remove(piPath); removed {
		t.Fatal("a Prime Agent uninstall removed Pi's extension")
	}
	for _, path := range []string{primePath, piPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("extension at %s was deleted: %v", path, err)
		}
	}
}

func TestRemovePrimeExtensionOnlyRemovesManagedExtension(t *testing.T) {
	dir := t.TempDir()
	managed := filepath.Join(dir, "managed.ts")
	unmanaged := filepath.Join(dir, "unmanaged.ts")
	if err := primeExtension.install(managed, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", ""); err != nil {
		t.Fatalf("installPrimeExtension returned error: %v", err)
	}
	if err := os.WriteFile(unmanaged, []byte("export default function () {}\n"), 0644); err != nil {
		t.Fatalf("seed unmanaged extension: %v", err)
	}

	removed, err := primeExtension.remove(managed)
	if err != nil || !removed {
		t.Fatalf("primeExtension.remove(managed) = %v, %v; want true, nil", removed, err)
	}
	if _, err := os.Stat(managed); !os.IsNotExist(err) {
		t.Fatal("managed extension survived removal")
	}

	removed, err = primeExtension.remove(unmanaged)
	if err != nil {
		t.Fatalf("primeExtension.remove(unmanaged) returned error: %v", err)
	}
	if removed {
		t.Fatal("removePrimeExtension removed an extension Beacon does not manage")
	}
}

func TestRemovePrimeExtensionIsQuietWhenNothingIsInstalled(t *testing.T) {
	removed, err := primeExtension.remove(filepath.Join(t.TempDir(), "absent.ts"))
	if err != nil {
		t.Fatalf("removePrimeExtension on a missing file returned error: %v", err)
	}
	if removed {
		t.Fatal("removePrimeExtension reported removing a file that does not exist")
	}
}

// The marker alone is not enough to call an install healthy. An extension file outlives the binary
// it was rendered against -- a Beacon uninstall, a partial update, a restored home directory -- and
// in each case the runtime loads an extension that spawns nothing.
func TestPrimeStatusRejectsAnExtensionWithNoBinary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "beacon.ts")
	missing := filepath.Join(dir, "beacon-hooks")
	if err := primeExtension.install(path, missing, "/tmp/runtime.jsonl", ""); err != nil {
		t.Fatalf("installPrimeExtension returned error: %v", err)
	}
	status := primeStatusFromRuntime(runtimeStatus{Installed: true, BinaryPath: missing, ConfigPath: path})
	if status.Installed {
		t.Fatal("status reported installed while the hook binary is absent")
	}
	if !strings.Contains(status.Message, "missing") || !strings.Contains(status.Message, "Prime Agent") {
		t.Fatalf("status message = %q, want it to name Prime Agent and the missing binary", status.Message)
	}
}

func TestPrimeStatusRejectsAnExtensionPointingElsewhere(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "beacon.ts")
	stale := filepath.Join(dir, "old-beacon-hooks")
	current := filepath.Join(dir, "beacon-hooks")
	for _, binary := range []string{stale, current} {
		if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0755); err != nil {
			t.Fatalf("seed binary: %v", err)
		}
	}
	if err := primeExtension.install(path, stale, "/tmp/runtime.jsonl", ""); err != nil {
		t.Fatalf("installPrimeExtension returned error: %v", err)
	}
	status := primeStatusFromRuntime(runtimeStatus{Installed: true, BinaryPath: current, ConfigPath: path})
	if status.Installed {
		t.Fatal("status reported installed while the extension spawns a different binary")
	}
}

func TestPrimeStatusAcceptsAHealthyInstall(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "beacon.ts")
	binary := filepath.Join(dir, "beacon-hooks")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatalf("seed binary: %v", err)
	}
	if err := primeExtension.install(path, binary, "/tmp/runtime.jsonl", ""); err != nil {
		t.Fatalf("installPrimeExtension returned error: %v", err)
	}
	status := primeStatusFromRuntime(runtimeStatus{Installed: true, BinaryPath: binary, ConfigPath: path})
	if !status.Installed {
		t.Fatalf("status reported not installed for a healthy install: %q", status.Message)
	}
	if status.ExtensionPath != path {
		t.Fatalf("status.ExtensionPath = %q, want %q", status.ExtensionPath, path)
	}
}

func TestIsPrimeInstalledAtRequiresTheManagedMarker(t *testing.T) {
	dir := t.TempDir()
	managed := filepath.Join(dir, "managed.ts")
	unmanaged := filepath.Join(dir, "unmanaged.ts")
	if err := primeExtension.install(managed, "/tmp/beacon-hooks", "", ""); err != nil {
		t.Fatalf("installPrimeExtension returned error: %v", err)
	}
	if err := os.WriteFile(unmanaged, []byte("export default function () {}\n"), 0644); err != nil {
		t.Fatalf("seed unmanaged extension: %v", err)
	}
	if !primeExtension.installedAt(managed) {
		t.Fatal("isPrimeInstalledAt did not recognize Beacon's own extension")
	}
	if primeExtension.installedAt(unmanaged) {
		t.Fatal("isPrimeInstalledAt claimed an unmanaged extension as Beacon's")
	}
	if primeExtension.installedAt(filepath.Join(dir, "absent.ts")) {
		t.Fatal("isPrimeInstalledAt claimed a file that does not exist")
	}
}
