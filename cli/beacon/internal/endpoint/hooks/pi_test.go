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
// and collect nothing. PI_CODING_AGENT_DIR moves only the user path, so it is set here to prove
// the project path ignores it.
func TestPiExtensionPathProjectLevelOmitsAgentSegment(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	testenv.SetHome(t, home)
	t.Chdir(cwd)
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(home, "elsewhere", "agent"))

	got, err := PiExtensionPath(LevelProject)
	if err != nil {
		t.Fatalf("PiExtensionPath(project) returned error: %v", err)
	}
	want := filepath.Join(cwd, ".pi", "extensions", "beacon.ts")
	if got != want {
		t.Fatalf("PiExtensionPath(project) = %q, want %q", got, want)
	}
}

// Every pi-family runtime resolves its level in its own switch. A typo in a scope flag must fail
// loudly rather than silently installing at the default scope.
func TestPiFamilyExtensionPathsRejectUnknownLevel(t *testing.T) {
	for name, resolve := range map[string]func(Level) (string, error){
		"pi":    PiExtensionPath,
		"omp":   OmpExtensionPath,
		"omo":   OmoExtensionPath,
		"prime": PrimeExtensionPath,
	} {
		if _, err := resolve(Level("machine")); err == nil {
			t.Errorf("%s extension path accepted an unknown level", name)
		}
	}
}

// The pi-family runtimes are separately installed products that one machine can run side by side.
// One writing into another's extension directory would attribute a whole runtime's activity to the
// wrong harness.
func TestPiFamilyRuntimesInstallToDifferentDirectories(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	testenv.SetHome(t, home)
	t.Chdir(cwd)
	for _, name := range omoAgentDirEnvNames {
		t.Setenv(name, "")
	}
	t.Setenv(primeAgentDirEnv, "")
	clearOmpEnv(t)

	for _, level := range []Level{LevelUser, LevelProject} {
		seen := map[string]string{}
		for name, resolve := range map[string]func(Level) (string, error){
			"pi":    PiExtensionPath,
			"omp":   OmpExtensionPath,
			"omo":   OmoExtensionPath,
			"prime": PrimeExtensionPath,
		} {
			path, err := resolve(level)
			if err != nil {
				t.Fatalf("%s %s extension path: %v", name, level, err)
			}
			if other, ok := seen[path]; ok {
				t.Fatalf("%s and %s share the %s extension path %q", name, other, level, path)
			}
			seen[path] = name
		}
	}
}

// The marker is the only thing that distinguishes a file Beacon may overwrite from one it must
// not, and it is read by three packages. Changing one strands extensions installed by earlier
// builds, which uninstall then refuses to remove.
func TestPiFamilyManagedExtensionMarkersAreStable(t *testing.T) {
	for got, want := range map[string]string{
		PiManagedExtensionMarker:    "beacon-managed-pi-extension:v1",
		OmpManagedExtensionMarker:   "beacon-managed-omp-extension:v1",
		OmoManagedExtensionMarker:   "beacon-managed-omo-extension:v1",
		PrimeManagedExtensionMarker: "beacon-managed-prime-extension:v1",
	} {
		if got != want {
			t.Errorf("managed extension marker = %q, want %q", got, want)
		}
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
	if err := piExtension.install(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
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

func TestRemovePiExtensionIsQuietWhenNothingIsInstalled(t *testing.T) {
	removed, err := piExtension.remove(filepath.Join(t.TempDir(), "absent.ts"))
	if err != nil {
		t.Fatalf("removePiExtension on a missing file returned error: %v", err)
	}
	if removed {
		t.Fatal("removePiExtension reported removing a file that does not exist")
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

// A Windows path is the case a hand-rolled quoting rule gets wrong: the argv must decode back to
// the unescaped path, and the status check must find the binary in its JSON-escaped form.
func TestPiExtensionReferencesBinaryHandlesWindowsPaths(t *testing.T) {
	binary := `C:\Program Files\beacon\beacon-hooks.exe`
	source, err := piExtension.render(binary, `C:\ProgramData\beacon\runtime.jsonl`, "")
	if err != nil {
		t.Fatalf("piExtension.render returned error: %v", err)
	}
	if argv := piRenderedArgv(t, source); argv[0] != binary {
		t.Fatalf("argv[0] = %q, want the unescaped Windows path back", argv[0])
	}
	if !extensionReferencesBinary(source, binary) {
		t.Fatal("a correctly installed Windows extension was reported as not referencing its own binary")
	}
	if extensionReferencesBinary(source, `C:\Other\beacon-hooks.exe`) {
		t.Fatal("piExtensionReferencesBinary matched a binary the extension does not spawn")
	}
	if extensionReferencesBinary(source, "") {
		t.Fatal("piExtensionReferencesBinary matched an empty binary path")
	}
}

// PI_CODING_AGENT_DIR replaces Pi's user agent directory outright, and Beacon's Pi session store
// already reads it. An install that ignored it would write the extension where Pi does not look.
func TestPiExtensionPathHonorsTheAgentDirOverride(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	agentDir := filepath.Join(home, "elsewhere", "agent")
	t.Setenv("PI_CODING_AGENT_DIR", agentDir)

	got, err := PiExtensionPath(LevelUser)
	if err != nil {
		t.Fatalf("PiExtensionPath returned error: %v", err)
	}
	if want := filepath.Join(agentDir, "extensions", "beacon.ts"); got != want {
		t.Fatalf("PiExtensionPath = %q, want %q", got, want)
	}

	t.Setenv("PI_CODING_AGENT_DIR", "~/custom/agent")
	got, err = PiExtensionPath(LevelUser)
	if err != nil {
		t.Fatalf("PiExtensionPath returned error: %v", err)
	}
	if want := filepath.Join(home, "custom", "agent", "extensions", "beacon.ts"); got != want {
		t.Fatalf("PiExtensionPath with ~ = %q, want %q", got, want)
	}
}
