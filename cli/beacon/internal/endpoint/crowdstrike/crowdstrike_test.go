package crowdstrike

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollectorConfigUsesConfiguredPath(t *testing.T) {
	got := CollectorConfig("/tmp/beacon/runtime.jsonl")
	if !strings.Contains(got, "/tmp/beacon/runtime.jsonl") {
		t.Fatalf("config did not include configured path: %s", got)
	}
	if strings.Contains(got, "{{LOG_PATH}}") {
		t.Fatalf("config still contains template token: %s", got)
	}
	for _, want := range []string{
		"filelog/beacon_runtime",
		"transform/aidr_genai",
		"filter/beacon_ai_activity",
		"otlphttp/aidr_logs",
		"${env:CS_AIDR_BASE_URL:-https://api.crowdstrike.com/aidr/aiguard}/v1/otel/logs",
		"Bearer ${env:CS_AIDR_TOKEN}",
		"encoding: json",
		"compression: none",
		"gen_ai.user.message",
		"gen_ai.tool.message",
		"service.name",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("config missing %q: %s", want, got)
		}
	}
}

func TestInstallPackWritesExpectedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := InstallPack(dir, "/tmp/beacon/runtime.jsonl"); err != nil {
		t.Fatalf("InstallPack returned error: %v", err)
	}
	for _, name := range []string{"README.md", "otel-collector-config.yaml", "docker-compose.yml", "sample-event.jsonl"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("expected %s: %v", name, err)
		}
	}
	config, err := os.ReadFile(filepath.Join(dir, "otel-collector-config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(config), "/tmp/beacon/runtime.jsonl") {
		t.Fatalf("generated collector config missing configured log path: %s", config)
	}
}

func TestSampleEventsCoverValidationPromptAndMCPShapes(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader(mustRead("pack/sample-event.jsonl")))
	var sawValidation, sawPrompt, sawMCP bool
	for scanner.Scan() {
		var doc map[string]interface{}
		if err := json.Unmarshal(scanner.Bytes(), &doc); err != nil {
			t.Fatalf("sample-event.jsonl is not valid JSONL: %v", err)
		}
		if destination, ok := doc["destination"].(map[string]interface{}); ok && destination["type"] == "crowdstrike" {
			sawValidation = true
		}
		if prompt, ok := doc["prompt"].(map[string]interface{}); ok && prompt["text"] != "" {
			sawPrompt = true
		}
		if _, ok := doc["mcp"].(map[string]interface{}); ok {
			sawMCP = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !sawValidation || !sawPrompt || !sawMCP {
		t.Fatalf("sample events should include validation, prompt, and MCP shapes; validation=%t prompt=%t mcp=%t", sawValidation, sawPrompt, sawMCP)
	}
}

func TestPackREADMEMentionsAIDRSetupValidationAndRetention(t *testing.T) {
	readme := mustRead("pack/README.md")
	for _, want := range []string{
		"beacon endpoint crowdstrike validate",
		"AIDR for Agents",
		"No Policy, Log Only",
		"CS_AIDR_BASE_URL",
		"CS_AIDR_TOKEN",
		"event_type=\"AIDRPromptDataEvent\"",
		"content retention",
		"Direct Falcon",
		"/var/log/beacon-agent/runtime.jsonl",
	} {
		if !strings.Contains(readme, want) {
			t.Fatalf("pack README missing %q", want)
		}
	}
}
