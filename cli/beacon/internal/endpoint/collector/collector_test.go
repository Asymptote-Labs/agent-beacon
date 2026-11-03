package collector

import (
	"errors"
	"fmt"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

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

// The direct HEC exporters send what they receive, so privacy has to run before the fanout. The
// local writer redacts on its own, so a config without them keeps its pipelines unchanged.
func TestPrivacyRunsBeforeHECFanoutForEverySignal(t *testing.T) {
	for _, tc := range []struct {
		name         string
		destinations *endpointconfig.Destinations
		privacy      string
		exporters    string
	}{
		{"hec", &endpointconfig.Destinations{
			SplunkHEC: &endpointconfig.SplunkHEC{Endpoint: "https://splunk.example/services/collector", Token: "test-token"},
			FalconHEC: &endpointconfig.FalconHEC{Endpoint: "https://falcon.example/api/v1/ingest/hec", Token: "test-token"},
		}, "beacon_privacy,", "beaconjson,splunk_hec,falcon_hec"},
		{"local only", nil, "", "beaconjson"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.Destinations = tc.destinations
			var rendered struct {
				Processors map[string]any `yaml:"processors"`
				Service    struct {
					Pipelines map[string]struct {
						Processors []string `yaml:"processors"`
						Exporters  []string `yaml:"exporters"`
					} `yaml:"pipelines"`
				} `yaml:"service"`
			}
			if err := yaml.Unmarshal([]byte(ConfigYAML(cfg)), &rendered); err != nil {
				t.Fatal(err)
			}
			if _, exists := rendered.Processors["beacon_privacy"]; exists != (tc.privacy != "") {
				t.Fatalf("beacon_privacy declared = %v, want %v", exists, tc.privacy != "")
			}
			for _, signal := range []string{"logs", "traces", "metrics"} {
				pipeline := rendered.Service.Pipelines[signal]
				want := "memory_limiter," + tc.privacy + "batch"
				if signal == "logs" {
					want = "memory_limiter,claude_api_body," + tc.privacy + "batch"
				}
				if strings.Join(pipeline.Processors, ",") != want {
					t.Fatalf("%s processor order = %v, want %s", signal, pipeline.Processors, want)
				}
				if strings.Join(pipeline.Exporters, ",") != tc.exporters {
					t.Fatalf("%s fanout = %v", signal, pipeline.Exporters)
				}
			}
		})
	}
}

func TestConfigYAMLIncludesReleaseContractFields(t *testing.T) {
	cfg := testConfig(t)

	yaml := ConfigYAML(cfg)

	for _, want := range []string{
		"endpoint: 127.0.0.1:14317",
		"endpoint: 127.0.0.1:14318",
		"beaconjson:",
		// %q, matching how the config is generated. Hand-quoting the path only agreed with the
		// generator by accident: a POSIX path contains nothing that needs escaping, while a
		// Windows one is full of backslashes that a double-quoted YAML scalar must escape. The
		// assertion now checks the contract -- a correctly quoted path -- rather than a coincidence.
		fmt.Sprintf("path: %q", cfg.LogPath),
		"max_event_bytes: 65536",
		"rotate_bytes: 10485760",
		"rotate_archives: 5",
		"redact_secrets: true",
		"level: none",
		"receivers: [otlp]",
		"exporters: [beaconjson]",
		"  claude_api_body:\n",
		// Ahead of the fan-out to exporters, so Claude Code API bodies are filtered for every
		// destination, not only the local runtime log.
		"processors: [memory_limiter, claude_api_body, batch]",
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

// The processor and each converting exporter decide on their own whether to keep anything from a
// Claude Code API body, so the opt-in reaches all of them or none.
func TestConfigYAMLWritesTheModelContextOptInToEveryComponentThatReadsBodies(t *testing.T) {
	cfg := testConfig(t)
	cfg.Destinations = &endpointconfig.Destinations{FalconHEC: &endpointconfig.FalconHEC{
		Endpoint: "https://cloud.us.humio.com/api/v1/ingest/hec",
		Token:    "ingest-token",
	}}
	if yaml := ConfigYAML(cfg); strings.Contains(yaml, "capture_model_context") {
		t.Fatalf("ConfigYAML opted in without the flag:\n%s", yaml)
	}
	cfg.ClaudeCaptureModelContext = true
	yaml := ConfigYAML(cfg)
	for _, want := range []string{
		"  claude_api_body:\n    capture_model_context: true\n",
		"    redact_secrets: true\n    capture_model_context: true\n",
		"  falcon_hec:\n",
	} {
		if !strings.Contains(yaml, want) {
			t.Fatalf("ConfigYAML missing %q:\n%s", want, yaml)
		}
	}
	if got := strings.Count(yaml, "capture_model_context: true"); got != 3 {
		t.Fatalf("capture_model_context appears %d times, want processor, beaconjson and falcon_hec:\n%s", got, yaml)
	}
}

// Only with --claude-capture-model-context, which turns on Claude Code's raw API bodies, does a gRPC
// export grow past the receiver's 4 MiB default, so only then is the limit raised.
func TestConfigYAMLRaisesTheGRPCLimitOnlyForClaudeModelContext(t *testing.T) {
	cfg := testConfig(t)
	if yaml := ConfigYAML(cfg); strings.Contains(yaml, "max_recv_msg_size_mib") {
		t.Fatalf("ConfigYAML raised the gRPC limit by default:\n%s", yaml)
	}
	cfg.ClaudeCaptureModelContext = true
	yaml := ConfigYAML(cfg)
	if !strings.Contains(yaml, "      grpc:\n        endpoint: 127.0.0.1:14317\n        max_recv_msg_size_mib: 32\n      http:\n") {
		t.Fatalf("ConfigYAML missing the gRPC receiver limit under grpc:\n%s", yaml)
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
	// Each opt-in goes to beaconjson and to falcon_hec, which converts on its own.
	for _, want := range []string{"include_runtime_metrics: true", "include_codex_spans: true"} {
		if got := strings.Count(yaml, want); got != 2 {
			t.Fatalf("%q appears %d times, want beaconjson and falcon_hec:\n%s", want, got, yaml)
		}
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

func TestWriteConfigUsesPrivatePermissionsWithAHECToken(t *testing.T) {
	testenv.RequirePOSIXFileModes(t)
	for name, destinations := range map[string]*endpointconfig.Destinations{
		"splunk": {SplunkHEC: &endpointconfig.SplunkHEC{
			Endpoint: "https://splunk.example:8088/services/collector",
			Token:    "hec-token",
		}},
		"falcon": {FalconHEC: &endpointconfig.FalconHEC{
			Endpoint: "https://cloud.us.humio.com/api/v1/ingest/hec",
			Token:    "ingest-token",
		}},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.Destinations = destinations
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
		})
	}
}

func TestWaitForPortsAvailableWaitsForTransientListeners(t *testing.T) {
	fastPortPolling(t)
	grpcListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = grpcListener.Close()
		t.Fatal(err)
	}
	grpcPort := grpcListener.Addr().(*net.TCPAddr).Port
	httpPort := httpListener.Addr().(*net.TCPAddr).Port
	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = grpcListener.Close()
		_ = httpListener.Close()
	}()

	if err := WaitForPortsAvailable(grpcPort, httpPort, freeTestPort(t), 2*time.Second); err != nil {
		t.Fatalf("WaitForPortsAvailable returned error: %v", err)
	}
}

func TestWaitForPortsAvailableReportsPersistentConflict(t *testing.T) {
	fastPortPolling(t)
	grpcListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer grpcListener.Close()
	httpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpPort := httpListener.Addr().(*net.TCPAddr).Port
	_ = httpListener.Close()

	err = WaitForPortsAvailable(grpcListener.Addr().(*net.TCPAddr).Port, httpPort, freeTestPort(t), 10*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "OTLP gRPC port") {
		t.Fatalf("WaitForPortsAvailable error = %v, want gRPC port conflict", err)
	}
}

// stubCollectorName returns the fixture file name a collector binary must have to be discoverable
// on this platform.
//
// exec.LookPath resolves a bare name through PATHEXT on Windows, so a fixture written without an
// extension is invisible to it however its bits are set -- DiscoverBinary would report no collector
// installed while the file sat right there on PATH. The production BinaryName stays extensionless
// because LookPath is what applies the extension.
func stubCollectorName() string {
	if runtime.GOOS == "windows" {
		return BinaryName + ".exe"
	}
	return BinaryName
}

func TestDiscoverBinaryPrefersConfiguredExistingPath(t *testing.T) {
	bin := filepath.Join(t.TempDir(), stubCollectorName())
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
	bin := filepath.Join(dir, stubCollectorName())
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

// A package CLI run under sudo on a Mac with a Homebrew Beacon finds Homebrew's older
// beacon-otelcol first on PATH. The collector beside the CLI is the one from its own release, so
// it wins; a repair that took the PATH one refused to install and left the endpoint without its
// collector.
func TestDiscoverBinaryPrefersTheCollectorBesideTheCLIOverPath(t *testing.T) {
	onPath := filepath.Join(t.TempDir(), stubCollectorName())
	beside := filepath.Join(t.TempDir(), stubCollectorName())
	for _, bin := range []string{onPath, beside} {
		if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0755); err != nil {
			t.Fatalf("write fake collector: %v", err)
		}
	}
	t.Setenv("PATH", filepath.Dir(onPath))
	withCollectorDiscovery(t, nil, nil)
	discoverSiblingBinary = func() string { return beside }

	if got := DiscoverBinary(""); got != beside {
		t.Fatalf("DiscoverBinary = %q, want the collector beside the CLI %q", got, beside)
	}
}

func withCollectorDiscovery(t *testing.T, lookup func(string) (string, error), candidates func() []string) {
	t.Helper()
	oldLookPath := lookPath
	oldSibling := discoverSiblingBinary
	oldCandidates := discoverDefaultBinaryCandidates
	if lookup == nil {
		lookup = execLookPath
	}
	if candidates == nil {
		candidates = func() []string { return nil }
	}
	lookPath = lookup
	discoverSiblingBinary = func() string { return "" }
	discoverDefaultBinaryCandidates = candidates
	t.Cleanup(func() {
		lookPath = oldLookPath
		discoverSiblingBinary = oldSibling
		discoverDefaultBinaryCandidates = oldCandidates
	})
}

func execLookPath(file string) (string, error) {
	return exec.LookPath(file)
}

func TestConfigYAMLForwardsFilteredRedactedOTLPWhenConfigured(t *testing.T) {
	for _, tc := range []struct {
		protocol, endpoint, exporter string
	}{
		{endpointconfig.OTLPForwardProtocolGRPC, "otel.backend.example:4317", "otlp/forward"},
		{endpointconfig.OTLPForwardProtocolHTTP, "https://otel.backend.example", "otlphttp/forward"},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.Destinations = &endpointconfig.Destinations{OTLPForward: &endpointconfig.OTLPForward{
				Endpoint: tc.endpoint,
				Protocol: tc.protocol,
				Headers:  map[string]string{"x-b": "2", "authorization": "Bearer secret"},
				CAFile:   "/tmp/ca.pem",
			}}
			endpointconfig.NormalizeDestinations(&cfg)

			yaml := ConfigYAML(cfg)

			for _, want := range []string{
				"  " + tc.exporter + ":\n    endpoint: \"" + tc.endpoint + "\"\n    headers:\n      \"authorization\": \"Bearer secret\"\n      \"x-b\": \"2\"\n",
				// A down backend sheds forward data rather than filling memory_limiter.
				"    sending_queue:\n      enabled: true\n      num_consumers: 2\n      queue_size: 20\n",
				"    retry_on_failure:\n      enabled: true\n      max_elapsed_time: 120s\n",
				"    tls:\n      ca_file: \"/tmp/ca.pem\"\n",
				"  beacon_privacy: {}\n  beacon_forward_filter:\n    harnesses: [\"claude_code\", \"codex_cli\", \"codex_desktop\"]\n",
				"    logs/forward:\n      receivers: [otlp]\n      processors: [memory_limiter, beacon_forward_filter, claude_api_body, beacon_privacy, batch]\n      exporters: [" + tc.exporter + "]\n",
				"    traces/forward:\n      receivers: [otlp]\n      processors: [memory_limiter, beacon_forward_filter, beacon_privacy, batch]\n",
				"    metrics/forward:\n      receivers: [otlp]\n      processors: [memory_limiter, beacon_forward_filter, beacon_privacy, batch]\n",
				// Beacon's own pipelines are unchanged by a forward.
				"    logs:\n      receivers: [otlp]\n      processors: [memory_limiter, claude_api_body, batch]\n      exporters: [beaconjson]\n",
			} {
				if !strings.Contains(yaml, want) {
					t.Fatalf("ConfigYAML missing %q:\n%s", want, yaml)
				}
			}
		})
	}
}

func TestConfigYAMLHasNoForwardWhenDisabled(t *testing.T) {
	cfg := testConfig(t)
	cfg.Destinations = &endpointconfig.Destinations{OTLPForward: &endpointconfig.OTLPForward{}}
	endpointconfig.NormalizeDestinations(&cfg)
	if yaml := ConfigYAML(cfg); strings.Contains(yaml, "/forward") {
		t.Fatalf("ConfigYAML has a forward with none configured:\n%s", yaml)
	}
}

// Harness names are typed by the operator, so YAML syntax in one must not break the config the
// collector shares with Beacon's own pipelines.
func TestConfigYAMLQuotesForwardHarnessNames(t *testing.T) {
	cfg := testConfig(t)
	cfg.Destinations = &endpointconfig.Destinations{OTLPForward: &endpointconfig.OTLPForward{
		Endpoint:  "otel.example:4317",
		Harnesses: []string{"claude_code", "my agent #2", "foo: bar"},
	}}
	endpointconfig.NormalizeDestinations(&cfg)
	var parsed struct {
		Processors map[string]struct {
			Harnesses []string `yaml:"harnesses"`
		} `yaml:"processors"`
	}
	if err := yaml.Unmarshal([]byte(ConfigYAML(cfg)), &parsed); err != nil {
		t.Fatalf("collector config does not parse: %v", err)
	}
	got := parsed.Processors["beacon_forward_filter"].Harnesses
	if strings.Join(got, "|") != "claude_code|my agent #2|foo: bar" {
		t.Fatalf("harnesses = %q", got)
	}
}
