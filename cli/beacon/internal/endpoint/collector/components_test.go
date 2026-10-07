package collector

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func stubComponents(t *testing.T, out string, err error) {
	t.Helper()
	previous := listComponents
	listComponents = func(context.Context, string) ([]byte, error) { return []byte(out), err }
	t.Cleanup(func() { listComponents = previous })
}

// The v1.3.32 release collector lists only these processors, and refuses a config that names
// claude_api_body.
const releaseComponents = `buildinfo:
    command: beacon-otelcol
    version: 0.121.0
processors:
    - name: batch
      module: go.opentelemetry.io/collector/processor/batchprocessor v0.121.0
    - name: memory_limiter
      module: go.opentelemetry.io/collector/processor/memorylimiterprocessor v0.121.0
`

func TestCheckComponentsRefusesACollectorWithoutTheBodyProcessor(t *testing.T) {
	stubComponents(t, releaseComponents, nil)
	err := CheckComponents("/usr/local/bin/beacon-otelcol")
	if err == nil || !strings.Contains(err.Error(), `"claude_api_body"`) || !strings.Contains(err.Error(), "/usr/local/bin/beacon-otelcol") {
		t.Fatalf("CheckComponents = %v, want the missing processor and the binary named", err)
	}
}

func TestCheckComponentsAcceptsACollectorWithTheBodyProcessor(t *testing.T) {
	stubComponents(t, releaseComponents+"    - name: claude_api_body\n      module: github.com/asymptote-labs/agent-beacon/collector-builder/exporter/beaconjsonexporter v0.0.0\n", nil)
	if err := CheckComponents("beacon-otelcol"); err != nil {
		t.Fatalf("CheckComponents = %v, want a collector that has claude_api_body accepted", err)
	}
}

// A stand-in that cannot list its components is not judged on them.
func TestCheckComponentsAcceptsABinaryThatCannotListComponents(t *testing.T) {
	for name, stub := range map[string]struct {
		out string
		err error
	}{
		"command fails":    {"", errors.New("exit status 1")},
		"prints nothing":   {"", nil},
		"prints non-YAML":  {"fake beacon-otelcol for smoke test\n", nil},
		"lists processors": {"processors: []\n", nil},
	} {
		t.Run(name, func(t *testing.T) {
			stubComponents(t, stub.out, stub.err)
			if err := CheckComponents("beacon-otelcol"); err != nil {
				t.Fatalf("CheckComponents = %v, want nil", err)
			}
		})
	}
}
