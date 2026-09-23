package collector

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func freeTestPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// serveHealth answers 200 on a free loopback port, standing in for the health_check extension.
func serveHealth(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})}
	done := make(chan struct{})
	go func() {
		_ = server.Serve(ln)
		close(done)
	}()
	t.Cleanup(func() {
		_ = server.Close()
		<-done
	})
	return ln.Addr().(*net.TCPAddr).Port
}

// #447: the rendered collector config used to hardcode 127.0.0.1:13133 whatever the install asked for.
func TestConfigYAMLRendersTheConfiguredHealthPort(t *testing.T) {
	cfg := testConfig(t)
	cfg.Collector.HealthPort = 13143

	yaml := ConfigYAML(cfg)

	if !strings.Contains(yaml, "health_check:\n    endpoint: 127.0.0.1:13143\n") {
		t.Fatalf("collector config does not bind the configured health port:\n%s", yaml)
	}
	if strings.Contains(yaml, "13133") {
		t.Fatalf("collector config still carries the fixed 13133:\n%s", yaml)
	}
}

// Upgrade safety: re-rendering a config recorded before the field existed keeps the port the
// running collector already binds.
func TestConfigYAMLKeepsTheDefaultHealthPortForALegacyConfig(t *testing.T) {
	cfg := testConfig(t)
	cfg.Collector.HealthPort = 0

	yaml := ConfigYAML(cfg)

	if !strings.Contains(yaml, "health_check:\n    endpoint: 127.0.0.1:13133\n") {
		t.Fatalf("legacy config should render the default health port:\n%s", yaml)
	}
}

// Status and the install readiness wait have to probe the port the collector was told to bind.
func TestCheckStatusProbesTheConfiguredHealthPort(t *testing.T) {
	healthPort := serveHealth(t)
	cfg := testConfig(t)
	cfg.Collector.GRPCPort = freeTestPort(t)
	cfg.Collector.HTTPPort = freeTestPort(t)
	cfg.Collector.HealthPort = healthPort

	status := CheckStatus(cfg)

	if !status.HealthReady {
		t.Fatalf("HealthReady = false with a healthy endpoint on the configured port %d: %#v", healthPort, status)
	}
	if status.HealthPort != healthPort {
		t.Fatalf("status HealthPort = %d, want %d", status.HealthPort, healthPort)
	}
}

func TestCheckStatusNamesTheHealthPortItProbed(t *testing.T) {
	grpcLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer grpcLn.Close()
	httpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer httpLn.Close()
	cfg := testConfig(t)
	cfg.Collector.BinaryPath = writeStubCollector(t)
	cfg.Collector.GRPCPort = grpcLn.Addr().(*net.TCPAddr).Port
	cfg.Collector.HTTPPort = httpLn.Addr().(*net.TCPAddr).Port
	cfg.Collector.HealthPort = freeTestPort(t)

	status := CheckStatus(cfg)

	if status.HealthReady {
		t.Fatalf("HealthReady = true with nothing on the health port: %#v", status)
	}
	if want := "127.0.0.1:" + strconv.Itoa(cfg.Collector.HealthPort); !strings.Contains(status.Message, want) {
		t.Fatalf("Message = %q, want it to name %s", status.Message, want)
	}
}

// #447: the readiness preflight used to check only the OTLP ports, so a health port held by the
// system collector passed it and the install failed later with an error blaming the OTLP ports.
func TestWaitForPortsAvailableReportsAHealthPortConflict(t *testing.T) {
	healthLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer healthLn.Close()
	healthPort := healthLn.Addr().(*net.TCPAddr).Port

	err = WaitForPortsAvailable(freeTestPort(t), freeTestPort(t), healthPort, 50*time.Millisecond)

	if err == nil {
		t.Fatal("WaitForPortsAvailable returned nil with the health port held")
	}
	msg := err.Error()
	if !strings.Contains(msg, "health-check port "+strconv.Itoa(healthPort)) || !strings.Contains(msg, "--health-port") {
		t.Fatalf("error = %q, want it to name health-check port %d and --health-port", msg, healthPort)
	}
	if strings.Contains(msg, "OTLP") {
		t.Fatalf("error = %q blames the OTLP ports, which are free", msg)
	}
}

func writeStubCollector(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), stubCollectorName())
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
