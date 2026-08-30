package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	endpointhooks "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// The spellings an operator actually types. "omp" is the binary and the canonical harness name;
// "oh-my-pi" is the repository name people reach for; the underscore form arrives from anyone
// copying a harness name out of a log, since normalizeHarnessKey maps `_` to `-`.
func TestOmpHookTargetAliases(t *testing.T) {
	for _, in := range []string{"omp", "OMP", "  omp  ", "oh-my-pi", "oh_my_pi", "ohmypi", "Oh-My-Pi"} {
		t.Run(in, func(t *testing.T) {
			got, ok := normalizeHookTarget(in)
			if !ok {
				t.Fatalf("normalizeHookTarget(%q) was not recognized", in)
			}
			if got != "omp" {
				t.Fatalf("normalizeHookTarget(%q) = %q, want omp", in, got)
			}
		})
	}
}

// Oh My Pi and Pi are separately installed products. A `--harness` value for one must never resolve
// to the other, or an operator asking to instrument one gets the other's extension written into a
// directory the runtime they meant does not read.
func TestOmpAndPiTargetsDoNotAlias(t *testing.T) {
	for in, want := range map[string]string{
		"pi":       "pi",
		"pi-cli":   "pi",
		"pi_cli":   "pi",
		"omp":      "omp",
		"oh-my-pi": "omp",
		"ohmypi":   "omp",
	} {
		t.Run(in, func(t *testing.T) {
			got, ok := normalizeHookTarget(in)
			if !ok || got != want {
				t.Fatalf("normalizeHookTarget(%q) = (%q, %v), want %q", in, got, ok, want)
			}
		})
	}
}

// Oh My Pi is a hook (extension) target, not an OTLP one: it exports no OpenTelemetry, so an
// endpoint install has nothing to point at it.
func TestOmpEndpointTargetIsHookShaped(t *testing.T) {
	got, ok := normalizeEndpointTarget("omp")
	if !ok {
		t.Fatal("normalizeEndpointTarget(omp) was not recognized")
	}
	if got.Name != "omp" || got.Kind != endpointTargetHook {
		t.Fatalf("normalizeEndpointTarget(omp) = %+v, want an omp hook target", got)
	}
}

// The wiring test. A harness the target table advertises but the install switch has no case for
// falls through to `unsupported hook harness`, so this is what catches a runtime added to the table
// and nowhere else -- the way a half-added integration ships.
func TestInstallEndpointHookTargetHandlesOmp(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("PI_CODING_AGENT_DIR", "")
	t.Setenv("PI_CONFIG_DIR", "")

	previousLevel := endpointOpts.hookLevel
	endpointOpts.hookLevel = string(endpointhooks.LevelUser)
	t.Cleanup(func() { endpointOpts.hookLevel = previousLevel })

	cfg := endpointconfig.Config{
		LogPath:  filepath.Join(t.TempDir(), "runtime.jsonl"),
		UserMode: true,
	}

	if err := installEndpointHookTarget("omp", cfg); err != nil {
		t.Fatalf("installEndpointHookTarget(omp) returned error: %v", err)
	}

	path := filepath.Join(home, ".omp", "agent", "extensions", "beacon.ts")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the install wrote no extension at %s: %v", path, err)
	}
	if !strings.Contains(string(data), endpointhooks.OmpManagedExtensionMarker) {
		t.Fatal("the installed extension carries no Beacon marker")
	}
	// The invocation has to name Oh My Pi's own mapper. `pi-event` here would attribute every Oh My
	// Pi session to Pi, which is the failure the separate harness name exists to prevent.
	if !strings.Contains(string(data), `"omp-event"`) {
		t.Fatalf("the installed extension does not invoke omp-event:\n%s", data)
	}
	if strings.Contains(string(data), `"pi-event"`) {
		t.Fatal("the installed Oh My Pi extension invokes Pi's mapper")
	}

	// Installing Oh My Pi must not touch Pi's directory.
	if _, err := os.Stat(filepath.Join(home, ".pi")); !os.IsNotExist(err) {
		t.Fatalf("installing Oh My Pi created a Pi extension directory: %v", err)
	}

	// And the same command removes it again.
	if err := uninstallEndpointHookTarget("omp", cfg); err != nil {
		t.Fatalf("uninstallEndpointHookTarget(omp) returned error: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the extension survived uninstall: %v", err)
	}
}
