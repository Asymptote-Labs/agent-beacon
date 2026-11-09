package collector

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"gopkg.in/yaml.v3"
)

// shippedPeerAttribution is PeerAttribution as the package sets it for this platform, read before
// TestMain changes it.
var shippedPeerAttribution = PeerAttribution

// shippedMarkerRequired is peerLookupMarkerRequired as the package sets it for this platform.
var shippedMarkerRequired = peerLookupMarkerRequired

// The other tests in this package pin the config rendered without peer attribution (a platform with
// no lookup); the tests below set PeerAttribution themselves.
func TestMain(m *testing.M) {
	PeerAttribution = false
	peerLookupMarkerRequired = false
	os.Exit(m.Run())
}

// withPeerAttribution renders peer attribution the way Linux does, for any collector.
func withPeerAttribution(t *testing.T) {
	t.Helper()
	PeerAttribution = true
	t.Cleanup(func() { PeerAttribution = false })
}

// withPeerAttributionIfMarked renders it the way macOS and Windows do: only for a collector that
// lists PeerLookupExtension.
func withPeerAttributionIfMarked(t *testing.T) {
	t.Helper()
	withPeerAttribution(t)
	peerLookupMarkerRequired = true
	t.Cleanup(func() { peerLookupMarkerRequired = false })
}

// fakeCollector is an executable file for cfg.Collector.BinaryPath; what it lists comes from
// stubComponents.
func fakeCollector(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "beacon-otelcol")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func rendersPeer(text string) bool {
	return strings.Contains(text, PeerProcessor) || strings.Contains(text, OTLPHTTPReceiver) || strings.Contains(text, "peer_user")
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

// With peer attribution (Linux, macOS and Windows), OTLP/HTTP is served by beacon_otlp_http on the same port (so every runtime keeps
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

// Peer attribution is rendered on every platform the collector's lookup supports, and nowhere else.
func TestPeerAttributionIsOnWhereTheLookupExists(t *testing.T) {
	want := runtime.GOOS == "linux" || runtime.GOOS == "darwin" || runtime.GOOS == "windows"
	if shippedPeerAttribution != want {
		t.Fatalf("PeerAttribution on %s = %v, want %v", runtime.GOOS, shippedPeerAttribution, want)
	}
	// Linux collectors have done the lookup since beacon_peer shipped; on macOS and Windows only a
	// collector that says so does.
	if wantMarker := runtime.GOOS == "darwin" || runtime.GOOS == "windows"; shippedMarkerRequired != wantMarker {
		t.Fatalf("peerLookupMarkerRequired on %s = %v, want %v", runtime.GOOS, shippedMarkerRequired, wantMarker)
	}
}

// On macOS and Windows a collector from before peer attribution reached them lists beacon_peer and
// beacon_otlp_http, but its lookup names nobody there. Its config stays the previous one, so OTLP
// events keep the collector's account instead of losing their user, and install does not require
// the peer components of it.
func TestAnOlderCollectorKeepsThePreviousConfigWhereTheMarkerIsRequired(t *testing.T) {
	withPeerAttributionIfMarked(t)
	cfg := testConfig(t)
	cfg.Collector.BinaryPath = fakeCollector(t)

	stubComponents(t, peerComponents, nil) // beacon_peer and beacon_otlp_http, no marker
	if PeerAttributionFor(cfg.Collector.BinaryPath) {
		t.Fatal("PeerAttributionFor an unmarked collector = true")
	}
	text := ConfigYAML(cfg)
	if rendersPeer(text) {
		t.Fatalf("config for an unmarked collector renders peer attribution:\n%s", text)
	}
	if text != ConfigYAMLWithPeer(cfg, false) {
		t.Fatalf("config for an unmarked collector differs from the previous config:\n%s", text)
	}
	if err := WriteConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if written, err := os.ReadFile(cfg.Collector.ConfigPath); err != nil || rendersPeer(string(written)) {
		t.Fatalf("WriteConfig wrote peer attribution for an unmarked collector (%v):\n%s", err, written)
	}
	if err := CheckComponents(cfg.Collector.BinaryPath); err != nil {
		t.Fatalf("CheckComponents = %v, want an unmarked collector accepted", err)
	}
	// Even one without the peer components at all: the config it gets does not name them.
	stubComponents(t, strings.Replace(strings.Replace(peerComponents, "    - name: beacon_peer\n", "", 1), "    - name: beacon_otlp_http\n", "", 1), nil)
	if err := CheckComponents(cfg.Collector.BinaryPath); err != nil {
		t.Fatalf("CheckComponents = %v; a collector that gets no peer attribution needs neither component", err)
	}
	if rendersPeer(ConfigYAML(cfg)) {
		t.Fatal("config renders peer attribution for a collector without its components")
	}

	// A collector that cannot list its components is not judged, and gets the previous config.
	stubComponents(t, "", errors.New("unknown command"))
	if rendersPeer(ConfigYAML(cfg)) || CheckComponents(cfg.Collector.BinaryPath) != nil {
		t.Fatal("a collector that cannot list its components got peer attribution or was refused")
	}
	// As does a config whose collector cannot be found.
	cfg.Collector.BinaryPath = filepath.Join(t.TempDir(), "missing")
	stubComponents(t, markedPeerComponents, nil)
	if rendersPeer(ConfigYAML(cfg)) {
		t.Fatal("peer attribution rendered for a collector that does not exist")
	}
}

// A collector that lists the marker gets peer attribution, and must then have its components.
func TestAMarkedCollectorGetsPeerAttributionWhereTheMarkerIsRequired(t *testing.T) {
	withPeerAttributionIfMarked(t)
	cfg := testConfig(t)
	cfg.Collector.BinaryPath = fakeCollector(t)
	stubComponents(t, markedPeerComponents, nil)
	if !PeerAttributionFor(cfg.Collector.BinaryPath) {
		t.Fatal("PeerAttributionFor a marked collector = false")
	}
	text := ConfigYAML(cfg)
	if text != ConfigYAMLWithPeer(cfg, true) {
		t.Fatalf("config for a marked collector is not the peer config:\n%s", text)
	}
	out := render(t, cfg)
	if _, ok := out.Processors[PeerProcessor]; !ok {
		t.Fatalf("no %s processor:\n%s", PeerProcessor, text)
	}
	if out.Exporters["beaconjson"].(map[string]any)["peer_user"] != true {
		t.Fatalf("beaconjson peer_user missing:\n%s", text)
	}
	if err := CheckComponents(cfg.Collector.BinaryPath); err != nil {
		t.Fatalf("CheckComponents = %v, want a marked collector accepted", err)
	}
	stubComponents(t, strings.Replace(markedPeerComponents, "    - name: beacon_peer\n", "", 1), nil)
	if err := CheckComponents(cfg.Collector.BinaryPath); err == nil || !strings.Contains(err.Error(), `"beacon_peer" processor`) {
		t.Fatalf("CheckComponents = %v, want the missing processor named", err)
	}
}

// Linux needs no marker: every collector there renders and requires peer attribution, as before.
func TestLinuxNeedsNoMarker(t *testing.T) {
	withPeerAttribution(t)
	cfg := testConfig(t)
	cfg.Collector.BinaryPath = fakeCollector(t)
	for _, listing := range []string{peerComponents, markedPeerComponents} {
		stubComponents(t, listing, nil)
		if !PeerAttributionFor(cfg.Collector.BinaryPath) || ConfigYAML(cfg) != ConfigYAMLWithPeer(cfg, true) {
			t.Fatalf("Linux config without peer attribution for listing:\n%s", listing)
		}
	}
	stubComponents(t, "", errors.New("unknown command"))
	if !rendersPeer(ConfigYAML(cfg)) {
		t.Fatal("Linux config lost peer attribution for a collector that cannot list its components")
	}
	stubComponents(t, strings.Replace(peerComponents, "    - name: beacon_peer\n", "", 1), nil)
	if err := CheckComponents(cfg.Collector.BinaryPath); err == nil {
		t.Fatal("Linux accepted a collector without beacon_peer")
	}
}

// Without peer attribution the config is what it always was.
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

// markedPeerComponents is a collector that also lists the macOS/Windows lookup marker.
var markedPeerComponents = strings.Replace(peerComponents, "    - name: loopback_host\n", "    - name: loopback_host\n    - name: "+PeerLookupExtension+"\n", 1)

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
