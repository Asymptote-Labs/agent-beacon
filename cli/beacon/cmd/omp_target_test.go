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

// The wiring test. A harness the target table advertises but the install switch has no case for
// falls through to `unsupported hook harness`, so this is what catches a runtime added to the table
// and nowhere else -- the way a half-added integration ships.
func TestInstallEndpointHookTargetHandlesOmp(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	for _, name := range []string{"PI_CODING_AGENT_DIR", "PI_CONFIG_DIR", "OMP_PROFILE", "PI_PROFILE"} {
		t.Setenv(name, "")
	}

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
