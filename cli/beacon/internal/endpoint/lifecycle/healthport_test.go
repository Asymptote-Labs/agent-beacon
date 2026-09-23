package lifecycle

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

func shortTimeouts(t *testing.T) {
	t.Helper()
	oldReady, oldRelease := collectorReadyTimeout, portReleaseTimeout
	collectorReadyTimeout, portReleaseTimeout = 1500*time.Millisecond, 100*time.Millisecond
	t.Cleanup(func() { collectorReadyTimeout, portReleaseTimeout = oldReady, oldRelease })
}

// No-regression: the default install, which is what a package upgrade re-runs, keeps 13133.
func TestBuildConfigKeepsTheDefaultHealthPortForTheDefaultOTLPPorts(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	cfg := buildConfig(InstallOptions{UserMode: true, GRPCPort: endpointconfig.DefaultGRPCPort, HTTPPort: endpointconfig.DefaultHTTPPort})
	if cfg.Collector.HealthPort != endpointconfig.DefaultHealthCheckPort {
		t.Fatalf("HealthPort = %d, want %d", cfg.Collector.HealthPort, endpointconfig.DefaultHealthCheckPort)
	}
}

// #447: `install --user --otlp-grpc-port 4327 --otlp-http-port 4328` beside a system collector
// must not be handed the system collector's health port.
func TestBuildConfigMovesTheHealthPortWithAlternateOTLPPorts(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	cfg := buildConfig(InstallOptions{UserMode: true, GRPCPort: 4327, HTTPPort: 4328})
	if cfg.Collector.HealthPort != 13143 {
		t.Fatalf("HealthPort = %d, want 13143", cfg.Collector.HealthPort)
	}
}

// A health port recorded by an earlier install on other OTLP ports must not survive a reinstall
// that moves them; it is derived from this install's ports, like the OTLP ports themselves.
func TestBuildConfigDerivesTheHealthPortFromThisInstallsPorts(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	previous := endpointconfig.Default(true, filepath.Join(home, "runtime.jsonl"))
	previous.Collector.GRPCPort, previous.Collector.HTTPPort, previous.Collector.HealthPort = 4327, 4328, 13143
	if _, err := endpointconfig.Save(previous); err != nil {
		t.Fatal(err)
	}
	cfg := buildConfig(InstallOptions{UserMode: true, GRPCPort: 4337, HTTPPort: 4338})
	if cfg.Collector.HealthPort != 13153 {
		t.Fatalf("HealthPort = %d, want 13153 derived from the new HTTP port", cfg.Collector.HealthPort)
	}
}

func TestBuildConfigHonorsAnExplicitHealthPort(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	cfg := buildConfig(InstallOptions{UserMode: true, GRPCPort: 4327, HTTPPort: 4328, HealthPort: 23456})
	if cfg.Collector.HealthPort != 23456 {
		t.Fatalf("HealthPort = %d, want the explicit 23456", cfg.Collector.HealthPort)
	}
}

func TestPreflightRejectsAHealthPortThatIsAlsoAnOTLPPort(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	cfg := endpointconfig.Default(true, filepath.Join(t.TempDir(), "runtime.jsonl"))
	cfg.Collector.GRPCPort = freePort(t)
	cfg.Collector.HTTPPort = freePort(t)
	cfg.Collector.HealthPort = cfg.Collector.HTTPPort

	// Rejected even for --no-start: the written collector config could never start.
	err := preflight(cfg, false, service.KindSupervised)
	if err == nil || !strings.Contains(err.Error(), "--health-port") {
		t.Fatalf("preflight error = %v, want a --health-port collision error", err)
	}
}

// #447: the system collector holds the health port, the user install's OTLP ports are free. The
// install must stop before writing anything, and say which port is the problem.
func TestPreflightRejectsAHealthPortHeldByAnotherCollector(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	shortTimeouts(t)
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	cfg := endpointconfig.Default(true, filepath.Join(t.TempDir(), "runtime.jsonl"))
	cfg.Collector.GRPCPort = freePort(t)
	cfg.Collector.HTTPPort = freePort(t)
	cfg.Collector.HealthPort = held.Addr().(*net.TCPAddr).Port

	err = preflight(cfg, true, service.KindSupervised)

	if err == nil {
		t.Fatal("preflight passed with the health port held by another process")
	}
	msg := err.Error()
	if !strings.Contains(msg, "health-check port "+strconv.Itoa(cfg.Collector.HealthPort)) || !strings.Contains(msg, "--health-port") {
		t.Fatalf("preflight error = %q, want it to name the health-check port and --health-port", msg)
	}
	if strings.Contains(msg, "OTLP gRPC port") || strings.Contains(msg, "OTLP HTTP port") {
		t.Fatalf("preflight error = %q blames the OTLP ports, which are free", msg)
	}
}

// No-regression: a reinstall that moves only the gRPC port keeps the derived health port, which this
// mode's own running collector still holds until the install restarts it. That is not a conflict.
func TestPreflightAllowsAHealthPortHeldByThisModesRunningCollector(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	shortTimeouts(t)
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	writeRunningSupervisedState(t)
	cfg := endpointconfig.Default(true, filepath.Join(home, "runtime.jsonl"))
	cfg.Collector.GRPCPort = freePort(t)
	cfg.Collector.HTTPPort = freePort(t)
	cfg.Collector.HealthPort = held.Addr().(*net.TCPAddr).Port

	if err := preflight(cfg, true, service.KindSupervised); err != nil {
		t.Fatalf("preflight rejected a health port held by this mode's own running collector: %v", err)
	}
}

// Upgrade safety: a legacy user config on alternate OTLP ports ran its collector on 13133, and the
// reinstall a package upgrade runs moves it to the derived port. Its own running collector holds the
// OTLP ports and answers health on the old port; preflight must recognise it rather than wait out
// the timeout and blame the OTLP ports.
func TestPreflightRecognisesItsOwnCollectorWhileTheHealthPortMoves(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	shortTimeouts(t)
	writeRunningSupervisedState(t)
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
	oldHealth := serveHealthOK(t)

	previous := endpointconfig.Default(true, filepath.Join(home, "runtime.jsonl"))
	previous.Collector.GRPCPort = grpcLn.Addr().(*net.TCPAddr).Port
	previous.Collector.HTTPPort = httpLn.Addr().(*net.TCPAddr).Port
	previous.Collector.HealthPort = oldHealth
	if _, err := endpointconfig.Save(previous); err != nil {
		t.Fatal(err)
	}
	cfg := previous
	cfg.Collector.HealthPort = freePort(t)

	if err := preflight(cfg, true, service.KindSupervised); err != nil {
		t.Fatalf("preflight rejected a reinstall over this mode's own collector: %v", err)
	}
}

// The end-to-end #447 symptom: a collector that exits on start used to fail the install with only
// "Collector ports are not listening". The install error has to carry the collector's own reason.
func TestInstallErrorQuotesTheCollectorsOwnStartFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in collector is a shell script")
	}
	home := t.TempDir()
	testenv.SetHome(t, home)
	shortTimeouts(t)
	installFakeInventoryJob(t, false)

	bindFailure := "Error: failed to start extensions: failed to bind to address 127.0.0.1:13143: listen tcp 127.0.0.1:13143: bind: address already in use"
	collectorPath := filepath.Join(home, "bin", "beacon-otelcol")
	if err := os.MkdirAll(filepath.Dir(collectorPath), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho 'info starting collector' >&2\necho '" + bindFailure + "' >&2\nexit 1\n"
	if err := os.WriteFile(collectorPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// A failure left by an earlier run in the same appended-to log must not be quoted.
	logPath := service.Manager{UserMode: true, Kind: service.KindSupervised}.CollectorLog().Path
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("Error: stale failure from an earlier run\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Install(InstallOptions{
		UserMode:      true,
		LogPath:       filepath.Join(home, ".beacon", "endpoint", "logs", "runtime.jsonl"),
		Harnesses:     []string{},
		GRPCPort:      freePort(t),
		HTTPPort:      freePort(t),
		HealthPort:    freePort(t),
		CollectorPath: collectorPath,
		StartService:  true,
		ServiceKind:   service.KindSupervised,
	})

	if err == nil {
		t.Fatal("Install succeeded with a collector that exits on start")
	}
	msg := err.Error()
	if !strings.Contains(msg, bindFailure) {
		t.Fatalf("install error = %q, want it to quote the collector's bind failure", msg)
	}
	if strings.Contains(msg, "stale failure") {
		t.Fatalf("install error = %q quotes a line from an earlier run", msg)
	}
	if !strings.Contains(msg, logPath) {
		t.Fatalf("install error = %q, want it to name the log %s", msg, logPath)
	}
}

func TestCollectorStartErrorPointsAtTheJournalWhenThereIsNoFile(t *testing.T) {
	base := os.ErrDeadlineExceeded
	err := collectorStartError(base, service.CollectorLog{Hint: "journalctl --user -u beacon-collector.service"}, 0)
	if !strings.Contains(err.Error(), "journalctl --user -u beacon-collector.service") {
		t.Fatalf("error = %q, want the journal hint", err)
	}
	if got := collectorStartError(base, service.CollectorLog{}, 0); got != base {
		t.Fatalf("error with no log location = %v, want the original", got)
	}
}

// writeRunningSupervisedState records this test process as the user-mode supervised collector, so
// the backend reports it loaded and running without starting anything.
func writeRunningSupervisedState(t *testing.T) {
	t.Helper()
	pidfile, err := service.Manager{UserMode: true, Kind: service.KindSupervised}.UnitPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(pidfile), 0o755); err != nil {
		t.Fatal(err)
	}
	state := `{"pid": ` + strconv.Itoa(os.Getpid()) + `, "program": "/bin/beacon-otelcol", "config_path": "/tmp/otelcol.yaml"}`
	if err := os.WriteFile(pidfile, []byte(state), 0o644); err != nil {
		t.Fatal(err)
	}
}

func serveHealthOK(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })}
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
