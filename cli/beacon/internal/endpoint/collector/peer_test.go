package collector

import (
	"os"
	"strings"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"gopkg.in/yaml.v3"
)

// The other tests in this package pin the config every platform but Linux renders; the tests below
// set PeerAttribution themselves.
func TestMain(m *testing.M) {
	PeerAttribution = false
	os.Exit(m.Run())
}

func withPeerAttribution(t *testing.T) {
	t.Helper()
	PeerAttribution = true
	t.Cleanup(func() { PeerAttribution = false })
}

type renderedConfig struct {
	Receivers  map[string]any `yaml:"receivers"`
	Processors map[string]any `yaml:"processors"`
	Exporters  map[string]any `yaml:"exporters"`
	Service    struct {
		Pipelines map[string]struct {
			Receivers  []string `yaml:"receivers"`
			Processors []string `yaml:"processors"`
			Exporters  []string `yaml:"exporters"`
		} `yaml:"pipelines"`
	} `yaml:"service"`
}

func render(t *testing.T, cfg endpointconfig.Config) renderedConfig {
	t.Helper()
	var out renderedConfig
	if err := yaml.Unmarshal([]byte(ConfigYAML(cfg)), &out); err != nil {
		t.Fatalf("ConfigYAML is not YAML: %v\n%s", err, ConfigYAML(cfg))
	}
	return out
}

// On Linux, OTLP/HTTP is served by beacon_otlp_http on the same port (so every runtime keeps
// exporting where it did), every Beacon pipeline runs beacon_peer before batch, and beaconjson
// trusts what it wrote.
func TestConfigYAMLRendersPeerAttribution(t *testing.T) {
	withPeerAttribution(t)
	cfg := testConfig(t)
	text := ConfigYAML(cfg)
	out := render(t, cfg)

	otlp := out.Receivers["otlp"].(map[string]any)["protocols"].(map[string]any)
	if _, ok := otlp["http"]; ok {
		t.Fatalf("otlp still serves HTTP; the port belongs to %s:\n%s", OTLPHTTPReceiver, text)
	}
	if _, ok := otlp["grpc"]; !ok {
		t.Fatalf("otlp lost gRPC:\n%s", text)
	}
	if got := out.Receivers[OTLPHTTPReceiver].(map[string]any)["endpoint"]; got != "127.0.0.1:14318" {
		t.Fatalf("%s endpoint = %v, want the configured HTTP port on loopback", OTLPHTTPReceiver, got)
	}
	// The receiver that takes over the HTTP port keeps the DNS-rebinding guard the upstream one has.
	httpReceiver := out.Receivers[OTLPHTTPReceiver].(map[string]any)
	if httpReceiver["include_metadata"] != true {
		t.Fatalf("%s include_metadata = %v, want true so the Host header reaches loopback_host", OTLPHTTPReceiver, httpReceiver["include_metadata"])
	}
	if auth, _ := httpReceiver["auth"].(map[string]any); auth["authenticator"] != "loopback_host" {
		t.Fatalf("%s auth = %v, want the loopback_host authenticator:\n%s", OTLPHTTPReceiver, httpReceiver["auth"], text)
	}
	if _, ok := out.Processors[PeerProcessor]; !ok {
		t.Fatalf("no %s processor:\n%s", PeerProcessor, text)
	}
	if got := out.Exporters["beaconjson"].(map[string]any)["peer_user"]; got != true {
		t.Fatalf("beaconjson peer_user = %v, want true", got)
	}
	for _, signal := range []string{"logs", "traces", "metrics"} {
		p := out.Service.Pipelines[signal]
		if strings.Join(p.Receivers, ",") != "otlp,"+OTLPHTTPReceiver {
			t.Errorf("%s receivers = %v", signal, p.Receivers)
		}
		if len(p.Processors) < 2 || p.Processors[0] != "memory_limiter" || p.Processors[1] != PeerProcessor || p.Processors[len(p.Processors)-1] != "batch" {
			t.Errorf("%s processors = %v, want memory_limiter, %s first and batch last", signal, p.Processors, PeerProcessor)
		}
	}
}

// falcon_hec runs its own converter after beacon_peer, so it is told to trust the peer too, or
// LogScale would record the collector's account for events runtime.jsonl attributes to the sender.
func TestPeerAttributionReachesFalconHEC(t *testing.T) {
	cfg := testConfig(t)
	cfg.Destinations = &endpointconfig.Destinations{
		FalconHEC: &endpointconfig.FalconHEC{Endpoint: "https://falcon.example/api/v1/ingest/hec", Token: "test-token"},
	}
	endpointconfig.NormalizeDestinations(&cfg)
	if text := ConfigYAML(cfg); strings.Contains(text, "peer_user") {
		t.Fatalf("peer_user rendered without peer attribution:\n%s", text)
	}
	withPeerAttribution(t)
	out := render(t, cfg)
	falcon, ok := out.Exporters["falcon_hec"].(map[string]any)
	if !ok {
		t.Fatalf("no falcon_hec exporter:\n%s", ConfigYAML(cfg))
	}
	if falcon["peer_user"] != true {
		t.Fatalf("falcon_hec peer_user = %v, want true", falcon["peer_user"])
	}
}

// The OTLP forward hears HTTP exports too, but never runs beacon_peer: Beacon's attribution of
// local accounts stays on this machine.
func TestPeerAttributionStaysOutOfTheOTLPForward(t *testing.T) {
	withPeerAttribution(t)
	cfg := testConfig(t)
	cfg.Destinations = &endpointconfig.Destinations{OTLPForward: &endpointconfig.OTLPForward{
		Endpoint: "https://otel.backend.example",
		Protocol: endpointconfig.OTLPForwardProtocolHTTP,
	}}
	endpointconfig.NormalizeDestinations(&cfg)
	out := render(t, cfg)
	for _, signal := range []string{"logs/forward", "traces/forward", "metrics/forward"} {
		p, ok := out.Service.Pipelines[signal]
		if !ok {
			t.Fatalf("no %s pipeline", signal)
		}
		if strings.Join(p.Receivers, ",") != "otlp,"+OTLPHTTPReceiver {
			t.Errorf("%s receivers = %v, want both, so HTTP exports are still forwarded", signal, p.Receivers)
		}
		for _, proc := range p.Processors {
			if proc == PeerProcessor {
				t.Errorf("%s runs %s: %v", signal, PeerProcessor, p.Processors)
			}
		}
	}
}

// Everywhere else the config is what it always was.
func TestConfigYAMLWithoutPeerAttributionIsUnchanged(t *testing.T) {
	cfg := testConfig(t)
	text := ConfigYAML(cfg)
	for _, absent := range []string{OTLPHTTPReceiver, PeerProcessor, "peer_user"} {
		if strings.Contains(text, absent) {
			t.Fatalf("config names %s without peer attribution:\n%s", absent, text)
		}
	}
	if !strings.Contains(text, "      http:\n        endpoint: 127.0.0.1:14318\n        include_metadata: true\n        auth:\n          authenticator: loopback_host\n") {
		t.Fatalf("otlp lost its HTTP protocol:\n%s", text)
	}
}

const peerComponents = `buildinfo:
    command: beacon-otelcol
receivers:
    - name: otlp
    - name: beacon_otlp_http
processors:
    - name: batch
    - name: memory_limiter
    - name: claude_api_body
    - name: beacon_privacy
    - name: beacon_peer
extensions:
    - name: health_check
    - name: loopback_host
`

// A collector from before peer attribution would refuse the whole config at start; install says so
// first, as it does for every other component.
func TestCheckComponentsRequiresPeerAttributionWhereItIsRendered(t *testing.T) {
	withPeerAttribution(t)
	stubComponents(t, peerComponents, nil)
	if err := CheckComponents("/opt/beacon/bin/beacon-otelcol"); err != nil {
		t.Fatalf("CheckComponents = %v, want a current collector accepted", err)
	}
	stubComponents(t, strings.Replace(peerComponents, "    - name: beacon_otlp_http\n", "", 1), nil)
	if err := CheckComponents("/opt/beacon/bin/beacon-otelcol"); err == nil || !strings.Contains(err.Error(), `"beacon_otlp_http" receiver`) {
		t.Fatalf("CheckComponents = %v, want the missing receiver named", err)
	}
	stubComponents(t, strings.Replace(peerComponents, "    - name: beacon_peer\n", "", 1), nil)
	if err := CheckComponents("/opt/beacon/bin/beacon-otelcol"); err == nil || !strings.Contains(err.Error(), `"beacon_peer" processor`) {
		t.Fatalf("CheckComponents = %v, want the missing processor named", err)
	}

	PeerAttribution = false
	stubComponents(t, strings.Replace(strings.Replace(peerComponents, "    - name: beacon_peer\n", "", 1), "    - name: beacon_otlp_http\n", "", 1), nil)
	if err := CheckComponents("/opt/beacon/bin/beacon-otelcol"); err != nil {
		t.Fatalf("CheckComponents = %v; without peer attribution neither is needed", err)
	}
}
