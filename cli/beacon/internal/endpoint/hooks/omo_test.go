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
// nothing. The agent-dir override applies to the user root only -- the runtime joins the project
// directory from the working directory with no variable consulted -- so it is set here to prove
// the project path ignores it.
func TestOmoExtensionPathProjectLevelKeepsAgentSegment(t *testing.T) {
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

// OMO_CODING_AGENT_DIR replaces the user agent directory outright; SENPI_CODING_AGENT_DIR and
// PI_CODING_AGENT_DIR are the legacy prefixes Senpi's own brand profile keeps readable when it is
// unset -- upstream Senpi's own name, then pi-mono's, most specific first. An install that ignored
// any of them would land somewhere the runtime does not look.
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
