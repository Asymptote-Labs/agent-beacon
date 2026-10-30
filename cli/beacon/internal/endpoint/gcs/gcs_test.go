package gcs

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

func TestUploadSmokeTestUsesConfiguredPath(t *testing.T) {
	got, err := UploadSmokeTest("/tmp/beacon/runtime.jsonl")
	if err != nil {
		t.Fatalf("UploadSmokeTest returned unexpected error: %v", err)
	}
	if !strings.Contains(got, "/tmp/beacon/runtime.jsonl") {
		t.Fatalf("script did not include configured path: %s", got)
	}
	// Derived with the host's separator, so the expectation is too: the assertion is that the
	// inventory stream sits beside the configured log, not what a separator looks like.
	if want := filepath.Join("/tmp/beacon", "inventory_state.jsonl"); !strings.Contains(got, want) {
		t.Fatalf("script did not include derived inventory path %q: %s", want, got)
	}
	if strings.Contains(got, "{{LOG_PATH}}") {
		t.Fatalf("script still contains template token: %s", got)
	}
	for _, want := range []string{
		"BEACON_GCS_BUCKET",
		"BEACON_GCS_PREFIX",
		"gcloud storage cp",
		"gsutil",
		"--content-type=\"application/x-ndjson\"",
		`prefix="${prefix%/runtime}"`,
		`prefix="${prefix%/inventory}"`,
		`/runtime/smoke-tests/beacon-runtime-`,
		`/inventory/smoke-tests/beacon-inventory-`,
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
	for _, name := range []string{"README.md", "gcs-upload-smoke-test.sh", "sample-event.jsonl", "vector.toml"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("expected %s: %v", name, err)
		}
	}
	scriptPath := filepath.Join(dir, "gcs-upload-smoke-test.sh")
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
	if testenv.HasPOSIXFileModes() && info.Mode().Perm()&0111 == 0 {
		t.Fatalf("generated script should be executable, mode=%s", info.Mode())
	}
	vectorPath := filepath.Join(dir, "vector.toml")
	vectorConfig, err := os.ReadFile(vectorPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(vectorConfig), "/tmp/beacon/runtime.jsonl") {
		t.Fatalf("generated vector config missing configured log path: %s", vectorConfig)
	}
	if !strings.Contains(string(vectorConfig), "/tmp/beacon/inventory_state.jsonl") {
		t.Fatalf("generated vector config missing derived inventory log path: %s", vectorConfig)
	}
	info, err = os.Stat(vectorPath)
	if err != nil {
		t.Fatal(err)
	}
	if testenv.HasPOSIXFileModes() && info.Mode().Perm() != 0644 {
		t.Fatalf("generated vector config should be 0644, mode=%s", info.Mode().Perm())
	}
}

func TestVectorConfigUsesGCSCloudStorageSinkAndPreservesJSONShape(t *testing.T) {
	got := mustRead("pack/vector.toml.tmpl")
	for _, want := range []string{
		`include = ["{{LOG_PATH}}"]`,
		`include = ["{{INVENTORY_LOG_PATH}}"]`,
		`read_from = "end"`,
		`read_from = "beginning"`,
		`. = parse_json!(.message)`,
		`type = "gcp_cloud_storage"`,
		`bucket = "${BEACON_GCS_BUCKET}"`,
		`key_prefix = "${BEACON_GCS_PREFIX:-beacon}/runtime/date=%F/"`,
		`key_prefix = "${BEACON_GCS_PREFIX:-beacon}/inventory/date=%F/"`,
		`filename_time_format = "%s"`,
		`filename_append_uuid = true`,
		`filename_extension = "jsonl"`,
		`content_type = "application/x-ndjson"`,
		`storage_class = "${BEACON_GCS_STORAGE_CLASS:-STANDARD}"`,
		`codec = "json"`,
		`method = "newline_delimited"`,
		`max_bytes = 10000000`,
		`retry_attempts = 10`,
		`[sinks.beacon_runtime_gcs.healthcheck]`,
		`[sinks.beacon_inventory_gcs.healthcheck]`,
		`enabled = false`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("vector config missing %q: %s", want, got)
		}
	}
	for _, forbidden := range []string{
		`compression = "gzip"`,
		`content_encoding`,
		`.input_tokens =`,
		`.output_tokens =`,
		`.cache_read_input_tokens =`,
		`.cache_creation_input_tokens =`,
		`.reasoning_output_tokens =`,
		`.cost_usd =`,
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("vector config must preserve canonical gen_ai.usage without parallel field %q", forbidden)
		}
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
		if destination, ok := doc["destination"].(map[string]interface{}); ok && destination["type"] == "gcs" {
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
