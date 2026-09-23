package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// The default install must keep the port every existing endpoint, doc, and packaging smoke test
// already expects.
func TestDeriveHealthCheckPortKeepsTheDefaultForTheDefaultPorts(t *testing.T) {
	if got := DeriveHealthCheckPort(DefaultGRPCPort, DefaultHTTPPort); got != DefaultHealthCheckPort {
		t.Fatalf("DeriveHealthCheckPort(defaults) = %d, want %d", got, DefaultHealthCheckPort)
	}
	if got := DeriveHealthCheckPort(0, 0); got != DefaultHealthCheckPort {
		t.Fatalf("DeriveHealthCheckPort(0, 0) = %d, want %d", got, DefaultHealthCheckPort)
	}
}

// #447: a user-mode install moved to alternate OTLP ports must get a health port that differs from
// the system collector's, or the two cannot run together.
func TestDeriveHealthCheckPortMovesWithTheHTTPPort(t *testing.T) {
	for _, tc := range []struct{ grpc, http, want int }{
		{4327, 4328, 13143},
		{14317, 14318, 23133},
		{5317, 5318, 14133},
	} {
		if got := DeriveHealthCheckPort(tc.grpc, tc.http); got != tc.want {
			t.Errorf("DeriveHealthCheckPort(%d, %d) = %d, want %d", tc.grpc, tc.http, got, tc.want)
		}
	}
}

// Whatever the OTLP ports, the derived port has to be one the collector can bind next to them.
func TestDeriveHealthCheckPortIsAlwaysValidAndDistinct(t *testing.T) {
	for http := 1; http <= 65535; http++ {
		for _, grpc := range []int{http - 1, http + 1, DefaultGRPCPort, DefaultHealthCheckPort + (http - DefaultHTTPPort)} {
			if grpc <= 0 || grpc > 65535 || grpc == http {
				continue
			}
			got := DeriveHealthCheckPort(grpc, http)
			if got <= 0 || got > 65535 || got == grpc || got == http {
				t.Fatalf("DeriveHealthCheckPort(%d, %d) = %d, want a valid port distinct from both", grpc, http, got)
			}
		}
	}
}

// Upgrade safety: a config.json written before the field existed rendered 13133 into its collector
// config, so status and repair must keep reading 13133 for it rather than deriving a new one.
func TestHealthCheckPortIsTheDefaultForAConfigWithoutTheField(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	path := ConfigPath(true)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"user_mode": true, "log_path": "/tmp/runtime.jsonl", "collector": {"grpc_port": 4327, "http_port": 4328}}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(true)
	if err != nil {
		t.Fatalf("Load legacy config: %v", err)
	}
	if cfg.Collector.HealthPort != 0 {
		t.Fatalf("legacy config decoded HealthPort = %d, want 0", cfg.Collector.HealthPort)
	}
	if got := HealthCheckPort(cfg.Collector); got != DefaultHealthCheckPort {
		t.Fatalf("HealthCheckPort(legacy) = %d, want %d", got, DefaultHealthCheckPort)
	}
}

func TestHealthPortRoundTripsThroughConfigJSON(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	cfg := Default(true, filepath.Join(home, "runtime.jsonl"))
	cfg.Collector.HealthPort = 13143
	path, err := Save(cfg)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"health_port": 13143`) {
		t.Fatalf("config.json does not record the health port:\n%s", data)
	}
	loaded, err := Load(true)
	if err != nil {
		t.Fatal(err)
	}
	if got := HealthCheckPort(loaded.Collector); got != 13143 {
		t.Fatalf("HealthCheckPort after round trip = %d, want 13143", got)
	}
}

func TestValidateCollectorPortsRejectsAHealthPortTheCollectorCannotBind(t *testing.T) {
	ok := Collector{GRPCPort: 4327, HTTPPort: 4328, HealthPort: 13143}
	if err := ValidateCollectorPorts(ok); err != nil {
		t.Fatalf("valid ports rejected: %v", err)
	}
	if err := ValidateCollectorPorts(Collector{GRPCPort: 4317, HTTPPort: 4318}); err != nil {
		t.Fatalf("legacy config (no health port) rejected: %v", err)
	}
	for name, c := range map[string]Collector{
		"same as gRPC": {GRPCPort: 4327, HTTPPort: 4328, HealthPort: 4327},
		"same as HTTP": {GRPCPort: 4327, HTTPPort: 4328, HealthPort: 4328},
		"out of range": {GRPCPort: 4327, HTTPPort: 4328, HealthPort: 70000},
	} {
		err := ValidateCollectorPorts(c)
		if err == nil || !strings.Contains(err.Error(), "--health-port") {
			t.Errorf("%s: ValidateCollectorPorts error = %v, want one naming --health-port", name, err)
		}
	}
}
