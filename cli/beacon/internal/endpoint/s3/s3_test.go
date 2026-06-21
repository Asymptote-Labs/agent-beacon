package s3

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
		"BEACON_S3_BUCKET",
		"BEACON_S3_PREFIX",
		"AWS_REGION",
		"aws s3 cp",
		"--content-type \"application/x-ndjson\"",
		"standard AWS credential provider chain",
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
	for _, name := range []string{"README.md", "s3-upload-smoke-test.sh", "sample-event.jsonl", "vector.toml"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("expected %s: %v", name, err)
		}
	}
	scriptPath := filepath.Join(dir, "s3-upload-smoke-test.sh")
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

func TestVectorConfigUsesAWSS3SinkAndPreservesJSONShape(t *testing.T) {
	got := mustRead("pack/vector.toml.tmpl")
	for _, want := range []string{
		`include = ["{{LOG_PATH}}"]`,
		`include = ["{{INVENTORY_LOG_PATH}}"]`,
		`read_from = "end"`,
		`read_from = "beginning"`,
		`. = parse_json!(.message)`,
		`value, err = to_int(.gen_ai.usage.input_tokens)`,
		`.input_tokens = value`,
		`value, err = to_float(.gen_ai.usage.cost_usd)`,
		`.cost_usd = value`,
		`type = "aws_s3"`,
		`bucket = "${BEACON_S3_BUCKET}"`,
		`region = "${AWS_REGION:-us-east-1}"`,
		`key_prefix = "${BEACON_S3_PREFIX:-beacon}/runtime/date=%F/"`,
		`key_prefix = "${BEACON_S3_PREFIX:-beacon}/inventory/date=%F/"`,
		`filename_time_format = "%s"`,
		`filename_append_uuid = true`,
		`filename_extension = "jsonl.gz"`,
		`compression = "gzip"`,
		`content_encoding = "gzip"`,
		`content_type = "application/x-ndjson"`,
		`codec = "json"`,
		`method = "newline_delimited"`,
		`max_bytes = 10000000`,
		`retry_attempts = 10`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("vector config missing %q: %s", want, got)
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
		if destination, ok := doc["destination"].(map[string]interface{}); ok && destination["type"] == "s3" {
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

func TestPackREADMEMentionsS3SetupAndProductionForwarding(t *testing.T) {
	readme := mustRead("pack/README.md")
	for _, want := range []string{
		"beacon endpoint s3 validate",
		"AWS S3 bucket",
		"standard AWS credential provider chain",
		"least-privilege IAM policy",
		"BEACON_S3_BUCKET",
		"BEACON_S3_PREFIX",
		"aws s3 ls",
		"aws s3 cp",
		"inventory_state.jsonl",
		"Beacon endpoint S3 validation event",
		"vendor=beacon product=endpoint-agent destination.type=s3 destination.mode=aws_s3_jsonl",
		"vector.toml",
		"customer-managed host-agent",
		"without a Vector wrapper",
		"Content Handling",
		"/var/log/beacon-agent/runtime.jsonl",
		"/var/log/beacon-agent/inventory_state.jsonl",
		"Beacon does not store AWS credentials",
		"server-side encryption",
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
		"README.md", "s3-upload-smoke-test.sh", "sample-event.jsonl", "vector.toml",
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
	if !strings.Contains(err.Error(), "s3 pack asset") {
		t.Fatalf("error should identify the pack source, got: %v", err)
	}
}

func TestUploadSmokeTestFromFS_ErrorOnMissingAsset(t *testing.T) {
	emptyFS := fstest.MapFS{}
	_, err := uploadSmokeTestFromFS(emptyFS, "/some/path.jsonl")
	if err == nil {
		t.Fatal("uploadSmokeTestFromFS with empty FS should return error")
	}
	if !strings.Contains(err.Error(), "s3 pack asset") {
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
