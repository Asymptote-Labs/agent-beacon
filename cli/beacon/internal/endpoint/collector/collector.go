package collector

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/brewpath"
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/sysacl"
)

const (
	BinaryName         = "beacon-otelcol"
	PackagedBinaryPath = "/opt/beacon/bin/beacon-otelcol"
)

var (
	lookPath                        = exec.LookPath
	currentExecutable               = os.Executable
	discoverSiblingBinary           = siblingBinary
	discoverDefaultBinaryCandidates = defaultBinaryCandidates
	// pollInterval is how often the readiness and port waits re-check; tests shorten it.
	pollInterval = 250 * time.Millisecond
)

type Status struct {
	BinaryPath  string `json:"binary_path,omitempty"`
	ConfigPath  string `json:"config_path,omitempty"`
	GRPCPort    int    `json:"grpc_port"`
	HTTPPort    int    `json:"http_port"`
	HealthPort  int    `json:"health_port"`
	GRPCReady   bool   `json:"grpc_ready"`
	HTTPReady   bool   `json:"http_ready"`
	HealthReady bool   `json:"health_ready"`
	Message     string `json:"message,omitempty"`
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
	// The collector beside this CLI comes from the same release, so it comes before PATH. An older
	// beacon-otelcol earlier on PATH (a Homebrew install on a Mac that also has the package, which
	// sudo finds first) can lack components this CLI's collector config needs, and a repair that
	// took it refused to install.
	if sibling := discoverSiblingBinary(); sibling != "" && validateExecutable(sibling) == nil {
		return sibling
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

// defaultBinaryCandidates lists where to look for the collector besides PATH.
//
// A sibling of the running CLI comes first, because that is what an extracted release archive looks
// like: beacon and beacon-otelcol next to each other in whatever directory the user unpacked them
// into. The machine-wide install location follows, for a package install.
//
// Both are asked for by their on-disk name rather than by BinaryName. Those differ on Windows -- the
// file is beacon-otelcol.exe, while BinaryName is what exec.LookPath is given so PATHEXT can resolve
// it -- and using the PATH spelling for a direct stat meant an extracted archive reported no
// collector at all.
func defaultBinaryCandidates() []string {
	paths := packagedBinaryPaths()
	if sibling := siblingBinary(); sibling != "" {
		paths = append([]string{sibling}, paths...)
	}
	return paths
}

// siblingBinary is the collector beside the running CLI, or "" when the executable is unknown.
// A Homebrew CLI runs from its versioned keg, and the collector path found here goes into the
// service unit, so it is mapped to the <prefix>/bin link that survives `brew upgrade`.
func siblingBinary() string {
	executable, err := currentExecutable()
	if err != nil {
		return ""
	}
	return brewpath.Stable(filepath.Join(filepath.Dir(executable), binaryFileName()))
}

func validateExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("is a directory")
	}
	if !isExecutableMode(info) {
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
	if endpointconfig.HasSecretDestinations(cfg) {
		// The collector config renders the destinations' tokens and headers. On a system install
		// it is created SYSTEM and Administrators only (the 0600 mode means nothing on Windows,
		// and the tree's DACL lets every user read below it); per-user configs keep the mode.
		if !cfg.UserMode {
			return sysacl.WriteSecretFile(cfg.Collector.ConfigPath, []byte(ConfigYAML(cfg)))
		}
		if err := os.WriteFile(cfg.Collector.ConfigPath, []byte(ConfigYAML(cfg)), 0600); err != nil {
			return err
		}
		return os.Chmod(cfg.Collector.ConfigPath, 0600)
	}
	return os.WriteFile(cfg.Collector.ConfigPath, []byte(ConfigYAML(cfg)), 0644)
}

// IngestExtension is the collector extension that serves the local ingest socket in the restricted
// log access mode.
const IngestExtension = "beacon_ingest"

// PeerProcessor and OTLPHTTPReceiver are the collector components that record which local account
// sent each OTLP export: the receiver serves OTLP/HTTP keeping the client's port, which the upstream
// one drops, and the processor asks the kernel who owns that client socket (gRPC already carries
// the port). See collector-builder/exporter/beaconjsonexporter/peerprocessor.
const (
	PeerProcessor    = "beacon_peer"
	OTLPHTTPReceiver = "beacon_otlp_http"
)

// PeerAttribution reports whether ConfigYAML can render peer attribution: on the platforms where the
// processor can ask the kernel who owns a loopback client socket (Linux: sock_diag or /proc/net/tcp;
// macOS: the net.inet.tcp.pcblist_n table; Windows: GetExtendedTcpTable and the owning process's
// token). Elsewhere it would name nobody, so the upstream HTTP receiver stays as it was. On macOS and
// Windows it also takes a collector that can do the lookup (PeerAttributionFor). A var so tests on
// any platform can render either form.
var PeerAttribution = runtime.GOOS == "linux" || runtime.GOOS == "darwin" || runtime.GOOS == "windows"

// PeerLookupExtension is the marker a collector lists in `beacon-otelcol components` when its
// beacon_peer can identify senders on macOS and Windows (collector-builder/.../peerlookupextension).
// No config names it.
const PeerLookupExtension = "beacon_peer_lookup"

// peerLookupMarkerRequired is where a collector must list PeerLookupExtension for its config to
// render peer attribution: macOS and Windows. Collectors released before peer attribution reached
// them already carry beacon_peer and beacon_otlp_http, with a lookup that always answers
// "unsupported" there; a config with peer_user would give every OTLP event from such a collector no
// user at all, where it used to carry the collector's account. So a new CLI with an older collector
// (--collector, a CLI-only upgrade, a repair before the collector is replaced) keeps the previous
// config. Linux collectors have done the lookup since beacon_peer first shipped, so Linux needs no
// marker and renders exactly as before. A var so tests on any platform can take either path.
var peerLookupMarkerRequired = runtime.GOOS == "darwin" || runtime.GOOS == "windows"

// PeerAttributionFor reports whether the config for the collector binary renders peer attribution.
// Where the marker is required, a binary that does not list it -- including one that cannot list
// its components at all -- gets the config without peer attribution: it records the collector's
// account, as before, rather than no one.
func PeerAttributionFor(binary string) bool {
	if !PeerAttribution {
		return false
	}
	if !peerLookupMarkerRequired {
		return true
	}
	if binary == "" {
		return false
	}
	listed, ok := readComponents(binary)
	return ok && listed.has("extension", PeerLookupExtension)
}

// peerAttributionForConfig is PeerAttributionFor the collector cfg runs: the binary install,
// repair and the CI session resolve from cfg.Collector.BinaryPath, and check with
// CheckComponentsFor, before they write the config.
func peerAttributionForConfig(cfg endpointconfig.Config) bool {
	if !PeerAttribution {
		return false
	}
	if !peerLookupMarkerRequired {
		return true
	}
	binary, err := ResolveBinary(cfg.Collector.BinaryPath)
	if err != nil {
		return false
	}
	return PeerAttributionFor(binary)
}

// ConfigYAML renders the collector config for cfg, with peer attribution where the collector cfg
// runs can do the lookup (see PeerAttributionFor).
func ConfigYAML(cfg endpointconfig.Config) string {
	return ConfigYAMLWithPeer(cfg, peerAttributionForConfig(cfg))
}

// ConfigYAMLWithPeer renders the collector config for cfg with or without peer attribution.
func ConfigYAMLWithPeer(cfg endpointconfig.Config, peer bool) string {
	endpointconfig.NormalizeDestinations(&cfg)
	exporterNames := []string{"beaconjson"}
	splunkExporter := splunkHECYAML(cfg)
	if splunkExporter != "" {
		exporterNames = append(exporterNames, "splunk_hec")
	}
	falconExporter := falconHECYAML(cfg, peer)
	if falconExporter != "" {
		exporterNames = append(exporterNames, "falcon_hec")
	}
	exporters := "[" + strings.Join(exporterNames, ", ") + "]"
	forwardExporter, forwardName := otlpForwardYAML(cfg)
	// The OTLP forward stands in for the export a runtime had to another backend before Beacon's
	// settings pointed it at this collector. It has pipelines of its own, fed by the same receiver,
	// that keep Beacon's own pipelines independent of that backend and give it only:
	//   - the runtimes it is meant to carry (beacon_forward_filter), never the browser extension's
	//     chats or another runtime's OTLP;
	//   - Claude Code API body events under the same policy as everywhere else (claude_api_body);
	//   - secrets redacted, as for the HEC exporters (beacon_privacy).
	forwardPipelines, forwardFilterYAML := "", ""
	if forwardName != "" {
		// Quoted like every other value the forward renders: a harness name the operator typed can
		// hold YAML syntax (`#`, `: `), and a config the collector cannot parse stops Beacon's own
		// pipelines too.
		quoted := make([]string, 0, len(cfg.Destinations.OTLPForward.Harnesses))
		for _, name := range cfg.Destinations.OTLPForward.Harnesses {
			quoted = append(quoted, fmt.Sprintf("%q", name))
		}
		forwardFilterYAML = "  " + ForwardFilterProcessor + ":\n    harnesses: [" + strings.Join(quoted, ", ") + "]\n"
		for _, signal := range []string{"logs", "traces", "metrics"} {
			apiBody := ""
			if signal == "logs" {
				apiBody = "claude_api_body, "
			}
			forwardPipelines += fmt.Sprintf("    %s/forward:\n      receivers: [otlp]\n      processors: [memory_limiter, %s, %sbeacon_privacy, batch]\n      exporters: [%s]\n", signal, ForwardFilterProcessor, apiBody, forwardName)
		}
	}
	// The direct HEC exporters send what they receive, so beacon_privacy redacts before the
	// fanout. beaconjson redacts in its own writer; with no HEC exporter the processor would only
	// repeat that on every record.
	privacyYAML, privacyStep := "", ""
	if len(exporterNames) > 1 {
		privacyYAML, privacyStep = "  beacon_privacy: {}\n", "beacon_privacy, "
	}
	if forwardName != "" {
		privacyYAML = "  beacon_privacy: {}\n" + forwardFilterYAML
	}
	runtimeMetricsYAML := ""
	if cfg.Collector.IncludeRuntimeMetrics {
		runtimeMetricsYAML = "    include_runtime_metrics: true\n"
	}
	codexSpansYAML := ""
	if cfg.Collector.IncludeCodexSpans {
		codexSpansYAML = "    include_codex_spans: true\n"
	}
	// The processor and the exporters each decide from their own config whether to keep anything
	// from a Claude Code API body, so the opt-in is written to all of them.
	modelContextYAML := ""
	// Claude Code batches its log records into one gRPC export, and with raw API bodies on each
	// model request adds a request and a response body of up to the content limit install sets.
	// The receiver's 4 MiB default rejects a whole export over it, and with it every prompt and
	// tool event in the batch. The receiver applies the limit before claude_api_body runs.
	grpcLimitYAML := ""
	if cfg.ClaudeCaptureModelContext {
		modelContextYAML = "    capture_model_context: true\n"
		grpcLimitYAML = "        max_recv_msg_size_mib: 32\n"
	}
	// The restricted log access mode makes the collector the only writer of the system log: hooks
	// send their events to beacon_ingest over a local socket instead of appending, so the log can
	// be 0640 root:beacon-log. Its rotation matches beaconjson's, since both append to one log.
	// Peer attribution. Without it every OTLP event records the collector's own account (root, on
	// a system install), whoever sent it. beacon_peer goes right after memory_limiter and before
	// batch, which would drop the connection details it reads; beaconjson is told to trust what it
	// writes, and only then. The OTLP forward's pipelines do not run it, so Beacon's attribution
	// never reaches that backend.
	receivers, httpReceiverYAML, otlpHTTPYAML, peerYAML, peerStep, peerExporterYAML := "[otlp]", "", "", "", "", ""
	if peer {
		receivers = "[otlp, " + OTLPHTTPReceiver + "]"
		httpReceiverYAML = fmt.Sprintf("  %s:\n    endpoint: 127.0.0.1:%d\n%s", OTLPHTTPReceiver, cfg.Collector.HTTPPort, loopbackHostAuthYAML("    "))
		peerYAML = "  " + PeerProcessor + ": {}\n"
		peerStep = PeerProcessor + ", "
		peerExporterYAML = "    peer_user: true\n"
	} else {
		otlpHTTPYAML = fmt.Sprintf("      http:\n        endpoint: 127.0.0.1:%d\n%s", cfg.Collector.HTTPPort, loopbackHostAuthYAML("        "))
	}
	forwardPipelines = strings.ReplaceAll(forwardPipelines, "receivers: [otlp]", "receivers: "+receivers)
	ingestYAML, extensions := "", "[health_check, loopback_host]"
	if cfg.RestrictedLogAccess() {
		ingestYAML = fmt.Sprintf("  %s:\n    log_path: %q\n    rotate_bytes: 10485760\n    rotate_archives: 5\n", IngestExtension, cfg.LogPath)
		extensions = "[health_check, loopback_host, " + IngestExtension + "]"
	}
	return fmt.Sprintf(`receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 127.0.0.1:%d
%s%s%s

processors:
  memory_limiter:
    check_interval: 1s
    limit_mib: 128
  batch:
    timeout: 5s
    send_batch_size: 128
  claude_api_body:
%s
%s%sexporters:
  beaconjson:
    path: %q
    max_event_bytes: 65536
    rotate_bytes: 10485760
    rotate_archives: 5
    redact_secrets: true
%s
extensions:
  health_check:
    endpoint: 127.0.0.1:%d
  loopback_host:
%s
service:
  telemetry:
    metrics:
      level: none
  extensions: %s
  pipelines:
    logs:
      receivers: %s
      processors: [memory_limiter, %sclaude_api_body, %sbatch]
      exporters: %s
    traces:
      receivers: %s
      processors: [memory_limiter, %s%sbatch]
      exporters: %s
    metrics:
      receivers: %s
      processors: [memory_limiter, %s%sbatch]
      exporters: %s
%s`, cfg.Collector.GRPCPort, grpcLimitYAML, otlpHTTPYAML, httpReceiverYAML, modelContextYAML, privacyYAML, peerYAML, cfg.LogPath,
		runtimeMetricsYAML+codexSpansYAML+modelContextYAML+peerExporterYAML+splunkExporter+falconExporter+forwardExporter,
		endpointconfig.HealthCheckPort(cfg.Collector), ingestYAML, extensions,
		receivers, peerStep, privacyStep, exporters,
		receivers, peerStep, privacyStep, exporters,
		receivers, peerStep, privacyStep, exporters,
		forwardPipelines)
}

// loopbackHostAuthYAML attaches the loopback_host authenticator to an OTLP HTTP receiver, at the
// indentation of that receiver's settings. It refuses any request whose Host header is not a
// loopback name, so a DNS-rebinding page cannot post telemetry; include_metadata is how confighttp
// hands the Host header to it. beacon_otlp_http is built on the same confighttp server, so it takes
// the same two settings.
func loopbackHostAuthYAML(indent string) string {
	return indent + "include_metadata: true\n" + indent + "auth:\n" + indent + "  authenticator: loopback_host\n"
}

// ForwardFilterProcessor keeps an OTLP forward to the runtimes it is meant to carry.
const ForwardFilterProcessor = "beacon_forward_filter"

// OTLPForwardExporters are the collector exporters an OTLP forward is rendered with, by protocol.
var OTLPForwardExporters = map[string]string{
	endpointconfig.OTLPForwardProtocolGRPC: "otlp",
	endpointconfig.OTLPForwardProtocolHTTP: "otlphttp",
}

// otlpForwardYAML renders the exporter for the configured OTLP forward and returns its name, or
// "" for both when forwarding is off.
func otlpForwardYAML(cfg endpointconfig.Config) (string, string) {
	if cfg.Destinations == nil || cfg.Destinations.OTLPForward == nil || !cfg.Destinations.OTLPForward.Enabled {
		return "", ""
	}
	forward := cfg.Destinations.OTLPForward
	kind := OTLPForwardExporters[forward.Protocol]
	if kind == "" {
		kind = OTLPForwardExporters[endpointconfig.OTLPForwardProtocolGRPC]
	}
	name := kind + "/forward"
	var b strings.Builder
	fmt.Fprintf(&b, "  %s:\n", name)
	fmt.Fprintf(&b, "    endpoint: %q\n", forward.Endpoint)
	if len(forward.Headers) > 0 {
		keys := make([]string, 0, len(forward.Headers))
		for key := range forward.Headers {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		fmt.Fprintf(&b, "    headers:\n")
		for _, key := range keys {
			fmt.Fprintf(&b, "      %q: %q\n", key, forward.Headers[key])
		}
	}
	// Bounded well below memory_limiter's 128 MiB, so a backend that is down or slow sheds forward
	// data after a couple of minutes instead of holding enough in memory to make the limiter
	// refuse Beacon's own pipelines.
	fmt.Fprintf(&b, "    timeout: 10s\n")
	fmt.Fprintf(&b, "    sending_queue:\n      enabled: true\n      num_consumers: 2\n      queue_size: 20\n")
	fmt.Fprintf(&b, "    retry_on_failure:\n      enabled: true\n      max_elapsed_time: 120s\n")
	if forward.Insecure || forward.InsecureSkipVerify || forward.CAFile != "" {
		fmt.Fprintf(&b, "    tls:\n")
		if forward.Insecure {
			fmt.Fprintf(&b, "      insecure: true\n")
		}
		if forward.InsecureSkipVerify {
			fmt.Fprintf(&b, "      insecure_skip_verify: true\n")
		}
		if forward.CAFile != "" {
			fmt.Fprintf(&b, "      ca_file: %q\n", forward.CAFile)
		}
	}
	return b.String(), name
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

func falconHECYAML(cfg endpointconfig.Config, peer bool) string {
	if cfg.Destinations == nil || cfg.Destinations.FalconHEC == nil || !cfg.Destinations.FalconHEC.Enabled {
		return ""
	}
	falcon := cfg.Destinations.FalconHEC
	var b strings.Builder
	fmt.Fprintf(&b, "  falcon_hec:\n")
	fmt.Fprintf(&b, "    token: %q\n", falcon.Token)
	fmt.Fprintf(&b, "    endpoint: %q\n", falcon.Endpoint)
	if cfg.Collector.IncludeRuntimeMetrics {
		fmt.Fprintf(&b, "    include_runtime_metrics: true\n")
	}
	if cfg.Collector.IncludeCodexSpans {
		fmt.Fprintf(&b, "    include_codex_spans: true\n")
	}
	if cfg.ClaudeCaptureModelContext {
		fmt.Fprintf(&b, "    capture_model_context: true\n")
	}
	if peer {
		// falcon_hec converts on its own, after beacon_peer in the same pipelines, so it is told to
		// trust the account beacon_peer resolved just as beaconjson is; otherwise LogScale would
		// record the collector's account for an event runtime.jsonl attributes to its sender.
		fmt.Fprintf(&b, "    peer_user: true\n")
	}
	if falcon.Source != "" {
		fmt.Fprintf(&b, "    source: %q\n", falcon.Source)
	}
	if falcon.Sourcetype != "" {
		fmt.Fprintf(&b, "    sourcetype: %q\n", falcon.Sourcetype)
	}
	if falcon.Index != "" {
		fmt.Fprintf(&b, "    index: %q\n", falcon.Index)
	}
	if falcon.InsecureSkipVerify {
		fmt.Fprintf(&b, "    insecure_skip_verify: true\n")
	}
	if falcon.CAFile != "" {
		fmt.Fprintf(&b, "    ca_file: %q\n", falcon.CAFile)
	}
	return b.String()
}

func CheckStatus(cfg endpointconfig.Config) Status {
	binary := DiscoverBinary(cfg.Collector.BinaryPath)
	healthPort := endpointconfig.HealthCheckPort(cfg.Collector)
	status := Status{
		BinaryPath:  binary,
		ConfigPath:  cfg.Collector.ConfigPath,
		GRPCPort:    cfg.Collector.GRPCPort,
		HTTPPort:    cfg.Collector.HTTPPort,
		HealthPort:  healthPort,
		GRPCReady:   portOpen(cfg.Collector.GRPCPort),
		HTTPReady:   portOpen(cfg.Collector.HTTPPort),
		HealthReady: healthReady(healthPort),
	}
	if binary == "" {
		status.Message = "OpenTelemetry Collector binary was not found on PATH"
	} else if !status.GRPCReady && !status.HTTPReady {
		status.Message = "Collector ports are not listening"
	} else if !status.HealthReady {
		status.Message = fmt.Sprintf("Collector health check on 127.0.0.1:%d is not ready", healthPort)
	}
	return status
}

func WaitUntilReady(cfg endpointconfig.Config, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		status := CheckStatus(cfg)
		if status.GRPCReady && status.HTTPReady && status.HealthReady {
			return nil
		}
		if time.Now().After(deadline) {
			if status.Message != "" {
				return fmt.Errorf("%s", status.Message)
			}
			return fmt.Errorf("collector did not become ready before timeout")
		}
		time.Sleep(pollInterval)
	}
}

// WaitForPortsAvailable waits for every port the collector will bind -- the two OTLP receivers
// and the health_check extension -- to be free, and names the ones that stay taken.
//
// The health port is checked with the others because it fails the collector just as surely: a
// second collector whose OTLP ports were free but whose health port was held by the first used to
// pass this wait, exit on start, and be reported as "Collector ports are not listening" (#447).
func WaitForPortsAvailable(grpcPort, httpPort, healthPort int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		grpcAvailable := PortAvailable(grpcPort)
		httpAvailable := PortAvailable(httpPort)
		healthAvailable := PortAvailable(healthPort)
		if grpcAvailable && httpAvailable && healthAvailable {
			return nil
		}
		if time.Now().After(deadline) {
			var busy []string
			switch {
			case !grpcAvailable && !httpAvailable:
				busy = append(busy, fmt.Sprintf("OTLP gRPC port %d and HTTP port %d are already in use", grpcPort, httpPort))
			case !grpcAvailable:
				busy = append(busy, fmt.Sprintf("OTLP gRPC port %d is already in use", grpcPort))
			case !httpAvailable:
				busy = append(busy, fmt.Sprintf("OTLP HTTP port %d is already in use", httpPort))
			}
			if !healthAvailable {
				busy = append(busy, fmt.Sprintf("collector health-check port %d is already in use (another Beacon collector, such as a system-mode install, may hold it; choose another with --health-port)", healthPort))
			}
			return fmt.Errorf("%s", strings.Join(busy, "; "))
		}
		time.Sleep(pollInterval)
	}
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

func healthReady(port int) bool {
	client := http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}
