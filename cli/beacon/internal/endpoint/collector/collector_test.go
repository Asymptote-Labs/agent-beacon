package collector

import (
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
)

func testConfig(t *testing.T) endpointconfig.Config {
	t.Helper()
	dir := t.TempDir()
	return endpointconfig.Config{
		UserMode: true,
		LogPath:  filepath.Join(dir, "logs", "runtime.jsonl"),
		Collector: endpointconfig.Collector{
			ConfigPath: filepath.Join(dir, "otelcol.yaml"),
			GRPCPort:   14317,
			HTTPPort:   14318,
			SpoolPath:  filepath.Join(dir, "spool", "otlp.jsonl"),
		},
	}
}

func TestConfigYAMLIncludesReleaseContractFields(t *testing.T) {
	cfg := testConfig(t)

	yaml := ConfigYAML(cfg)

	for _, want := range []string{
		"endpoint: 127.0.0.1:14317",
		"endpoint: 127.0.0.1:14318",
		"beaconjson:",
		"path: " + `"` + cfg.LogPath + `"`,
		"max_event_bytes: 65536",
		"rotate_bytes: 10485760",
		"rotate_archives: 5",
		"redact_secrets: true",
		"level: none",
		"receivers: [otlp]",
		"exporters: [beaconjson]",
	} {
		if !strings.Contains(yaml, want) {
			t.Fatalf("ConfigYAML missing %q:\n%s", want, yaml)
		}
	}
	if strings.Contains(yaml, "include_runtime_metrics") {
		t.Fatalf("ConfigYAML should omit runtime metrics option unless explicitly enabled:\n%s", yaml)
	}
	if strings.Contains(yaml, "include_codex_spans") {
		t.Fatalf("ConfigYAML should omit Codex span option unless explicitly enabled:\n%s", yaml)
	}
}

func TestConfigYAMLIncludesRuntimeMetricsOptIn(t *testing.T) {
	cfg := testConfig(t)
	cfg.Collector.IncludeRuntimeMetrics = true

	yaml := ConfigYAML(cfg)
	if !strings.Contains(yaml, "include_runtime_metrics: true") {
		t.Fatalf("ConfigYAML missing runtime metrics opt-in:\n%s", yaml)
	}
}

func TestConfigYAMLIncludesCodexSpansOptIn(t *testing.T) {
	cfg := testConfig(t)
	cfg.Collector.IncludeCodexSpans = true

	yaml := ConfigYAML(cfg)
	if !strings.Contains(yaml, "include_codex_spans: true") {
		t.Fatalf("ConfigYAML missing Codex spans opt-in:\n%s", yaml)
	}
}

func TestConfigYAMLIncludesSplunkHECWhenConfigured(t *testing.T) {
	cfg := testConfig(t)
	cfg.Destinations = &endpointconfig.Destinations{SplunkHEC: &endpointconfig.SplunkHEC{
		Endpoint:           "https://splunk.example:8088/services/collector",
		Token:              "hec-token",
		Index:              "beacon",
		Source:             "beacon-endpoint-agent",
		Sourcetype:         "beacon:endpoint",
		InsecureSkipVerify: true,
		CAFile:             "/tmp/ca.pem",
	}}

	yaml := ConfigYAML(cfg)

	for _, want := range []string{
		"splunk_hec:",
		`token: "hec-token"`,
		`endpoint: "https://splunk.example:8088/services/collector"`,
		`index: "beacon"`,
		`source: "beacon-endpoint-agent"`,
		`sourcetype: "beacon:endpoint"`,
		"tls:",
		"insecure_skip_verify: true",
		`ca_file: "/tmp/ca.pem"`,
		"exporters: [beaconjson, splunk_hec]",
	} {
		if !strings.Contains(yaml, want) {
			t.Fatalf("ConfigYAML missing %q:\n%s", want, yaml)
		}
	}
}

func TestConfigYAMLIncludesFalconHECWhenConfigured(t *testing.T) {
	cfg := testConfig(t)
	cfg.ContentRetention = endpointconfig.ContentRetentionRedacted
	cfg.Collector.IncludeRuntimeMetrics = true
	cfg.Collector.IncludeCodexSpans = true
	cfg.Destinations = &endpointconfig.Destinations{FalconHEC: &endpointconfig.FalconHEC{
		Endpoint:           "https://cloud.us.humio.com/api/v1/ingest/hec",
		Token:              "ingest-token",
		Index:              "beacon-repo",
		Source:             "beacon-endpoint-agent",
		Sourcetype:         "json",
		InsecureSkipVerify: true,
		CAFile:             "/tmp/logscale-ca.pem",
	}}

	yaml := ConfigYAML(cfg)

	for _, want := range []string{
		"falcon_hec:",
		`token: "ingest-token"`,
		`endpoint: "https://cloud.us.humio.com/api/v1/ingest/hec"`,
		`content_retention: "redacted"`,
		"include_runtime_metrics: true",
		"include_codex_spans: true",
		`index: "beacon-repo"`,
		`source: "beacon-endpoint-agent"`,
		`sourcetype: "json"`,
		"insecure_skip_verify: true",
		`ca_file: "/tmp/logscale-ca.pem"`,
		"exporters: [beaconjson, falcon_hec]",
	} {
		if !strings.Contains(yaml, want) {
			t.Fatalf("ConfigYAML missing %q:\n%s", want, yaml)
		}
	}
}

func TestConfigYAMLIncludesSplunkAndFalconHECWhenConfigured(t *testing.T) {
	cfg := testConfig(t)
	cfg.Destinations = &endpointconfig.Destinations{
		SplunkHEC: &endpointconfig.SplunkHEC{
			Endpoint: "https://splunk.example:8088/services/collector",
			Token:    "hec-token",
		},
		FalconHEC: &endpointconfig.FalconHEC{
			Endpoint: "https://cloud.us.humio.com/api/v1/ingest/hec",
			Token:    "ingest-token",
		},
	}

	yaml := ConfigYAML(cfg)
	if !strings.Contains(yaml, "exporters: [beaconjson, splunk_hec, falcon_hec]") {
		t.Fatalf("ConfigYAML missing combined exporters:\n%s", yaml)
	}
}

func TestWriteConfigCreatesConfigAndSpoolDirectory(t *testing.T) {
	cfg := testConfig(t)

	if err := WriteConfig(cfg); err != nil {
		t.Fatalf("WriteConfig returned error: %v", err)
	}
	if _, err := os.Stat(cfg.Collector.ConfigPath); err != nil {
		t.Fatalf("collector config not written: %v", err)
	}
	if info, err := os.Stat(filepath.Dir(cfg.Collector.SpoolPath)); err != nil || !info.IsDir() {
		t.Fatalf("spool dir not created: info=%v err=%v", info, err)
	}
}

func TestWriteConfigUsesPrivatePermissionsWithSplunkToken(t *testing.T) {
	cfg := testConfig(t)
	cfg.Destinations = &endpointconfig.Destinations{SplunkHEC: &endpointconfig.SplunkHEC{
		Endpoint: "https://splunk.example:8088/services/collector",
		Token:    "hec-token",
	}}

	if err := WriteConfig(cfg); err != nil {
		t.Fatalf("WriteConfig returned error: %v", err)
	}
	info, err := os.Stat(cfg.Collector.ConfigPath)
	if err != nil {
		t.Fatalf("stat collector config: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0600); got != want {
		t.Fatalf("collector config permissions = %o, want %o", got, want)
	}
}

func TestWriteConfigUsesPrivatePermissionsWithFalconToken(t *testing.T) {
	cfg := testConfig(t)
	cfg.Destinations = &endpointconfig.Destinations{FalconHEC: &endpointconfig.FalconHEC{
		Endpoint: "https://cloud.us.humio.com/api/v1/ingest/hec",
		Token:    "ingest-token",
	}}

	if err := WriteConfig(cfg); err != nil {
		t.Fatalf("WriteConfig returned error: %v", err)
	}
	info, err := os.Stat(cfg.Collector.ConfigPath)
	if err != nil {
		t.Fatalf("stat collector config: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0600); got != want {
		t.Fatalf("collector config permissions = %o, want %o", got, want)
	}
}

func TestDiscoverBinaryPrefersConfiguredExistingPath(t *testing.T) {
	bin := filepath.Join(t.TempDir(), BinaryName)
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatalf("write fake collector: %v", err)
	}

	if got := DiscoverBinary(bin); got != bin {
		t.Fatalf("DiscoverBinary = %q, want configured path %q", got, bin)
	}
}

func TestResolveBinaryRejectsMissingConfiguredPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), BinaryName)

	_, err := ResolveBinary(missing)
	if err == nil || !strings.Contains(err.Error(), "not usable") || !strings.Contains(err.Error(), missing) {
		t.Fatalf("ResolveBinary error = %v, want configured path error", err)
	}
}

func TestDiscoverBinaryFindsBeaconOtelcolOnPath(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, BinaryName)
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatalf("write fake collector: %v", err)
	}
	t.Setenv("PATH", dir)
	withCollectorDiscovery(t, nil, nil)

	if got := DiscoverBinary(""); got != bin {
		t.Fatalf("DiscoverBinary = %q, want PATH binary %q", got, bin)
	}
}

func TestDiscoverBinaryIgnoresGenericOtelcol(t *testing.T) {
	dir := t.TempDir()
	generic := filepath.Join(dir, "otelcol-contrib")
	if err := os.WriteFile(generic, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatalf("write generic collector: %v", err)
	}
	t.Setenv("PATH", dir)
	withCollectorDiscovery(t, nil, nil)

	if got := DiscoverBinary(""); got != "" {
		t.Fatalf("DiscoverBinary = %q, want empty for generic collector", got)
	}
}

func TestDiscoverBinaryFindsAdjacentBeaconOtelcol(t *testing.T) {
	dir := t.TempDir()
	beacon := filepath.Join(dir, "beacon")
	collector := filepath.Join(dir, BinaryName)
	if err := os.WriteFile(collector, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatalf("write adjacent collector: %v", err)
	}
	withCollectorDiscovery(t, func(file string) (string, error) {
		if file == BinaryName {
			return "", errors.New("not found")
		}
		return "", errors.New("unexpected lookup")
	}, func() []string {
		return []string{filepath.Join(filepath.Dir(beacon), BinaryName)}
	})

	if got := DiscoverBinary(""); got != collector {
		t.Fatalf("DiscoverBinary = %q, want adjacent collector %q", got, collector)
	}
}

func TestPortAvailabilityAndOpenChecks(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	if PortAvailable(port) {
		t.Fatalf("PortAvailable(%d) = true while listener is active", port)
	}
	if !portOpen(port) {
		t.Fatalf("portOpen(%d) = false while listener is active", port)
	}
}

func TestCheckStatusDoesNotTreatOpenOTLPPortsAsHealthy(t *testing.T) {
	if healthReady() {
		t.Skip("collector health check endpoint is already active")
	}
	bin := filepath.Join(t.TempDir(), BinaryName)
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatalf("write fake collector: %v", err)
	}
	grpcLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen grpc: %v", err)
	}
	defer grpcLn.Close()
	httpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen http: %v", err)
	}
	defer httpLn.Close()
	cfg := testConfig(t)
	cfg.Collector.BinaryPath = bin
	cfg.Collector.GRPCPort = grpcLn.Addr().(*net.TCPAddr).Port
	cfg.Collector.HTTPPort = httpLn.Addr().(*net.TCPAddr).Port

	status := CheckStatus(cfg)
	if !status.GRPCReady || !status.HTTPReady {
		t.Fatalf("OTLP ports should appear open: %#v", status)
	}
	if status.HealthReady {
		t.Fatalf("HealthReady = true for unrelated listeners: %#v", status)
	}
	if !strings.Contains(status.Message, "health check") {
		t.Fatalf("Message = %q, want health check warning", status.Message)
	}
}

func TestHealthReadyChecksCollectorHealthEndpoint(t *testing.T) {
	if !PortAvailable(HealthCheckPort) {
		t.Skip("collector health check port is already in use")
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})}
	ln, err := net.Listen("tcp", "127.0.0.1:13133")
	if err != nil {
		t.Fatalf("listen health: %v", err)
	}
	done := make(chan struct{})
	go func() {
		_ = server.Serve(ln)
		close(done)
	}()
	t.Cleanup(func() {
		_ = server.Close()
		<-done
	})

	if !healthReady() {
		t.Fatal("healthReady() = false, want true")
	}
}

func TestLaunchAgentPlistUsesFallbackBinaryAndUserLabel(t *testing.T) {
	cfg := testConfig(t)
	cfg.UserMode = true
	cfg.Collector.BinaryPath = filepath.Join(t.TempDir(), "missing-otelcol")
	withCollectorDiscovery(t, func(file string) (string, error) {
		return "", errors.New("not found")
	}, nil)

	plist := LaunchAgentPlist(cfg)

	for _, want := range []string{
		"<string>com.beacon.endpoint.collector.user</string>",
		"<string>beacon-otelcol</string>",
		"<string>--config</string>",
		"<string>" + cfg.Collector.ConfigPath + "</string>",
	} {
		if !strings.Contains(plist, want) {
			t.Fatalf("LaunchAgentPlist missing %q:\n%s", want, plist)
		}
	}
}

func TestWriteLaunchPlistUserMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("launch plist paths are POSIX-specific")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := testConfig(t)
	cfg.UserMode = true

	path, err := WriteLaunchPlist(cfg)
	if err != nil {
		t.Fatalf("WriteLaunchPlist returned error: %v", err)
	}
	if got, want := path, filepath.Join(home, "Library", "LaunchAgents", "com.beacon.endpoint.collector.plist"); got != want {
		t.Fatalf("plist path = %q, want %q", got, want)
	}
}

func withCollectorDiscovery(t *testing.T, lookup func(string) (string, error), candidates func() []string) {
	t.Helper()
	oldLookPath := lookPath
	oldCandidates := discoverDefaultBinaryCandidates
	if lookup == nil {
		lookup = execLookPath
	}
	if candidates == nil {
		candidates = func() []string { return nil }
	}
	lookPath = lookup
	discoverDefaultBinaryCandidates = candidates
	t.Cleanup(func() {
		lookPath = oldLookPath
		discoverDefaultBinaryCandidates = oldCandidates
	})
}

func execLookPath(file string) (string, error) {
	return exec.LookPath(file)
}
