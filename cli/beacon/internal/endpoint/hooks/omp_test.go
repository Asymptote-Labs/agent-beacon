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
func TestOmpExtensionPathDefaultsToUserLevel(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv(ompAgentDirEnv, "")
	t.Setenv(ompConfigDirEnv, "")

	want := filepath.Join(home, ".omp", "agent", "extensions", "beacon.ts")
	for _, level := range []Level{"", LevelUser} {
		got, err := OmpExtensionPath(level)
		if err != nil {
			t.Fatalf("OmpExtensionPath(%q) returned error: %v", level, err)
		}
		if got != want {
			t.Fatalf("OmpExtensionPath(%q) = %q, want %q", level, got, want)
		}
	}
}

// Oh My Pi's project extension directory is `.omp/extensions`, not `.omp/agent/extensions`: the
// `agent` segment exists only under the home directory. Deriving one path from the other would
// write the project extension where the runtime does not look for it, so the install would report
// success and collect nothing.
func TestOmpExtensionPathProjectLevelOmitsAgentSegment(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)

	got, err := OmpExtensionPath(LevelProject)
	if err != nil {
		t.Fatalf("OmpExtensionPath(project) returned error: %v", err)
	}
	want := filepath.Join(cwd, ".omp", "extensions", "beacon.ts")
	if got != want {
		t.Fatalf("OmpExtensionPath(project) = %q, want %q", got, want)
	}
}

// PI_CODING_AGENT_DIR replaces the agent directory outright. Oh My Pi reads it itself, and sets it
// on its own process when a named profile is active -- so honoring it is how a profiled install
// lands in `~/.omp/profiles/<name>/agent/extensions` without Beacon having to guess a profile name.
func TestOmpExtensionPathHonorsTheAgentDirOverride(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	agentDir := filepath.Join(home, ".omp", "profiles", "work", "agent")
	t.Setenv(ompAgentDirEnv, agentDir)
	t.Setenv(ompConfigDirEnv, "")

	got, err := OmpExtensionPath(LevelUser)
	if err != nil {
		t.Fatalf("OmpExtensionPath returned error: %v", err)
	}
	want := filepath.Join(agentDir, "extensions", "beacon.ts")
	if got != want {
		t.Fatalf("OmpExtensionPath = %q, want %q -- an install that ignores the override writes "+
			"where the runtime does not look", got, want)
	}
}

// PI_CONFIG_DIR renames the `.omp` directory under the home directory only. Applying it to the
// project path too would be a plausible-looking mistake: the runtime joins the literal `.omp` there
// (getProjectAgentDir uses the constant, not the env-aware lookup), so an install that renamed both
// would miss the project directory entirely.
func TestOmpExtensionPathAppliesTheConfigDirOverrideToTheUserPathOnly(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	testenv.SetHome(t, home)
	t.Chdir(cwd)
	t.Setenv(ompAgentDirEnv, "")
	t.Setenv(ompConfigDirEnv, ".omp-alt")

	user, err := OmpExtensionPath(LevelUser)
	if err != nil {
		t.Fatalf("OmpExtensionPath(user) returned error: %v", err)
	}
	if want := filepath.Join(home, ".omp-alt", "agent", "extensions", "beacon.ts"); user != want {
		t.Fatalf("OmpExtensionPath(user) = %q, want %q", user, want)
	}

	project, err := OmpExtensionPath(LevelProject)
	if err != nil {
		t.Fatalf("OmpExtensionPath(project) returned error: %v", err)
	}
	if want := filepath.Join(cwd, ".omp", "extensions", "beacon.ts"); project != want {
		t.Fatalf("OmpExtensionPath(project) = %q, want %q -- PI_CONFIG_DIR does not rename the "+
			"project directory", project, want)
	}
}

// The agent-dir override wins over the config-dir rename, because it replaces the whole path rather
// than one segment of it. Oh My Pi resolves them in that order too.
func TestOmpAgentDirOverrideBeatsTheConfigDirRename(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	agentDir := filepath.Join(home, "elsewhere", "agent")
	t.Setenv(ompAgentDirEnv, agentDir)
	t.Setenv(ompConfigDirEnv, ".omp-alt")

	got, err := OmpExtensionPath(LevelUser)
	if err != nil {
		t.Fatalf("OmpExtensionPath returned error: %v", err)
	}
	if want := filepath.Join(agentDir, "extensions", "beacon.ts"); got != want {
		t.Fatalf("OmpExtensionPath = %q, want %q", got, want)
	}
}

func TestOmpExtensionPathRejectsUnknownLevel(t *testing.T) {
	if _, err := OmpExtensionPath(Level("machine")); err == nil {
		t.Fatal("OmpExtensionPath accepted an unknown level; a typo in a scope flag must fail loudly " +
			"rather than silently installing at the default scope")
	}
}

// Oh My Pi must never install into Pi's directory or vice versa. They are separate products that a
// fleet can run side by side, and one writing into the other's extension directory would attribute
// a whole runtime's activity to the wrong harness.
func TestOmpAndPiInstallToDifferentDirectories(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	testenv.SetHome(t, home)
	t.Chdir(cwd)
	t.Setenv(ompAgentDirEnv, "")
	t.Setenv(ompConfigDirEnv, "")

	for _, level := range []Level{LevelUser, LevelProject} {
		omp, err := OmpExtensionPath(level)
		if err != nil {
			t.Fatal(err)
		}
		pi, err := PiExtensionPath(level)
		if err != nil {
			t.Fatal(err)
		}
		if omp == pi {
			t.Fatalf("Oh My Pi and Pi resolve to the same %s extension path %q", level, omp)
		}
	}
}

// The marker is the only thing that distinguishes a file Beacon may overwrite from one it must
// not, and it is read by three packages. Pinning the literal here means a change to it is a
// deliberate edit to a test rather than a silent break of install/uninstall/status agreement.
func TestOmpManagedExtensionMarkerIsStable(t *testing.T) {
	if OmpManagedExtensionMarker != "beacon-managed-omp-extension:v1" {
		t.Fatalf("OmpManagedExtensionMarker = %q; changing it strands extensions installed by "+
			"earlier builds, which uninstall then refuses to remove", OmpManagedExtensionMarker)
	}
}

// ompRenderedArgv extracts the argv the installer substituted into the extension.
//
// Parsed back out of the source rather than compared as a string: argv is the whole point of this
// template, and a test that compared text would pass on a file the runtime cannot execute.
func ompRenderedArgv(t *testing.T, source string) []string {
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

func TestInstallOmpExtensionWritesAnExecutableInvocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beacon.ts")
	binary := "/opt/beacon/bin/beacon-hooks"
	logPath := "/var/log/beacon-agent/runtime.jsonl"

	if err := ompExtension.install(path, binary, logPath, "/etc/beacon/endpoint.yaml"); err != nil {
		t.Fatalf("install returned error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	if !strings.Contains(source, OmpManagedExtensionMarker) {
		t.Fatal("the installed extension carries no Beacon marker; uninstall would refuse to remove it")
	}

	argv := ompRenderedArgv(t, source)
	if argv[0] != binary {
		t.Fatalf("argv[0] = %q, want the hook binary %q", argv[0], binary)
	}
	if argv[len(argv)-1] != "omp-event" {
		t.Fatalf("argv ends with %q, want omp-event -- any other subcommand sends Oh My Pi's "+
			"payloads to a mapper that does not understand them", argv[len(argv)-1])
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{"--platform omp", "--log " + logPath, "--config /etc/beacon/endpoint.yaml"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("argv %v is missing %q", argv, want)
		}
	}
}

// A Windows path is the case that makes argv worth encoding as JSON rather than as a command line.
func TestRenderOmpExtensionEncodesWindowsPaths(t *testing.T) {
	source, err := ompExtension.render(`C:\Program Files\beacon\beacon-hooks.exe`, `C:\ProgramData\beacon\runtime.jsonl`, "")
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	argv := ompRenderedArgv(t, source)
	if argv[0] != `C:\Program Files\beacon\beacon-hooks.exe` {
		t.Fatalf("argv[0] = %q; the Windows path did not survive rendering", argv[0])
	}
}

// The checked-in extension and the copy embedded in the binary must be byte-identical. They drift
// the moment somebody edits one and forgets `bun run sync`, and the drift is invisible until an
// install ships behavior nobody reviewed.
func TestOmpEmbeddedExtensionMatchesRootSource(t *testing.T) {
	embedded, err := os.ReadFile(ompEmbeddedExtensionSourcePath())
	if err != nil {
		t.Fatalf("embedded extension source is unreadable: %v", err)
	}
	root, err := os.ReadFile(ompRootExtensionSourcePath())
	if err != nil {
		t.Fatalf("root extension source is unreadable: %v", err)
	}
	if string(embedded) != string(root) {
		t.Fatal("plugins/omp-beacon/src/beacon.ts and its embedded copy have drifted; " +
			"run `bun run sync` in plugins/omp-beacon")
	}
}

// The subscription list is the contract between the extension and the omp-event mapper. It is
// asserted here against the shipped source rather than trusted, because a typo on either side
// produces no telemetry rather than an error.
func TestOmpExtensionSubscribesToTheApprovalEvents(t *testing.T) {
	for _, want := range []string{"tool_approval_requested", "tool_approval_resolved", "user_python"} {
		if !strings.Contains(ompExtension.template, `"`+want+`"`) {
			t.Fatalf("the Oh My Pi extension does not subscribe to %q; it is the capability that "+
				"distinguishes this runtime from Pi", want)
		}
	}
	// mcp_notification is deliberately not subscribed to: it is MCP transport plumbing rather than
	// an action the agent took, and it fires for every routine list refresh.
	if strings.Contains(ompExtension.template, `"mcp_notification"`) {
		t.Fatal("the Oh My Pi extension subscribes to mcp_notification; it reports transport " +
			"plumbing rather than agent activity")
	}
}
