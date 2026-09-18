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

// The spellings an operator actually types. "prime" is the --platform value and the shortest
// unambiguous name; "prime-agent" is the binary; the underscore form arrives from anyone copying
// the canonical harness name out of a log, since normalizeHarnessKey maps `_` to `-`.
func TestPrimeHookTargetAliases(t *testing.T) {
	for _, in := range []string{"prime", "PRIME", "  prime  ", "prime-agent", "prime_agent", "primeagent", "Prime-Agent"} {
		t.Run(in, func(t *testing.T) {
			got, ok := normalizeHookTarget(in)
			if !ok {
				t.Fatalf("normalizeHookTarget(%q) was not recognized", in)
			}
			if got != "prime" {
				t.Fatalf("normalizeHookTarget(%q) = %q, want prime", in, got)
			}
		})
	}
}

// Pi, Oh My Pi and Prime Agent are separately installed products. A `--harness` value for one must
// never resolve to another, or an operator asking to instrument one gets another's extension
// written into a directory the runtime they meant does not read.
func TestPrimePiAndOmpTargetsDoNotAlias(t *testing.T) {
	for in, want := range map[string]string{
		"pi":          "pi",
		"pi-cli":      "pi",
		"omp":         "omp",
		"oh-my-pi":    "omp",
		"prime":       "prime",
		"prime-agent": "prime",
		"prime_agent": "prime",
		"primeagent":  "prime",
	} {
		t.Run(in, func(t *testing.T) {
			got, ok := normalizeHookTarget(in)
			if !ok || got != want {
				t.Fatalf("normalizeHookTarget(%q) = (%q, %v), want %q", in, got, ok, want)
			}
		})
	}
}

// Prime Agent is a hook (extension) target, not an OTLP one: it exports no OpenTelemetry, so an
// endpoint install has nothing to point at it.
func TestPrimeEndpointTargetIsHookShaped(t *testing.T) {
	got, ok := normalizeEndpointTarget("prime")
	if !ok {
		t.Fatal("normalizeEndpointTarget(prime) was not recognized")
	}
	if got.Name != "prime" || got.Kind != endpointTargetHook {
		t.Fatalf("normalizeEndpointTarget(prime) = %+v, want a prime hook target", got)
	}
}

// The wiring test. A harness the target table advertises but the install switch has no case for
// falls through to `unsupported hook harness`, so this is what catches a runtime added to the table
// and nowhere else -- the way a half-added integration ships.
func TestInstallEndpointHookTargetHandlesPrime(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("PRIME_AGENT_CODING_AGENT_DIR", "")

	previousLevel := endpointOpts.hookLevel
	endpointOpts.hookLevel = string(endpointhooks.LevelUser)
	t.Cleanup(func() { endpointOpts.hookLevel = previousLevel })

	cfg := endpointconfig.Config{
		LogPath:  filepath.Join(t.TempDir(), "runtime.jsonl"),
		UserMode: true,
	}

	if _, err := installEndpointHookTarget("prime", cfg); err != nil {
		t.Fatalf("installEndpointHookTarget(prime) returned error: %v", err)
	}

	path := filepath.Join(home, ".prime", "agent", "extensions", "beacon.ts")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the install wrote no extension at %s: %v", path, err)
	}
	if !strings.Contains(string(data), endpointhooks.PrimeManagedExtensionMarker) {
		t.Fatal("the installed extension carries no Beacon marker")
	}
	// The invocation has to name Prime Agent's own mapper. `pi-event` here would attribute every
	// Prime Agent session to Pi, which is the failure the separate harness name exists to prevent.
	if !strings.Contains(string(data), `"prime-event"`) {
		t.Fatalf("the installed extension does not invoke prime-event:\n%s", data)
	}
	for _, foreign := range []string{`"pi-event"`, `"omp-event"`} {
		if strings.Contains(string(data), foreign) {
			t.Fatalf("the installed Prime Agent extension invokes %s", foreign)
		}
	}

	// Installing Prime Agent must not touch either sibling's directory.
	for _, sibling := range []string{".pi", ".omp"} {
		if _, err := os.Stat(filepath.Join(home, sibling)); !os.IsNotExist(err) {
			t.Fatalf("installing Prime Agent created %s: %v", sibling, err)
		}
	}

	// And the same command removes it again.
	if err := uninstallEndpointHookTarget("prime", cfg); err != nil {
		t.Fatalf("uninstallEndpointHookTarget(prime) returned error: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the extension survived uninstall: %v", err)
	}
}
