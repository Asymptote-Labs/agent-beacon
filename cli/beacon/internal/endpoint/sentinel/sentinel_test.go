package sentinel

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDCRTransformMentionsExpectedColumns(t *testing.T) {
	got := DCRTransform()
	for _, want := range []string{
		"RawEvent = todynamic(RawData)",
		"TimeGenerated = coalesce(todatetime(RawEvent.timestamp), TimeGenerated)",
		"EventAction = tostring(RawEvent.event.action)",
		"CommandLine = coalesce(tostring(RawEvent.command.command), tostring(RawEvent.tool.command))",
		"RawData = RawEvent",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("DCR transform missing %q: %s", want, got)
		}
	}
}

func TestInstallPackWritesExpectedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := InstallPack(dir, "/tmp/beacon/runtime.jsonl"); err != nil {
		t.Fatalf("InstallPack returned error: %v", err)
	}
	for _, name := range []string{
		"README.md",
		"dcr-transform.kql",
		"table-schema.json",
		"dcr-template.json",
		"queries.kql",
		"detections.kql",
		"sample-event.jsonl",
	} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("expected %s: %v", name, err)
		}
	}
	dcrTemplate, err := os.ReadFile(filepath.Join(dir, "dcr-template.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dcrTemplate), "/tmp/beacon/runtime.jsonl") {
		t.Fatalf("generated DCR template missing configured log path: %s", dcrTemplate)
	}
	if strings.Contains(string(dcrTemplate), "{{LOG_PATH}}") {
		t.Fatalf("generated DCR template still contains template token: %s", dcrTemplate)
	}
}

func TestPackJSONFilesAreValid(t *testing.T) {
	for _, path := range []string{
		"pack/table-schema.json",
		"pack/dcr-template.json",
	} {
		var doc map[string]interface{}
		if err := json.Unmarshal([]byte(mustRead(path)), &doc); err != nil {
			t.Fatalf("%s is not valid JSON: %v", path, err)
		}
	}
}

func TestRenderedDCRTemplateContainsTransformFromKQL(t *testing.T) {
	rendered := renderDCRTemplate()
	if strings.Contains(rendered, "{{DCR_TRANSFORM}}") {
		t.Fatal("rendered DCR template still contains {{DCR_TRANSFORM}} placeholder")
	}
	var doc map[string]interface{}
	if err := json.Unmarshal([]byte(rendered), &doc); err != nil {
		t.Fatalf("rendered DCR template is not valid JSON: %v", err)
	}
	transform := minifyKQL(DCRTransform())
	if !strings.Contains(rendered, transform) {
		t.Fatalf("rendered DCR template does not contain the minified dcr-transform.kql content")
	}
}

func TestSampleEventsCoverValidationHookAndOTelShapes(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader(mustRead("pack/sample-event.jsonl")))
	var sawValidation, sawHook, sawOTel bool
	for scanner.Scan() {
		var doc map[string]interface{}
		if err := json.Unmarshal(scanner.Bytes(), &doc); err != nil {
			t.Fatalf("sample-event.jsonl is not valid JSONL: %v", err)
		}
		if destination, ok := doc["destination"].(map[string]interface{}); ok && destination["type"] == "sentinel" {
			sawValidation = true
		}
		if _, ok := doc["tool"].(map[string]interface{}); ok {
			sawHook = true
		}
		if raw, ok := doc["raw"].(map[string]interface{}); ok && raw["otel_signal"] != nil {
			sawOTel = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !sawValidation || !sawHook || !sawOTel {
		t.Fatalf("sample events should include validation, hook-rich, and OTel shapes; validation=%t hook=%t otel=%t", sawValidation, sawHook, sawOTel)
	}
}

func TestPackREADMEMentionsSentinelSetupAndSecretBoundaries(t *testing.T) {
	readme := mustRead("pack/README.md")
	for _, want := range []string{
		"beacon endpoint sentinel validate",
		"Azure Monitor Agent",
		"Data Collection Rule",
		"BeaconRuntime_CL",
		"Microsoft Sentinel",
		"content retention",
		"/var/log/beacon-agent/runtime.jsonl",
		"not in Beacon endpoint configuration",
		"Direct Logs Ingestion API",
		"CEF and Syslog",
	} {
		if !strings.Contains(readme, want) {
			t.Fatalf("pack README missing %q", want)
		}
	}
}

func TestKQLAssetsMentionSentinelTableAndValidation(t *testing.T) {
	for _, path := range []string{
		"pack/queries.kql",
		"pack/detections.kql",
		"pack/dcr-transform.kql",
	} {
		got := mustRead(path)
		if !strings.Contains(got, "BeaconRuntime_CL") && path != "pack/dcr-transform.kql" {
			t.Fatalf("%s should mention BeaconRuntime_CL", path)
		}
		if strings.Contains(got, "{{LOG_PATH}}") {
			t.Fatalf("%s still contains template token", path)
		}
	}
	if !strings.Contains(mustRead("pack/queries.kql"), "Beacon endpoint Sentinel validation event") {
		t.Fatal("queries.kql should include the Sentinel validation phrase")
	}
}
