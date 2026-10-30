package collector

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
)

func restrictedConfig(userMode bool) endpointconfig.Config {
	cfg := endpointconfig.Default(userMode, "/var/log/beacon-agent/runtime.jsonl")
	cfg.LogAccess = "restricted"
	return cfg
}

func parsedYAML(t *testing.T, text string) map[string]interface{} {
	t.Helper()
	var doc map[string]interface{}
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatalf("collector config is not YAML: %v\n%s", err, text)
	}
	return doc
}

func TestConfigYAMLServesTheIngestSocketInTheRestrictedMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no restricted mode on Windows")
	}
	doc := parsedYAML(t, ConfigYAML(restrictedConfig(false)))
	extensions := doc["extensions"].(map[string]interface{})
	ingest, ok := extensions[IngestExtension].(map[string]interface{})
	if !ok {
		t.Fatalf("no %s extension: %v", IngestExtension, extensions)
	}
	if ingest["log_path"] != "/var/log/beacon-agent/runtime.jsonl" {
		t.Fatalf("log_path = %v", ingest["log_path"])
	}
	// The exporter and the extension append to one log, so they must rotate it the same way.
	exporter := doc["exporters"].(map[string]interface{})["beaconjson"].(map[string]interface{})
	if ingest["rotate_bytes"] != exporter["rotate_bytes"] || ingest["rotate_archives"] != exporter["rotate_archives"] {
		t.Fatalf("rotation differs: ingest %v/%v, exporter %v/%v", ingest["rotate_bytes"], ingest["rotate_archives"], exporter["rotate_bytes"], exporter["rotate_archives"])
	}
	service := doc["service"].(map[string]interface{})
	enabled := service["extensions"].([]interface{})
	if len(enabled) != 2 || enabled[0] != "health_check" || enabled[1] != IngestExtension {
		t.Fatalf("service extensions = %v", enabled)
	}
}

func TestConfigYAMLLeavesTheIngestSocketOutOtherwise(t *testing.T) {
	for name, cfg := range map[string]endpointconfig.Config{
		"shared":                  endpointconfig.Default(false, "/var/log/beacon-agent/runtime.jsonl"),
		"restricted in user mode": restrictedConfig(true),
	} {
		text := ConfigYAML(cfg)
		if strings.Contains(text, IngestExtension) {
			t.Errorf("%s: config names %s:\n%s", name, IngestExtension, text)
		}
		service := parsedYAML(t, text)["service"].(map[string]interface{})
		if enabled := service["extensions"].([]interface{}); len(enabled) != 1 || enabled[0] != "health_check" {
			t.Errorf("%s: service extensions = %v", name, enabled)
		}
	}
}

func TestCheckComponentsRequiresTheIngestExtensionWhenAsked(t *testing.T) {
	prev := listComponents
	t.Cleanup(func() { listComponents = prev })
	listed := "processors:\n  - name: claude_api_body\n  - name: beacon_privacy\nextensions:\n  - name: health_check\n"
	listComponents = func(context.Context, string) ([]byte, error) { return []byte(listed), nil }
	if err := CheckComponents("/opt/beacon/bin/beacon-otelcol"); err != nil {
		t.Fatalf("shared mode: %v", err)
	}
	err := CheckComponents("/opt/beacon/bin/beacon-otelcol", IngestExtension)
	if err == nil || !strings.Contains(err.Error(), IngestExtension) {
		t.Fatalf("an old collector without %s = %v", IngestExtension, err)
	}
	listed += "  - name: beacon_ingest\n"
	if err := CheckComponents("/opt/beacon/bin/beacon-otelcol", IngestExtension); err != nil {
		t.Fatalf("a collector with %s: %v", IngestExtension, err)
	}
}
