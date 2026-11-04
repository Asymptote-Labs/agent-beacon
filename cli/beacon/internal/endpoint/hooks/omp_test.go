package hooks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// clearOmpEnv empties every variable that moves Oh My Pi's user agent directory, so a suite run
// from inside a profiled Oh My Pi session resolves the same paths as one run anywhere else. An
// explicitly empty OMP_PROFILE selects the default profile even when PI_PROFILE is set.
func clearOmpEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{ompAgentDirEnv, ompConfigDirEnv, ompProfileEnv, ompLegacyProfileEnv} {
		t.Setenv(name, "")
	}
}

// An empty level means "the default", and the default has to be the user level. Every caller that
// does not care about scope passes the zero value, so treating "" as unknown would turn status,
// repair and uninstall into errors for the ordinary install.
func TestOmpExtensionPathDefaultsToUserLevel(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	clearOmpEnv(t)

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

// PI_CODING_AGENT_DIR replaces the default profile's agent directory outright. Oh My Pi reads it
// itself, and exports it on its own process when a named profile is active, so a child of a
// profiled session that sees only this variable still lands in that profile's directory.
func TestOmpExtensionPathHonorsTheAgentDirOverride(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	clearOmpEnv(t)
	agentDir := filepath.Join(home, ".omp", "profiles", "work", "agent")
	t.Setenv(ompAgentDirEnv, agentDir)

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
	clearOmpEnv(t)
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
	clearOmpEnv(t)
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

// The user agent directory follows the profile Oh My Pi itself would run under: OMP_PROFILE, else
// PI_PROFILE, with a named profile ignoring PI_CODING_AGENT_DIR. Resolving anything else writes
// the extension, and Beacon Cloud MCP's mcp.json, into a profile the runtime is not using.
func TestOmpAgentDirFollowsTheActiveProfile(t *testing.T) {
	const unset = "\x00unset"
	for _, tc := range []struct {
		name                            string
		ompProfile, piProfile, agentDir string
		configDir                       string
		want                            []string // path under home
	}{
		{name: "OMP_PROFILE", ompProfile: "work", piProfile: unset, want: []string{".omp", "profiles", "work", "agent"}},
		{name: "PI_PROFILE when OMP_PROFILE is unset", ompProfile: unset, piProfile: "work", want: []string{".omp", "profiles", "work", "agent"}},
		{name: "OMP_PROFILE beats PI_PROFILE", ompProfile: "work", piProfile: "home", want: []string{".omp", "profiles", "work", "agent"}},
		{name: "an empty OMP_PROFILE selects the default over PI_PROFILE", ompProfile: "", piProfile: "work", want: []string{".omp", "agent"}},
		{name: "default is the default profile", ompProfile: " default ", piProfile: unset, want: []string{".omp", "agent"}},
		{name: "a named profile ignores the agent-dir override", ompProfile: "work", piProfile: unset, agentDir: "elsewhere", want: []string{".omp", "profiles", "work", "agent"}},
		{name: "a named profile keeps the config-dir rename", ompProfile: "work", piProfile: unset, configDir: ".omp-alt", want: []string{".omp-alt", "profiles", "work", "agent"}},
		// What a child of a `work` session inherits after it sets OMP_PROFILE= to go back to the
		// default: the runtime treats the inherited override as the profile's, not the user's.
		{name: "the default ignores an override that is PI_PROFILE's directory", ompProfile: "", piProfile: "work", agentDir: filepath.Join(".omp", "profiles", "work", "agent"), want: []string{".omp", "agent"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			testenv.SetHome(t, home)
			clearOmpEnv(t)
			for name, value := range map[string]string{ompProfileEnv: tc.ompProfile, ompLegacyProfileEnv: tc.piProfile} {
				if value == unset {
					os.Unsetenv(name)
				} else {
					t.Setenv(name, value)
				}
			}
			if tc.agentDir != "" {
				t.Setenv(ompAgentDirEnv, filepath.Join(home, tc.agentDir))
			}
			t.Setenv(ompConfigDirEnv, tc.configDir)

			got, err := OmpAgentDirForHome(home)
			if err != nil {
				t.Fatal(err)
			}
			if want := filepath.Join(append([]string{home}, tc.want...)...); got != want {
				t.Fatalf("OmpAgentDirForHome = %q, want %q", got, want)
			}
		})
	}
}

// Oh My Pi refuses to start with a profile name it does not accept, so there is no directory to
// write to -- and a name like `../x` would otherwise escape the profiles directory.
func TestOmpAgentDirRejectsAProfileNameOhMyPiRejects(t *testing.T) {
	for _, name := range []string{"../escape", "Work", "a.", "con", "lpt1.txt", "-dash"} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			testenv.SetHome(t, home)
			clearOmpEnv(t)
			t.Setenv(ompProfileEnv, name)
			if got, err := OmpAgentDirForHome(home); err == nil {
				t.Fatalf("OmpAgentDirForHome accepted profile %q and resolved %q", name, got)
			}
		})
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
