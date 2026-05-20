package collector

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
)

const (
	BinaryName         = "beacon-otelcol"
	PackagedBinaryPath = "/opt/beacon/bin/beacon-otelcol"
)

var (
	lookPath                        = exec.LookPath
	currentExecutable               = os.Executable
	discoverDefaultBinaryCandidates = defaultBinaryCandidates
)

type Status struct {
	BinaryPath string `json:"binary_path,omitempty"`
	ConfigPath string `json:"config_path,omitempty"`
	GRPCPort   int    `json:"grpc_port"`
	HTTPPort   int    `json:"http_port"`
	GRPCReady  bool   `json:"grpc_ready"`
	HTTPReady  bool   `json:"http_ready"`
	Message    string `json:"message,omitempty"`
}

func ResolveBinary(configured string) (string, error) {
	if configured != "" {
		if err := validateExecutable(configured); err != nil {
			return "", fmt.Errorf("collector binary %q is not usable: %w", configured, err)
		}
		return configured, nil
	}
	if path := DiscoverBinary(""); path != "" {
		return path, nil
	}
	return "", fmt.Errorf("Beacon installation is missing the endpoint collector (%s); reinstall Beacon so %s is installed beside the beacon CLI, or pass --collector /path/to/%s for development and custom deployments", BinaryName, BinaryName, BinaryName)
}

func DiscoverBinary(configured string) string {
	if configured != "" {
		if err := validateExecutable(configured); err == nil {
			return configured
		}
	}
	if path, err := lookPath(BinaryName); err == nil && validateExecutable(path) == nil {
		return path
	}
	for _, path := range discoverDefaultBinaryCandidates() {
		if err := validateExecutable(path); err == nil {
			return path
		}
	}
	return ""
}

func defaultBinaryCandidates() []string {
	paths := []string{PackagedBinaryPath}
	if executable, err := currentExecutable(); err == nil {
		paths = append([]string{filepath.Join(filepath.Dir(executable), BinaryName)}, paths...)
	}
	return paths
}

func validateExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("is a directory")
	}
	if info.Mode().Perm()&0111 == 0 {
		return fmt.Errorf("is not executable")
	}
	return nil
}

func WriteConfig(cfg endpointconfig.Config) error {
	endpointconfig.NormalizeDestinations(&cfg)
	if err := endpointconfig.ValidateDestinations(cfg.Destinations); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cfg.Collector.ConfigPath), 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cfg.Collector.SpoolPath), 0755); err != nil {
		return err
	}
	perm := os.FileMode(0644)
	if endpointconfig.HasSecretDestinations(cfg) {
		perm = 0600
	}
	if err := os.WriteFile(cfg.Collector.ConfigPath, []byte(ConfigYAML(cfg)), perm); err != nil {
		return err
	}
	if endpointconfig.HasSecretDestinations(cfg) {
		return os.Chmod(cfg.Collector.ConfigPath, perm)
	}
	return nil
}

func ConfigYAML(cfg endpointconfig.Config) string {
	endpointconfig.NormalizeDestinations(&cfg)
	exporters := "[beaconjson]"
	splunkExporter := splunkHECYAML(cfg)
	if splunkExporter != "" {
		exporters = "[beaconjson, splunk_hec]"
	}
	runtimeMetricsYAML := ""
	if cfg.Collector.IncludeRuntimeMetrics {
		runtimeMetricsYAML = "    include_runtime_metrics: true\n"
	}
	codexSpansYAML := ""
	if cfg.Collector.IncludeCodexSpans {
		codexSpansYAML = "    include_codex_spans: true\n"
	}
	return fmt.Sprintf(`receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 127.0.0.1:%d
      http:
        endpoint: 127.0.0.1:%d

processors:
  memory_limiter:
    check_interval: 1s
    limit_mib: 128
  batch:
    timeout: 5s
    send_batch_size: 128

exporters:
  beaconjson:
    path: %q
    max_event_bytes: 65536
    rotate_bytes: 10485760
    redact_secrets: true
    content_retention: %q
%s
extensions:
  health_check:
    endpoint: 127.0.0.1:13133

service:
  telemetry:
    metrics:
      level: none
  extensions: [health_check]
  pipelines:
    logs:
      receivers: [otlp]
      processors: [memory_limiter, batch]
      exporters: %s
    traces:
      receivers: [otlp]
      processors: [memory_limiter, batch]
      exporters: %s
    metrics:
      receivers: [otlp]
      processors: [memory_limiter, batch]
      exporters: %s
`, cfg.Collector.GRPCPort, cfg.Collector.HTTPPort, cfg.LogPath, cfg.ContentRetention, runtimeMetricsYAML+codexSpansYAML+splunkExporter, exporters, exporters, exporters)
}

func splunkHECYAML(cfg endpointconfig.Config) string {
	if cfg.Destinations == nil || cfg.Destinations.SplunkHEC == nil || !cfg.Destinations.SplunkHEC.Enabled {
		return ""
	}
	splunk := cfg.Destinations.SplunkHEC
	var b strings.Builder
	fmt.Fprintf(&b, "  splunk_hec:\n")
	fmt.Fprintf(&b, "    token: %q\n", splunk.Token)
	fmt.Fprintf(&b, "    endpoint: %q\n", splunk.Endpoint)
	if splunk.Source != "" {
		fmt.Fprintf(&b, "    source: %q\n", splunk.Source)
	}
	if splunk.Sourcetype != "" {
		fmt.Fprintf(&b, "    sourcetype: %q\n", splunk.Sourcetype)
	}
	if splunk.Index != "" {
		fmt.Fprintf(&b, "    index: %q\n", splunk.Index)
	}
	if splunk.InsecureSkipVerify || splunk.CAFile != "" {
		fmt.Fprintf(&b, "    tls:\n")
		if splunk.InsecureSkipVerify {
			fmt.Fprintf(&b, "      insecure_skip_verify: true\n")
		}
		if splunk.CAFile != "" {
			fmt.Fprintf(&b, "      ca_file: %q\n", splunk.CAFile)
		}
	}
	return b.String()
}

func CheckStatus(cfg endpointconfig.Config) Status {
	binary := DiscoverBinary(cfg.Collector.BinaryPath)
	status := Status{
		BinaryPath: binary,
		ConfigPath: cfg.Collector.ConfigPath,
		GRPCPort:   cfg.Collector.GRPCPort,
		HTTPPort:   cfg.Collector.HTTPPort,
		GRPCReady:  portOpen(cfg.Collector.GRPCPort),
		HTTPReady:  portOpen(cfg.Collector.HTTPPort),
	}
	if binary == "" {
		status.Message = "OpenTelemetry Collector binary was not found on PATH"
	} else if !status.GRPCReady && !status.HTTPReady {
		status.Message = "Collector ports are not listening"
	}
	return status
}

func PortAvailable(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

func portOpen(port int) bool {
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func LaunchAgentPlist(cfg endpointconfig.Config) string {
	binary := DiscoverBinary(cfg.Collector.BinaryPath)
	if binary == "" {
		binary = BinaryName
	}
	label := "com.beacon.endpoint.collector"
	if cfg.UserMode {
		label = "com.beacon.endpoint.collector.user"
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>--config</string>
    <string>%s</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
</dict>
</plist>
`, label, binary, cfg.Collector.ConfigPath)
}

func WriteLaunchPlist(cfg endpointconfig.Config) (string, error) {
	var path string
	if cfg.UserMode {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, "Library", "LaunchAgents", "com.beacon.endpoint.collector.plist")
	} else {
		path = "/Library/LaunchDaemons/com.beacon.endpoint.collector.plist"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", err
	}
	return path, os.WriteFile(path, []byte(LaunchAgentPlist(cfg)), 0644)
}
