package elastic

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInputSnippetUsesConfiguredPath(t *testing.T) {
	got := InputSnippet("/tmp/beacon/runtime.jsonl")
	if !strings.Contains(got, "/tmp/beacon/runtime.jsonl") {
		t.Fatalf("snippet did not include configured path: %s", got)
	}
	if strings.Contains(got, "{{LOG_PATH}}") {
		t.Fatalf("snippet still contains template token: %s", got)
	}
	if !strings.Contains(got, "filestream") {
		t.Fatalf("snippet should use Filebeat filestream input: %s", got)
	}
}

func TestInstallPackWritesExpectedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := InstallPack(dir, "/tmp/beacon/runtime.jsonl"); err != nil {
		t.Fatalf("InstallPack returned error: %v", err)
	}
	for _, name := range []string{
		"README.md",
		"filebeat.yml",
		"elastic-agent-standalone.yml",
		"ilm-policy.json",
		"component-template-mappings.json",
		"component-template-settings.json",
		"index-template.json",
		"ingest-pipeline.json",
		"kibana-assets.ndjson",
		"docker-compose.yml",
		"sample-event.jsonl",
	} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("expected %s: %v", name, err)
		}
	}
	filebeat, err := os.ReadFile(filepath.Join(dir, "filebeat.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(filebeat), "/tmp/beacon/runtime.jsonl") {
		t.Fatalf("generated filebeat.yml missing configured log path: %s", filebeat)
	}
	for _, emptyCredential := range []string{`api_key: "${ES_API_KEY:}"`, `username: "${ES_USERNAME:}"`, `password: "${ES_PASSWORD:}"`} {
		if strings.Contains(string(filebeat), emptyCredential) {
			t.Fatalf("generated filebeat.yml should not emit empty credential defaults: %s", emptyCredential)
		}
	}
	compose, err := os.ReadFile(filepath.Join(dir, "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(compose), "${BEACON_LOG_DIR}:${BEACON_LOG_DIR}:ro") {
		t.Fatalf("docker-compose.yml should mount the log directory at the same path: %s", compose)
	}
}

func TestPackJSONFilesAreValid(t *testing.T) {
	for _, path := range []string{
		"pack/ilm-policy.json",
		"pack/component-template-mappings.json",
		"pack/component-template-settings.json",
		"pack/index-template.json",
		"pack/ingest-pipeline.json",
	} {
		var doc map[string]interface{}
		if err := json.Unmarshal([]byte(mustRead(path)), &doc); err != nil {
			t.Fatalf("%s is not valid JSON: %v", path, err)
		}
	}
}

func TestKibanaAssetsAreParseableNDJSON(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader(mustRead("pack/kibana-assets.ndjson")))
	line := 0
	var foundDataView bool
	for scanner.Scan() {
		line++
		var doc map[string]interface{}
		if err := json.Unmarshal(scanner.Bytes(), &doc); err != nil {
			t.Fatalf("kibana-assets.ndjson line %d is not valid JSON: %v", line, err)
		}
		if doc["type"] == "" || doc["id"] == "" {
			t.Fatalf("kibana-assets.ndjson line %d missing type/id: %#v", line, doc)
		}
		if doc["type"] == "index-pattern" {
			attrs, ok := doc["attributes"].(map[string]interface{})
			if !ok {
				t.Fatalf("kibana data view missing attributes: %#v", doc)
			}
			if attrs["name"] == "Beacon Endpoint Events" {
				foundDataView = true
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if line == 0 {
		t.Fatal("kibana-assets.ndjson was empty")
	}
	if !foundDataView {
		t.Fatal("kibana-assets.ndjson should include the Beacon Endpoint Events data view")
	}
}

func TestPackREADMEUsesIngestedFieldNames(t *testing.T) {
	readme := mustRead("pack/README.md")
	for _, want := range []string{
		"beacon.product:endpoint-agent",
		"beacon.prompt.text",
		"beacon.harness.name",
	} {
		if !strings.Contains(readme, want) {
			t.Fatalf("pack README missing ingested field query %q", want)
		}
	}
	if strings.Contains(readme, "product:endpoint-agent") && !strings.Contains(readme, "beacon.product:endpoint-agent") {
		t.Fatalf("pack README should not use raw pre-ingest product field query")
	}
}

func TestSampleEventsCoverHookAndOTelShapes(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader(mustRead("pack/sample-event.jsonl")))
	var sawHook, sawOTel bool
	for scanner.Scan() {
		var doc map[string]interface{}
		if err := json.Unmarshal(scanner.Bytes(), &doc); err != nil {
			t.Fatalf("sample-event.jsonl is not valid JSONL: %v", err)
		}
		if raw, ok := doc["raw"].(map[string]interface{}); ok && raw["otel_signal"] != nil {
			sawOTel = true
		}
		if _, ok := doc["tool"].(map[string]interface{}); ok {
			sawHook = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !sawHook || !sawOTel {
		t.Fatalf("sample events should include hook-rich and OTel shapes, sawHook=%t sawOTel=%t", sawHook, sawOTel)
	}
}

func TestIngestPipelineMentionsKeyMappings(t *testing.T) {
	pipeline := mustRead("pack/ingest-pipeline.json")
	for _, want := range []string{"event.duration", "'otel'", "host.hostname", "process.command_line", "gen_ai"} {
		if !strings.Contains(pipeline, want) {
			t.Fatalf("ingest pipeline missing %s", want)
		}
	}
	if strings.Contains(pipeline, "event.action = 'file.' + ctx.file.operation") {
		t.Fatal("ingest pipeline should preserve Beacon file actions instead of deriving lossy file operation actions")
	}
}
