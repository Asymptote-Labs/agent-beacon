package lifecycle

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// The Kandji guide (docs/guides/kandji-otlp-forward-mdm.mdx) audits the forward by grepping
// `beacon endpoint status --json` for this text, so its shape is pinned here.
func TestDestinationStatusReportsOTLPForwardWithoutHeaderValues(t *testing.T) {
	cfg := endpointconfig.Config{Destinations: &endpointconfig.Destinations{OTLPForward: &endpointconfig.OTLPForward{
		Enabled:  true,
		Endpoint: "otel.backend.example:4317",
		Protocol: endpointconfig.OTLPForwardProtocolGRPC,
		Headers:  map[string]string{"authorization": "Bearer secret"},
	}}}
	endpointconfig.NormalizeDestinations(&cfg)
	data, err := json.Marshal(destinationStatus(cfg))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, `"otlp_forward":{"configured":true,"endpoint":"otel.backend.example:4317","protocol":"grpc","harnesses":["claude_code","codex_cli","codex_desktop"],"headers":["authorization"]}`) {
		t.Fatalf("destination status = %s", got)
	}
	if strings.Contains(got, "secret") {
		t.Fatalf("destination status leaks a header value: %s", got)
	}
}

// A package upgrade re-runs install and repair re-runs it after a teardown; both rebuild
// config.json from defaults. The OTLP forward is what keeps another backend fed, so it is
// carried over, and only --no-otlp-forward (an empty forward) removes it.
func TestBuildConfigKeepsOTLPForwardUntilRemoved(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	writeInventoryConfig(t, true, `{"user_mode":true,"log_path":"/tmp/runtime.jsonl","destinations":{"otlp_forward":{"enabled":true,"endpoint":"otel.backend.example:4317","protocol":"grpc","header_names":["authorization"]}}}`)
	if err := os.WriteFile(endpointconfig.OTLPForwardSecretsPath(true), []byte(`{"headers":{"authorization":"Bearer secret"}}`), 0600); err != nil {
		t.Fatal(err)
	}

	cfg := buildConfig(InstallOptions{UserMode: true})
	if cfg.Destinations == nil || cfg.Destinations.OTLPForward == nil || cfg.Destinations.OTLPForward.Endpoint != "otel.backend.example:4317" || cfg.Destinations.OTLPForward.Headers["authorization"] != "Bearer secret" {
		t.Fatalf("buildConfig dropped the OTLP forward: %+v", cfg.Destinations)
	}

	cfg = buildConfig(InstallOptions{UserMode: true, OTLPForward: &endpointconfig.OTLPForward{}})
	if cfg.Destinations != nil && cfg.Destinations.OTLPForward != nil {
		t.Fatalf("an empty OTLPForward should remove it: %+v", cfg.Destinations.OTLPForward)
	}

	cfg = buildConfig(InstallOptions{UserMode: true, OTLPForward: &endpointconfig.OTLPForward{Endpoint: "https://other.example", Protocol: "http/protobuf"}})
	if forward := cfg.Destinations.OTLPForward; forward == nil || forward.Endpoint != "https://other.example" || !forward.Enabled {
		t.Fatalf("a new OTLPForward should replace it: %+v", forward)
	}
}
