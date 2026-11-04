package diagnostics

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
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

// Upgrade safety: a config.json written before health_port existed rendered 13133 into its
// collector config, so doctor must probe 13133 for it. Reading the zero field directly would probe
// 127.0.0.1:0 and warn about a healthy collector on every upgraded endpoint.
func TestDoctorProbesTheDefaultHealthPortForAConfigWrittenBeforeTheField(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	path := endpointconfig.ConfigPath(true)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"user_mode": true, "log_path": "/tmp/runtime.jsonl", "collector": {"grpc_port": 4327, "http_port": 4328}}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := endpointconfig.Load(true)
	if err != nil {
		t.Fatalf("Load legacy config: %v", err)
	}

	for _, check := range Run(cfg) {
		if check.Name == "collector_health" {
			if want := "127.0.0.1:" + strconv.Itoa(endpointconfig.DefaultHealthCheckPort); check.Target != want {
				t.Fatalf("doctor collector_health target = %q for a legacy config, want %q", check.Target, want)
			}
			return
		}
	}
	t.Fatal("doctor ran no collector_health check")
}
