package rapid7

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUploadSmokeTestUsesConfiguredPath(t *testing.T) {
	got := UploadSmokeTest("/tmp/beacon/runtime.jsonl")
	if !strings.Contains(got, "/tmp/beacon/runtime.jsonl") {
		t.Fatalf("script did not include configured path: %s", got)
	}
	if strings.Contains(got, "{{LOG_PATH}}") {
		t.Fatalf("script still contains template token: %s", got)
	}
	for _, want := range []string{
		"RAPID7_WEBHOOK_URL",
		"BEACON_LOG",
		"Content-Type: application/x-ndjson",
		"curl -X POST",
		"--data-binary @\"$BEACON_LOG\"",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("script missing %q: %s", want, got)
		}
	}
}

func TestInstallPackWritesExpectedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := InstallPack(dir, "/tmp/beacon/runtime.jsonl"); err != nil {
		t.Fatalf("InstallPack returned error: %v", err)
	}
	for _, name := range []string{"README.md", "rapid7-upload-smoke-test.sh", "sample-event.jsonl"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("expected %s: %v", name, err)
		}
	}
	scriptPath := filepath.Join(dir, "rapid7-upload-smoke-test.sh")
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "/tmp/beacon/runtime.jsonl") {
		t.Fatalf("generated script missing configured log path: %s", script)
	}
	info, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0111 == 0 {
		t.Fatalf("generated script should be executable, mode=%s", info.Mode())
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
		if destination, ok := doc["destination"].(map[string]interface{}); ok && destination["type"] == "rapid7" {
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

func TestPackREADMEMentionsRapid7SetupAndProductionForwarding(t *testing.T) {
	readme := mustRead("pack/README.md")
	for _, want := range []string{
		"beacon endpoint rapid7 validate",
		"Rapid7 InsightIDR Custom Logs",
		"Webhook",
		"JSON Events Key",
		"RAPID7_WEBHOOK_URL",
		"application/x-ndjson",
		"Beacon endpoint Rapid7 validation event",
		"vendor=beacon product=endpoint-agent destination.type=rapid7",
		"content retention",
		"/var/log/beacon-agent/runtime.jsonl",
	} {
		if !strings.Contains(readme, want) {
			t.Fatalf("pack README missing %q", want)
		}
	}
}
