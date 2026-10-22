package falcon

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

func TestInstallPackWritesExpectedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := InstallPack(dir, "/tmp/beacon/runtime.jsonl"); err != nil {
		t.Fatalf("InstallPack returned error: %v", err)
	}
	for _, name := range []string{"README.md", "falcon-hec-smoke-test.sh", "sample-event.jsonl", "vector.toml"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("expected %s: %v", name, err)
		}
	}
	vectorConfig, err := os.ReadFile(filepath.Join(dir, "vector.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(vectorConfig), "/tmp/beacon/runtime.jsonl") {
		t.Fatalf("generated vector config missing configured log path: %s", vectorConfig)
	}
	info, err := os.Stat(filepath.Join(dir, "falcon-hec-smoke-test.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if testenv.HasPOSIXFileModes() && info.Mode().Perm() != 0755 {
		t.Fatalf("generated smoke-test script should be 0755, mode=%s", info.Mode().Perm())
	}
}

func TestVectorConfigWrapsBeaconEventsForFalconHEC(t *testing.T) {
	got := mustRead("pack/vector.toml.tmpl")
	for _, want := range []string{
		`include = ["{{LOG_PATH}}"]`,
		`read_from = "${BEACON_VECTOR_READ_FROM:-end}"`,
		`event = parse_json!(.message)`,
		`event."@timestamp" = format_timestamp!(ts, format: "%+")`,
		`"time": to_unix_timestamp(ts)`,
		`"event": event`,
		`"source": get_env_var("BEACON_FALCON_SOURCE") ?? "beacon-endpoint-agent"`,
		`"sourcetype": get_env_var("BEACON_FALCON_SOURCETYPE") ?? "json"`,
		`payload.index = index`,
		`uri = "${BEACON_FALCON_HEC_ENDPOINT}"`,
		`method = "newline_delimited"`,
		`max_events = 500`,
		`retry_max_duration_secs = 300`,
		`type = "file"`,
		`type = "remap"`,
		`type = "http"`,
		`Authorization = "Bearer ${BEACON_FALCON_HEC_TOKEN}"`,
		`Content-Type = "text/plain; charset=utf-8"`,
		`data_dir = "/Library/Application Support/Beacon/Forwarders/vector-data/falcon"`,
		`retry_attempts = 10`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("vector config missing %q: %s", want, got)
		}
	}
}

func TestSmokeTestAvoidsPrintingTokenAndUsesHECHeaders(t *testing.T) {
	got, err := SmokeTest("/tmp/runtime.jsonl")
	if err != nil {
		t.Fatalf("SmokeTest returned error: %v", err)
	}
	for _, want := range []string{
		`BEACON_FALCON_HEC_ENDPOINT is required`,
		`BEACON_FALCON_HEC_TOKEN is required`,
		`Authorization: Bearer $BEACON_FALCON_HEC_TOKEN`,
		`Content-Type: text/plain; charset=utf-8`,
		`Falcon HEC status:`,
		`/tmp/runtime.jsonl`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("smoke test missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "echo $BEACON_FALCON_HEC_TOKEN") {
		t.Fatalf("smoke test should not print token: %s", got)
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
		if destination, ok := doc["destination"].(map[string]interface{}); ok && destination["type"] == "falcon" {
			sawValidation = true
		}
		if harness, ok := doc["harness"].(map[string]interface{}); ok && harness["name"] == "claude" {
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
		t.Fatalf("sample events should include validation, hook, and OTel shapes; validation=%t hook=%t otel=%t", sawValidation, sawHook, sawOTel)
	}
}
