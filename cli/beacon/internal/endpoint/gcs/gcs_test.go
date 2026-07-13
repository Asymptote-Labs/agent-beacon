package gcs

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestUploadSmokeTestUsesConfiguredPath(t *testing.T) {
	got, err := UploadSmokeTest("/tmp/beacon/runtime.jsonl")
	if err != nil {
		t.Fatalf("UploadSmokeTest returned unexpected error: %v", err)
	}
	if !strings.Contains(got, "/tmp/beacon/runtime.jsonl") {
		t.Fatalf("script did not include configured path: %s", got)
	}
	if !strings.Contains(got, "/tmp/beacon/inventory_state.jsonl") {
		t.Fatalf("script did not include derived inventory path: %s", got)
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
		"Application Default Credentials",
		"validation only",
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
	if info.Mode().Perm()&0111 == 0 {
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
	if info.Mode().Perm() != 0644 {
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
		`filename_extension = "jsonl.gz"`,
		`compression = "gzip"`,
		`content_encoding = "gzip"`,
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

func TestPackREADMEMentionsGCSSetupAndProductionForwarding(t *testing.T) {
	readme := mustRead("pack/README.md")
	for _, want := range []string{
		"beacon endpoint gcs validate",
		"Google Cloud Storage bucket",
		"Application Default Credentials",
		"roles/storage.objectCreator",
		"BEACON_GCS_BUCKET",
		"BEACON_GCS_PREFIX",
		"gcloud storage ls",
		"gcloud storage cat",
		"inventory_state.jsonl",
		"Beacon endpoint GCS validation event",
		"vendor=beacon product=endpoint-agent destination.type=gcs destination.mode=google_cloud_storage_jsonl",
		"vector.toml",
		"customer-managed host-agent",
		"without a Vector wrapper",
		"Content Handling",
		"/var/log/beacon-agent/runtime.jsonl",
		"/var/log/beacon-agent/inventory_state.jsonl",
		"Beacon does not store Google Cloud credentials",
		"encryption",
	} {
		if !strings.Contains(readme, want) {
			t.Fatalf("pack README missing %q", want)
		}
	}
}

func TestFiles_NoError(t *testing.T) {
	files, err := Files()
	if err != nil {
		t.Fatalf("Files() returned unexpected error: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("Files() returned no files")
	}
}

func TestFiles_ContainsAllRequiredNames(t *testing.T) {
	files, err := Files()
	if err != nil {
		t.Fatal(err)
	}
	names := make(map[string]bool)
	for _, f := range files {
		names[f.Name] = true
	}
	for _, required := range []string{
		"README.md", "gcs-upload-smoke-test.sh", "sample-event.jsonl", "vector.toml",
	} {
		if !names[required] {
			t.Errorf("Files() missing required file %q", required)
		}
	}
}

func TestFilesFromFS_PropagatesReadError(t *testing.T) {
	emptyFS := fstest.MapFS{}
	_, err := filesFromFS(emptyFS)
	if err == nil {
		t.Fatal("filesFromFS with empty FS should return an error")
	}
	if !strings.Contains(err.Error(), "gcs pack asset") {
		t.Fatalf("error should identify the pack source, got: %v", err)
	}
}

func TestUploadSmokeTestFromFS_ErrorOnMissingAsset(t *testing.T) {
	emptyFS := fstest.MapFS{}
	_, err := uploadSmokeTestFromFS(emptyFS, "/some/path.jsonl")
	if err == nil {
		t.Fatal("uploadSmokeTestFromFS with empty FS should return error")
	}
	if !strings.Contains(err.Error(), "gcs pack asset") {
		t.Fatalf("error should identify the pack source, got: %v", err)
	}
}

func TestInstallPack_ErrorOnWriteFailure(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: filesystem permission restrictions do not apply")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0555); err != nil {
		t.Skip("cannot set directory permissions:", err)
	}
	defer os.Chmod(dir, 0755)

	subdir := filepath.Join(dir, "output")
	err := InstallPack(subdir, DefaultLogPath)
	if err == nil {
		t.Fatal("InstallPack into read-only directory should return error")
	}
}

func TestUploadSmokeTest_DefaultLogPath(t *testing.T) {
	got, err := UploadSmokeTest("")
	if err != nil {
		t.Fatalf("UploadSmokeTest with empty path returned error: %v", err)
	}
	if !strings.Contains(got, DefaultLogPath) {
		t.Fatalf("UploadSmokeTest with empty logPath should use DefaultLogPath %q, got: %s", DefaultLogPath, got)
	}
}
