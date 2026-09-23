package diagnostics

import (
	"net"
	"path/filepath"
	"strconv"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
)

// Doctor's collector_health check has to report the port this endpoint's collector binds, not the
// fixed 13133, or a user-mode endpoint beside the system one is diagnosed against the wrong collector.
func TestCollectorHealthCheckTargetsTheConfiguredHealthPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	cfg := endpointconfig.Default(true, filepath.Join(t.TempDir(), "runtime.jsonl"))
	cfg.Collector.HealthPort = port

	check := checkCollectorHealth(cfg)

	if want := "127.0.0.1:" + strconv.Itoa(port); check.Target != want {
		t.Fatalf("collector_health target = %q, want %q", check.Target, want)
	}
}

func TestCollectorHealthCheckTargetsTheDefaultPortForALegacyConfig(t *testing.T) {
	cfg := endpointconfig.Default(true, filepath.Join(t.TempDir(), "runtime.jsonl"))
	cfg.Collector.HealthPort = 0

	check := checkCollectorHealth(cfg)

	if check.Target != "127.0.0.1:13133" {
		t.Fatalf("collector_health target = %q, want 127.0.0.1:13133", check.Target)
	}
}
