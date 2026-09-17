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
func TestPrimeExtensionPathDefaultsToUserLevel(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv(primeAgentDirEnv, "")

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

// Prime Agent's project extension directory keeps the `agent` segment -- `.prime/agent/extensions`
// -- where Pi's and Oh My Pi's project directories drop it. That is the runtime's own shape: its
// CONFIG_DIR_NAME is the literal ".prime/agent" and it joins that whole string under the working
// directory as well as under the home directory. Copying either sibling's layout would write the
// file where Prime Agent does not look, and the install would report success and collect nothing.
func TestPrimeExtensionPathProjectLevelKeepsAgentSegment(t *testing.T) {
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

// PRIME_AGENT_CODING_AGENT_DIR replaces the user agent directory outright. The runtime reads it
// itself, and its own self-update path sets it on the process it re-execs, so an install performed
// from inside such a session has to land where that session will look.
func TestPrimeExtensionPathHonorsTheAgentDirOverride(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	agentDir := filepath.Join(home, "elsewhere", "agent")
	t.Setenv(primeAgentDirEnv, agentDir)

	got, err := PrimeExtensionPath(LevelUser)
	if err != nil {
		t.Fatalf("PrimeExtensionPath returned error: %v", err)
	}
	want := filepath.Join(agentDir, "extensions", "beacon.ts")
	if got != want {
		t.Fatalf("PrimeExtensionPath = %q, want %q -- an install that ignores the override writes "+
			"where the runtime does not look", got, want)
	}
}

// The runtime expands a leading `~` in that variable before using it. An install that did not would
// create a literal `~` directory beside the working directory and report success while Prime Agent
// went on reading the real home.
func TestPrimeExtensionPathExpandsTildeInTheAgentDirOverride(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv(primeAgentDirEnv, "~/custom/agent")

	got, err := PrimeExtensionPath(LevelUser)
	if err != nil {
		t.Fatalf("PrimeExtensionPath returned error: %v", err)
	}
	want := filepath.Join(home, "custom", "agent", "extensions", "beacon.ts")
	if got != want {
		t.Fatalf("PrimeExtensionPath = %q, want %q", got, want)
	}
}

// The override applies to the user root only. The runtime joins the project directory from the
// working directory with no variable consulted at all, so an install that applied it to both would
// miss the project directory entirely -- the same mistake the Oh My Pi path guards against for its
// own config-dir rename.
func TestPrimeAgentDirOverrideDoesNotMoveTheProjectPath(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	testenv.SetHome(t, home)
	t.Chdir(cwd)
	t.Setenv(primeAgentDirEnv, filepath.Join(home, "elsewhere", "agent"))

	got, err := PrimeExtensionPath(LevelProject)
	if err != nil {
		t.Fatalf("PrimeExtensionPath(project) returned error: %v", err)
	}
	want := filepath.Join(cwd, ".prime", "agent", "extensions", "beacon.ts")
	if got != want {
		t.Fatalf("PrimeExtensionPath(project) = %q, want %q", got, want)
	}
}

// PI_CODING_AGENT_DIR is what the same upstream code produces for Pi and Oh My Pi. Prime Agent
// derives its variable from its own package name, so reading Pi's would move this install whenever
// an operator had set it for that product instead.
func TestPrimeExtensionPathIgnoresPisAgentDirOverride(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv(primeAgentDirEnv, "")
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(home, "pi-profile", "agent"))

	got, err := PrimeExtensionPath(LevelUser)
	if err != nil {
		t.Fatalf("PrimeExtensionPath returned error: %v", err)
	}
	want := filepath.Join(home, ".prime", "agent", "extensions", "beacon.ts")
	if got != want {
		t.Fatalf("PrimeExtensionPath = %q, want %q -- another product's variable must not move "+
			"this install", got, want)
	}
}

func TestPrimeExtensionPathRejectsUnknownLevel(t *testing.T) {
	if _, err := PrimeExtensionPath(Level("machine")); err == nil {
		t.Fatal("PrimeExtensionPath accepted an unknown level; a typo in a scope flag must fail " +
			"loudly rather than silently installing at the default scope")
	}
}

// The three pi-family runtimes are separately installed products that one machine can run side by
// side. One writing into another's extension directory would attribute a whole runtime's activity
// to the wrong harness.
func TestPrimePiAndOmpInstallToDifferentDirectories(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	testenv.SetHome(t, home)
	t.Chdir(cwd)
	t.Setenv(primeAgentDirEnv, "")
	t.Setenv(ompAgentDirEnv, "")
	t.Setenv(ompConfigDirEnv, "")

	for _, level := range []Level{LevelUser, LevelProject} {
		prime, err := PrimeExtensionPath(level)
		if err != nil {
			t.Fatal(err)
		}
		pi, err := PiExtensionPath(level)
		if err != nil {
			t.Fatal(err)
		}
		omp, err := OmpExtensionPath(level)
		if err != nil {
			t.Fatal(err)
		}
		if prime == pi || prime == omp {
			t.Fatalf("Prime Agent shares its %s extension path %q with another pi-family runtime", level, prime)
		}
	}
}

// The marker is the only thing that distinguishes a file Beacon may overwrite from one it must
// not, and it is read by three packages. Pinning the literal here means a change to it is a
// deliberate edit to a test rather than a silent break of install/uninstall/status agreement.
func TestPrimeManagedExtensionMarkerIsStable(t *testing.T) {
	if PrimeManagedExtensionMarker != "beacon-managed-prime-extension:v1" {
		t.Fatalf("PrimeManagedExtensionMarker = %q; changing it strands extensions installed by "+
			"earlier builds, which uninstall then refuses to remove", PrimeManagedExtensionMarker)
	}
}

// Each pi-family runtime's marker has to be its own. The three install to different directories
// today, but a shared marker would mean a status or repair pass could not tell which runtime's
// extension it had found if any of them ever moved -- and an uninstall keyed on the wrong marker
// removes the wrong file.
func TestPiFamilyMarkersAreDistinct(t *testing.T) {
	markers := map[string]string{
		"pi":    PiManagedExtensionMarker,
		"omp":   OmpManagedExtensionMarker,
		"prime": PrimeManagedExtensionMarker,
	}
	seen := map[string]string{}
	for runtime, marker := range markers {
		if other, ok := seen[marker]; ok {
			t.Fatalf("%s and %s share the marker %q", runtime, other, marker)
		}
		seen[marker] = runtime
	}
}

// primeRenderedArgv extracts the argv the installer substituted into the extension.
//
// Parsed back out of the source rather than compared as a string: argv is the whole point of this
// template, and a test that compared text would pass on a file the runtime cannot execute.
func primeRenderedArgv(t *testing.T, source string) []string {
	t.Helper()
	matches := regexp.MustCompile(`const beaconArgv: string\[\] = (\[.*\])`).FindStringSubmatch(source)
	if len(matches) != 2 {
		t.Fatalf("rendered extension has no beaconArgv array:\n%s", source)
	}
	var argv []string
	if err := json.Unmarshal([]byte(matches[1]), &argv); err != nil {
		t.Fatalf("beaconArgv is not a JSON array: %v", err)
	}
	return argv
}

func TestInstallPrimeExtensionWritesAnExecutableInvocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beacon.ts")
	binary := "/opt/beacon/bin/beacon-hooks"
	logPath := "/var/log/beacon-agent/runtime.jsonl"

	if err := primeExtension.install(path, binary, logPath, "/etc/beacon/endpoint.yaml"); err != nil {
		t.Fatalf("install returned error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	if !strings.Contains(source, PrimeManagedExtensionMarker) {
		t.Fatal("the installed extension carries no Beacon marker; uninstall would refuse to remove it")
	}

	argv := primeRenderedArgv(t, source)
	if argv[0] != binary {
		t.Fatalf("argv[0] = %q, want the hook binary %q", argv[0], binary)
	}
	if argv[len(argv)-1] != "prime-event" {
		t.Fatalf("argv ends with %q, want prime-event -- any other subcommand attributes Prime "+
			"Agent's payloads to a different runtime", argv[len(argv)-1])
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{"--platform prime", "--log " + logPath, "--config /etc/beacon/endpoint.yaml"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("argv %v is missing %q", argv, want)
		}
	}
}

// A file at this path that Beacon did not write belongs to somebody else. Overwriting it would
// destroy an operator's own extension, and the refusal is what makes the marker load-bearing.
func TestInstallPrimeRefusesToOverwriteAnUnmanagedExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beacon.ts")
	if err := os.WriteFile(path, []byte("export default function () {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := primeExtension.install(path, "/opt/beacon/bin/beacon-hooks", "/tmp/runtime.jsonl", ""); err == nil {
		t.Fatal("install overwrote an extension Beacon did not write")
	}

	data, _ := os.ReadFile(path)
	if string(data) != "export default function () {}\n" {
		t.Fatalf("the operator's extension was modified: %s", data)
	}
}

// Uninstall removes only a file carrying this runtime's marker. A Pi or Oh My Pi extension found at
// this path -- which should not happen, since the three read different directories, but the markers
// are distinct precisely so the answer does not depend on that staying true -- is left alone.
func TestUninstallPrimeLeavesAnotherRuntimesExtensionAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beacon.ts")
	if err := os.WriteFile(path, []byte("// "+PiManagedExtensionMarker+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	removed, err := primeExtension.remove(path)
	if err != nil {
		t.Fatalf("remove returned error: %v", err)
	}
	if removed {
		t.Fatal("uninstalling Prime Agent removed a file carrying Pi's marker")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the Pi extension was deleted: %v", err)
	}
}

// A Windows path is the case that makes argv worth encoding as JSON rather than as a command line.
func TestRenderPrimeExtensionEncodesWindowsPaths(t *testing.T) {
	source, err := primeExtension.render(`C:\Program Files\beacon\beacon-hooks.exe`, `C:\ProgramData\beacon\runtime.jsonl`, "")
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	argv := primeRenderedArgv(t, source)
	if argv[0] != `C:\Program Files\beacon\beacon-hooks.exe` {
		t.Fatalf("argv[0] = %q; the Windows path did not survive rendering", argv[0])
	}
}

// The checked-in extension and the copy embedded in the binary must be byte-identical. They drift
// the moment somebody edits one and forgets `bun run sync`, and the drift is invisible until an
// install ships behavior nobody reviewed.
func TestPrimeEmbeddedExtensionMatchesRootSource(t *testing.T) {
	embedded, err := os.ReadFile(primeEmbeddedExtensionSourcePath())
	if err != nil {
		t.Fatalf("embedded extension source is unreadable: %v", err)
	}
	root, err := os.ReadFile(primeRootExtensionSourcePath())
	if err != nil {
		t.Fatalf("root extension source is unreadable: %v", err)
	}
	if string(embedded) != string(root) {
		t.Fatal("plugins/prime-beacon/src/beacon.ts and its embedded copy have drifted; " +
			"run `bun run sync` in plugins/prime-beacon")
	}
}

// Prime Agent publishes no approval event, and Beacon refuses to synthesize one from a blocking
// tool handler. Asserted against the shipped source so the absence is a property of what installs
// rather than of what somebody remembered not to add.
func TestPrimeExtensionSubscribesToNoApprovalEvent(t *testing.T) {
	for _, forbidden := range []string{"tool_approval_requested", "tool_approval_resolved", "user_python"} {
		if strings.Contains(primeExtension.template, `"`+forbidden+`"`) {
			t.Fatalf("the Prime Agent extension subscribes to %q, which this runtime does not "+
				"publish; those are Oh My Pi's events", forbidden)
		}
	}
}
