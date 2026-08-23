package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// A runtime missing from DiscoverAll is invisible to `beacon endpoint discover`, which is where an
// operator looks to find out what is on the machine. It fails silently: the command succeeds and
// simply never mentions Qwen Code.
func TestDiscoverAllIncludesQwen(t *testing.T) {
	for _, h := range DiscoverAll() {
		if h.Name == "qwen_code" {
			if h.DisplayName != "Qwen Code" {
				t.Errorf("DisplayName = %q, want Qwen Code", h.DisplayName)
			}
			// "hooks", not "otel_env": Qwen Code is a Gemini CLI fork without Gemini's
			// OpenTelemetry export, so there is no endpoint to point at the local collector and
			// `endpoint install --harness qwen` has nothing to configure.
			if h.Capability != "hooks" {
				t.Errorf("Capability = %q, want hooks", h.Capability)
			}
			return
		}
	}
	t.Fatal("DiscoverAll does not include qwen_code; `beacon endpoint discover` would never mention it")
}

// Qwen Code installs through npm, so a version manager, a shell alias or a per-project install all
// hide the binary from the PATH this process inherited while ~/.qwen is still there. Reporting "not
// detected" for a runtime the user is actively using is the failure this guards.
func TestDiscoverQwenDetectsConfigDirectoryWithoutAnExecutable(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("PATH", t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".qwen"), 0755); err != nil {
		t.Fatalf("mkdir qwen config dir: %v", err)
	}

	h := DiscoverQwen()
	if !h.Detected {
		t.Fatalf("DiscoverQwen did not detect Qwen Code from its config directory: %#v", h)
	}
	if h.ExecutablePath != "" {
		t.Fatalf("ExecutablePath = %q, want empty with no binary on PATH", h.ExecutablePath)
	}
	if want := filepath.Join(home, ".qwen", "settings.json"); h.ConfigPath != want {
		t.Fatalf("ConfigPath = %q, want %q", h.ConfigPath, want)
	}
	if h.TelemetryStatus != TelemetryMissing {
		t.Fatalf("TelemetryStatus = %q, want %q with no settings file", h.TelemetryStatus, TelemetryMissing)
	}
}

func TestDiscoverQwenTelemetryStatusVariants(t *testing.T) {
	beaconHook := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"'/tmp/beacon-hooks' --platform qwen stop"}]}]}}`
	cases := []struct {
		name     string
		settings string
		want     TelemetryStatus
		message  string
	}{
		{name: "beacon hooks installed", settings: beaconHook, want: TelemetryEnabled, message: "configured"},
		// Someone else's hooks in the same file. Reporting that as enabled would claim telemetry
		// Beacon is not collecting.
		{name: "only the user's own hooks", settings: `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"my-own.sh"}]}]}}`, want: TelemetryDisabled, message: "were not found"},
		// Qwen's own kill switch. Beacon's hooks are in the file and will never run, which is a
		// different problem from "not installed" and is fixed in Qwen's settings rather than by
		// re-running the installer.
		{name: "disableAllHooks", settings: `{"disableAllHooks":true,` + beaconHook[1:], want: TelemetryDisabled, message: "disableAllHooks"},
		// Misconfigured, not missing: install refuses to touch an unparseable file, and reporting
		// "not found" for a file that is right there sends the user looking in the wrong place.
		{name: "invalid JSON", settings: `{not json`, want: TelemetryMisconfigured, message: "invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			testenv.SetHome(t, home)
			t.Setenv("PATH", t.TempDir())
			path := filepath.Join(home, ".qwen", "settings.json")
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatalf("mkdir qwen config dir: %v", err)
			}
			if err := os.WriteFile(path, []byte(tc.settings), 0600); err != nil {
				t.Fatalf("write qwen settings: %v", err)
			}

			h := DiscoverQwen()
			if h.TelemetryStatus != tc.want {
				t.Fatalf("TelemetryStatus = %q, want %q (message %q)", h.TelemetryStatus, tc.want, h.Message)
			}
			if !strings.Contains(h.Message, tc.message) {
				t.Fatalf("Message = %q, want it to mention %q", h.Message, tc.message)
			}
		})
	}
}

// The user path and the project path differ only by the home prefix -- `~/.qwen/settings.json`
// against `.qwen/settings.json`. So an unresolvable home directory does not merely produce a
// useless path: it produces exactly the path a project install occupies, and discovery would read
// a repository's own settings and report them as the machine's state. DiscoverCline and DiscoverPi
// guard the same way.
func TestDiscoverQwenDoesNotFallBackToTheProjectPath(t *testing.T) {
	testenv.SetHome(t, "")
	t.Setenv("PATH", t.TempDir())

	// A project-shaped settings file in the working directory, which is what a relative path would
	// pick up.
	work := t.TempDir()
	t.Chdir(work)
	path := filepath.Join(work, ".qwen", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir project qwen dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"beacon-hooks --platform qwen stop"}]}]}}`), 0600); err != nil {
		t.Fatalf("write project settings: %v", err)
	}

	h := DiscoverQwen()
	if h.ConfigPath == filepath.Join(".qwen", "settings.json") {
		t.Fatalf("ConfigPath = %q; a relative path reads the current repository as the machine's state", h.ConfigPath)
	}
	if h.TelemetryStatus == TelemetryEnabled {
		t.Fatalf("TelemetryStatus = enabled from a project settings file: %#v", h)
	}
}
