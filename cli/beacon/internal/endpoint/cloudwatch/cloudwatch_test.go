package cloudwatch

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestConfigSnippetUsesConfiguredPath(t *testing.T) {
	got, err := ConfigSnippet("/tmp/beacon/runtime.jsonl")
	if err != nil {
		t.Fatalf("ConfigSnippet returned unexpected error: %v", err)
	}
	if !strings.Contains(got, "/tmp/beacon/runtime.jsonl") {
		t.Fatalf("config did not include configured path: %s", got)
	}
	if strings.Contains(got, "{{LOG_PATH}}") {
		t.Fatalf("config still contains template token: %s", got)
	}
	for _, want := range []string{
		"aws_cloudwatch_logs",
		"BEACON_CLOUDWATCH_LOG_GROUP",
		"BEACON_CLOUDWATCH_LOG_STREAM_PREFIX",
		"AWS_REGION",
		"credential provider chain",
		"create_missing_stream = true",
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
	for _, name := range []string{"README.md", "sample-event.jsonl", "vector.toml"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("expected %s: %v", name, err)
		}
	}
	vectorPath := filepath.Join(dir, "vector.toml")
	vectorConfig, err := os.ReadFile(vectorPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(vectorConfig), "/tmp/beacon/runtime.jsonl") {
		t.Fatalf("generated vector config missing configured log path: %s", vectorConfig)
	}
	info, err := os.Stat(vectorPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0644 {
		t.Fatalf("generated vector config should be 0644, mode=%s", info.Mode().Perm())
	}
}

func TestVectorConfigUsesAWSCloudWatchLogsSinkAndPreservesJSONShape(t *testing.T) {
	got := mustRead("pack/vector.toml.tmpl")
	for _, want := range []string{
		`include = ["{{LOG_PATH}}"]`,
		`read_from = "end"`,
		`. = parse_json!(.message)`,
		`type = "aws_cloudwatch_logs"`,
		`group_name = "${BEACON_CLOUDWATCH_LOG_GROUP}"`,
		`stream_name = "${BEACON_CLOUDWATCH_LOG_STREAM_PREFIX:-beacon-runtime}/${HOSTNAME:-unknown-host}"`,
		`region = "${AWS_REGION:-us-east-1}"`,
		`create_missing_group = false`,
		`create_missing_stream = true`,
		`codec = "json"`,
		`max_events = 1000`,
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
		if destination, ok := doc["destination"].(map[string]interface{}); ok && destination["type"] == "cloudwatch" {
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

func TestPackREADMEMentionsAWSCloudWatchLogsSetupAndProductionForwarding(t *testing.T) {
	readme := mustRead("pack/README.md")
	for _, want := range []string{
		"beacon endpoint cloudwatch validate",
		"AWS CloudWatch Logs",
		"credentials available",
		"least-privilege IAM policy",
		"BEACON_CLOUDWATCH_LOG_GROUP",
		"BEACON_CLOUDWATCH_LOG_STREAM_PREFIX",
		"aws logs filter-log-events",
		"CloudWatch Logs Insights",
		"Beacon endpoint AWS CloudWatch Logs validation event",
		"vendor=beacon product=endpoint-agent destination.type=cloudwatch destination.mode=aws_cloudwatch_logs",
		"vector.toml",
		"customer-managed host-agent",
		"content retention",
		"/var/log/beacon-agent/runtime.jsonl",
		"Do not store AWS destination secrets",
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
		"README.md", "sample-event.jsonl", "vector.toml",
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
	if !strings.Contains(err.Error(), "cloudwatch pack asset") {
		t.Fatalf("error should identify the pack source, got: %v", err)
	}
}

func TestConfigSnippetFromFS_ErrorOnMissingAsset(t *testing.T) {
	emptyFS := fstest.MapFS{}
	_, err := configSnippetFromFS(emptyFS, "/some/path.jsonl")
	if err == nil {
		t.Fatal("configSnippetFromFS with empty FS should return error")
	}
	if !strings.Contains(err.Error(), "cloudwatch pack asset") {
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

func TestConfigSnippet_DefaultLogPath(t *testing.T) {
	got, err := ConfigSnippet("")
	if err != nil {
		t.Fatalf("ConfigSnippet with empty path returned error: %v", err)
	}
	if !strings.Contains(got, DefaultLogPath) {
		t.Fatalf("ConfigSnippet with empty logPath should use DefaultLogPath %q, got: %s", DefaultLogPath, got)
	}
}
