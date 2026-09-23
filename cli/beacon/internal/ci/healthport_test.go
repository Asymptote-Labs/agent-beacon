package ci

import (
	"os"
	"strings"
	"testing"
)

// A `beacon ci` collector on custom OTLP ports gets the same derived health port an endpoint
// install would, rather than always 13133, so it can run beside an endpoint collector.
func TestProvisionDerivesTheHealthPortFromTheOTLPPorts(t *testing.T) {
	t.Setenv("RUNNER_TEMP", t.TempDir())
	collector := fakeExecutable(t, "collector", "#!/bin/sh\nsleep 60\n")
	oldResolve := resolveCollectorBinary
	resolveCollectorBinary = func(string) (string, error) { return collector, nil }
	t.Cleanup(func() { resolveCollectorBinary = oldResolve })

	for _, tc := range []struct {
		grpc, http int
		want       string
	}{
		{0, 0, "endpoint: 127.0.0.1:13133\n"},
		{5317, 5318, "endpoint: 127.0.0.1:14133\n"},
	} {
		session, err := Provision(Options{CollectorPath: collector, Harness: "claude", GRPCPort: tc.grpc, HTTPPort: tc.http})
		if err != nil {
			t.Fatalf("Provision: %v", err)
		}
		data, err := os.ReadFile(session.ConfigPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "health_check:\n    "+tc.want) {
			t.Fatalf("ports %d/%d: collector config health_check does not contain %q:\n%s", tc.grpc, tc.http, tc.want, data)
		}
	}
}
