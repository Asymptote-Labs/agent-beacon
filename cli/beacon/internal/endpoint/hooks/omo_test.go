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
func TestOmoExtensionPathDefaultsToUserLevel(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	for _, name := range omoAgentDirEnvNames {
		t.Setenv(name, "")
	}

	want := filepath.Join(home, ".omo", "agent", "extensions", "beacon.ts")
	for _, level := range []Level{"", LevelUser} {
		got, err := OmoExtensionPath(level)
		if err != nil {
			t.Fatalf("OmoExtensionPath(%q) returned error: %v", level, err)
		}
		if got != want {
			t.Fatalf("OmoExtensionPath(%q) = %q, want %q", level, got, want)
		}
	}
}

// Senpi's project extension directory keeps the `agent` segment -- `.omo/agent/extensions` --
// where Pi's and Oh My Pi's project directories drop it, the same shape Prime Agent uses. That is
// the runtime's own layout: its resolveAgentDir joins the literal "agent" segment under the
// working directory as well as under the home directory. Copying either sibling's layout would
// write the file where Senpi does not look, and the install would report success and collect
// nothing.
func TestOmoExtensionPathProjectLevelKeepsAgentSegment(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)

	got, err := OmoExtensionPath(LevelProject)
	if err != nil {
		t.Fatalf("OmoExtensionPath(project) returned error: %v", err)
	}
	want := filepath.Join(cwd, ".omo", "agent", "extensions", "beacon.ts")
	if got != want {
		t.Fatalf("OmoExtensionPath(project) = %q, want %q", got, want)
	}
}

// OMO_CODING_AGENT_DIR replaces the user agent directory outright. The runtime reads it itself, so
// an install has to land where the runtime will look.
func TestOmoExtensionPathHonorsTheAgentDirOverride(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	agentDir := filepath.Join(home, "elsewhere", "agent")
	t.Setenv(omoAgentDirEnv, agentDir)

	got, err := OmoExtensionPath(LevelUser)
	if err != nil {
		t.Fatalf("OmoExtensionPath returned error: %v", err)
	}
	want := filepath.Join(agentDir, "extensions", "beacon.ts")
	if got != want {
		t.Fatalf("OmoExtensionPath = %q, want %q -- an install that ignores the override writes "+
			"where the runtime does not look", got, want)
	}
}

// unsetenv clears an environment variable for the duration of the test, restoring whatever was
// there before. Used instead of t.Setenv(name, "") where the test needs the variable genuinely
// absent: omoAgentDirOverride reads with os.LookupEnv, which treats an explicitly empty value as
// defined -- matching Senpi's own "first DEFINED value" semantics -- so t.Setenv(name, "") would
// stop the fallback chain rather than skip past it.
func unsetenv(t *testing.T, name string) {
	t.Helper()
	previous, had := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("could not unset %s: %v", name, err)
	}
	t.Cleanup(func() {
		if had {
			os.Setenv(name, previous)
		} else {
			os.Unsetenv(name)
		}
	})
}

// SENPI_CODING_AGENT_DIR and PI_CODING_AGENT_DIR are the legacy prefixes Senpi's own brand profile
// keeps readable when OMO_CODING_AGENT_DIR is unset -- upstream Senpi's own name, then pi-mono's,
// most specific first. An install that ignored them would land somewhere the runtime does not look
// for an operator who set one before OMO existed.
func TestOmoExtensionPathFallsBackToLegacyAgentDirOverrides(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	unsetenv(t, omoAgentDirEnv)

	t.Run("senpi", func(t *testing.T) {
		senpiDir := filepath.Join(home, "senpi-profile", "agent")
		t.Setenv(omoAgentDirLegacyEnv, senpiDir)
		unsetenv(t, omoAgentDirPiEnv)

		got, err := OmoExtensionPath(LevelUser)
		if err != nil {
			t.Fatalf("OmoExtensionPath returned error: %v", err)
		}
		want := filepath.Join(senpiDir, "extensions", "beacon.ts")
		if got != want {
			t.Fatalf("OmoExtensionPath = %q, want %q", got, want)
		}
	})

	t.Run("pi", func(t *testing.T) {
		unsetenv(t, omoAgentDirLegacyEnv)
		piDir := filepath.Join(home, "pi-profile", "agent")
		t.Setenv(omoAgentDirPiEnv, piDir)

		got, err := OmoExtensionPath(LevelUser)
		if err != nil {
			t.Fatalf("OmoExtensionPath returned error: %v", err)
		}
		want := filepath.Join(piDir, "extensions", "beacon.ts")
		if got != want {
			t.Fatalf("OmoExtensionPath = %q, want %q", got, want)
		}
	})

	t.Run("omo wins over both legacy names", func(t *testing.T) {
		omoDir := filepath.Join(home, "omo-profile", "agent")
		t.Setenv(omoAgentDirEnv, omoDir)
		t.Setenv(omoAgentDirLegacyEnv, filepath.Join(home, "senpi-profile", "agent"))
		t.Setenv(omoAgentDirPiEnv, filepath.Join(home, "pi-profile", "agent"))

		got, err := OmoExtensionPath(LevelUser)
		if err != nil {
			t.Fatalf("OmoExtensionPath returned error: %v", err)
		}
		want := filepath.Join(omoDir, "extensions", "beacon.ts")
		if got != want {
			t.Fatalf("OmoExtensionPath = %q, want %q -- the OMO-prefixed variable must win over "+
				"either legacy spelling", got, want)
		}
	})
}

// The runtime expands a leading `~` in that variable before using it. An install that did not
// would create a literal `~` directory beside the working directory and report success while
// Senpi went on reading the real home.
func TestOmoExtensionPathExpandsTildeInTheAgentDirOverride(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv(omoAgentDirEnv, "~/custom/agent")

	got, err := OmoExtensionPath(LevelUser)
	if err != nil {
		t.Fatalf("OmoExtensionPath returned error: %v", err)
	}
	want := filepath.Join(home, "custom", "agent", "extensions", "beacon.ts")
	if got != want {
		t.Fatalf("OmoExtensionPath = %q, want %q", got, want)
	}
}

// The override applies to the user root only. The runtime joins the project directory from the
// working directory with no variable consulted at all, so an install that applied it to both
// would miss the project directory entirely.
func TestOmoAgentDirOverrideDoesNotMoveTheProjectPath(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	testenv.SetHome(t, home)
	t.Chdir(cwd)
	t.Setenv(omoAgentDirEnv, filepath.Join(home, "elsewhere", "agent"))

	got, err := OmoExtensionPath(LevelProject)
	if err != nil {
		t.Fatalf("OmoExtensionPath(project) returned error: %v", err)
	}
	want := filepath.Join(cwd, ".omo", "agent", "extensions", "beacon.ts")
	if got != want {
		t.Fatalf("OmoExtensionPath(project) = %q, want %q", got, want)
	}
}

func TestOmoExtensionPathRejectsUnknownLevel(t *testing.T) {
	if _, err := OmoExtensionPath(Level("machine")); err == nil {
		t.Fatal("OmoExtensionPath accepted an unknown level; a typo in a scope flag must fail " +
			"loudly rather than silently installing at the default scope")
	}
}

// The pi-family runtimes are separately installed products that one machine can run side by side.
// One writing into another's extension directory would attribute a whole runtime's activity to the
// wrong harness.
func TestOmoPiAndPrimeInstallToDifferentDirectories(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	testenv.SetHome(t, home)
	t.Chdir(cwd)
	for _, name := range omoAgentDirEnvNames {
		t.Setenv(name, "")
	}
	t.Setenv(primeAgentDirEnv, "")
	t.Setenv(ompAgentDirEnv, "")
	t.Setenv(ompConfigDirEnv, "")

	for _, level := range []Level{LevelUser, LevelProject} {
		omo, err := OmoExtensionPath(level)
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
		prime, err := PrimeExtensionPath(level)
		if err != nil {
			t.Fatal(err)
		}
		if omo == pi || omo == omp || omo == prime {
			t.Fatalf("Senpi shares its %s extension path %q with another pi-family runtime", level, omo)
		}
	}
}

// The marker is the only thing that distinguishes a file Beacon may overwrite from one it must
// not, and it is read by three packages. Pinning the literal here means a change to it is a
// deliberate edit to a test rather than a silent break of install/uninstall/status agreement.
func TestOmoManagedExtensionMarkerIsStable(t *testing.T) {
	if OmoManagedExtensionMarker != "beacon-managed-omo-extension:v1" {
		t.Fatalf("OmoManagedExtensionMarker = %q; changing it strands extensions installed by "+
			"earlier builds, which uninstall then refuses to remove", OmoManagedExtensionMarker)
	}
}

// Each pi-family runtime's marker has to be its own. Extending this table from the existing
// TestPiFamilyMarkersAreDistinct-style check to include Senpi.
func TestOmoMarkerIsDistinctFromOtherPiFamilyMarkers(t *testing.T) {
	markers := map[string]string{
		"pi":    PiManagedExtensionMarker,
		"omp":   OmpManagedExtensionMarker,
		"prime": PrimeManagedExtensionMarker,
		"omo":   OmoManagedExtensionMarker,
	}
	seen := map[string]string{}
	for runtime, marker := range markers {
		if other, ok := seen[marker]; ok {
			t.Fatalf("%s and %s share the marker %q", runtime, other, marker)
		}
		seen[marker] = runtime
	}
}

// omoRenderedArgv extracts the argv the installer substituted into the extension.
//
// Parsed back out of the source rather than compared as a string: argv is the whole point of this
// template, and a test that compared text would pass on a file the runtime cannot execute.
func omoRenderedArgv(t *testing.T, source string) []string {
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

func TestInstallOmoExtensionWritesAnExecutableInvocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beacon.ts")
	binary := "/opt/beacon/bin/beacon-hooks"
	logPath := "/var/log/beacon-agent/runtime.jsonl"

	if err := omoExtension.install(path, binary, logPath, "/etc/beacon/endpoint.yaml"); err != nil {
		t.Fatalf("install returned error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	if !strings.Contains(source, OmoManagedExtensionMarker) {
		t.Fatal("the installed extension carries no Beacon marker; uninstall would refuse to remove it")
	}

	argv := omoRenderedArgv(t, source)
	if argv[0] != binary {
		t.Fatalf("argv[0] = %q, want the hook binary %q", argv[0], binary)
	}
	if argv[len(argv)-1] != "omo-event" {
		t.Fatalf("argv ends with %q, want omo-event -- any other subcommand attributes Senpi's "+
			"payloads to a different runtime", argv[len(argv)-1])
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{"--platform omo", "--log " + logPath, "--config /etc/beacon/endpoint.yaml"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("argv %v is missing %q", argv, want)
		}
	}
}

// A file at this path that Beacon did not write belongs to somebody else. Overwriting it would
// destroy an operator's own extension, and the refusal is what makes the marker load-bearing.
func TestInstallOmoRefusesToOverwriteAnUnmanagedExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beacon.ts")
	if err := os.WriteFile(path, []byte("export default function () {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := omoExtension.install(path, "/opt/beacon/bin/beacon-hooks", "/tmp/runtime.jsonl", ""); err == nil {
		t.Fatal("install overwrote an extension Beacon did not write")
	}

	data, _ := os.ReadFile(path)
	if string(data) != "export default function () {}\n" {
		t.Fatalf("the operator's extension was modified: %s", data)
	}
}

// Uninstall removes only a file carrying this runtime's marker. A Pi, Oh My Pi or Prime Agent
// extension found at this path -- which should not happen, since the four read different
// directories, but the markers are distinct precisely so the answer does not depend on that
// staying true -- is left alone.
func TestUninstallOmoLeavesAnotherRuntimesExtensionAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beacon.ts")
	if err := os.WriteFile(path, []byte("// "+PrimeManagedExtensionMarker+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	removed, err := omoExtension.remove(path)
	if err != nil {
		t.Fatalf("remove returned error: %v", err)
	}
	if removed {
		t.Fatal("uninstalling Senpi removed a file carrying Prime Agent's marker")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the Prime Agent extension was deleted: %v", err)
	}
}

// A Windows path is the case that makes argv worth encoding as JSON rather than as a command line.
func TestRenderOmoExtensionEncodesWindowsPaths(t *testing.T) {
	source, err := omoExtension.render(`C:\Program Files\beacon\beacon-hooks.exe`, `C:\ProgramData\beacon\runtime.jsonl`, "")
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	argv := omoRenderedArgv(t, source)
	if argv[0] != `C:\Program Files\beacon\beacon-hooks.exe` {
		t.Fatalf("argv[0] = %q; the Windows path did not survive rendering", argv[0])
	}
}

// The checked-in extension and the copy embedded in the binary must be byte-identical. They drift
// the moment somebody edits one and forgets `bun run sync`, and the drift is invisible until an
// install ships behavior nobody reviewed.
func TestOmoEmbeddedExtensionMatchesRootSource(t *testing.T) {
	embedded, err := os.ReadFile(omoEmbeddedExtensionSourcePath())
	if err != nil {
		t.Fatalf("embedded extension source is unreadable: %v", err)
	}
	root, err := os.ReadFile(omoRootExtensionSourcePath())
	if err != nil {
		t.Fatalf("root extension source is unreadable: %v", err)
	}
	if string(embedded) != string(root) {
		t.Fatal("plugins/omo-beacon/src/beacon.ts and its embedded copy have drifted; " +
			"run `bun run sync` in plugins/omo-beacon")
	}
}

// Senpi publishes no approval event, and Beacon refuses to synthesize one from a blocking tool
// handler. Asserted against the shipped source so the absence is a property of what installs
// rather than of what somebody remembered not to add.
func TestOmoExtensionSubscribesToNoApprovalEvent(t *testing.T) {
	for _, forbidden := range []string{"tool_approval_requested", "tool_approval_resolved", "user_python"} {
		if strings.Contains(omoExtension.template, `"`+forbidden+`"`) {
			t.Fatalf("the Senpi extension subscribes to %q, which this runtime does not publish; "+
				"those are Oh My Pi's events", forbidden)
		}
	}
}
