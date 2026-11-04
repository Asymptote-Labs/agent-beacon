package hooks

import (
	"os"
	"path/filepath"
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
// The agent-dir override applies to the user root only, so it is set here to prove the project
// path ignores it.
func TestPrimeExtensionPathProjectLevelKeepsAgentSegment(t *testing.T) {
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

// PRIME_AGENT_CODING_AGENT_DIR replaces the user agent directory outright, with a leading `~`
// expanded. The runtime reads it itself, and its own self-update path sets it on the process it
// re-execs, so an install performed from inside such a session has to land where that session
// will look.
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

	t.Setenv(primeAgentDirEnv, "~/custom/agent")
	got, err = PrimeExtensionPath(LevelUser)
	if err != nil {
		t.Fatalf("PrimeExtensionPath returned error: %v", err)
	}
	if want := filepath.Join(home, "custom", "agent", "extensions", "beacon.ts"); got != want {
		t.Fatalf("PrimeExtensionPath with ~ = %q, want %q", got, want)
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
