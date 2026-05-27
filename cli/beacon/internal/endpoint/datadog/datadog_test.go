package datadog

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
		t.Fatalf("snippet did not include configured path: %s", got)
	}
	if strings.Contains(got, "{{LOG_PATH}}") {
		t.Fatalf("snippet still contains template token: %s", got)
	}
	for _, want := range []string{
		"logs:",
		"type: file",
		"service: beacon-endpoint-agent",
		"source: beacon",
		"vendor:beacon",
		"product:endpoint-agent",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("snippet missing %q: %s", want, got)
		}
	}
}

func TestInstallPackWritesExpectedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := InstallPack(dir, "/tmp/beacon/runtime.jsonl"); err != nil {
		t.Fatalf("InstallPack returned error: %v", err)
	}
	for _, name := range []string{"README.md", "conf.yaml", "sample-event.jsonl"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("expected %s: %v", name, err)
		}
	}
	config, err := os.ReadFile(filepath.Join(dir, "conf.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(config), "/tmp/beacon/runtime.jsonl") {
		t.Fatalf("generated conf.yaml missing configured log path: %s", config)
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
		if destination, ok := doc["destination"].(map[string]interface{}); ok && destination["type"] == "datadog" {
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

func TestPackREADMEMentionsValidationRetentionAndMacOSPermissions(t *testing.T) {
	readme := mustRead("pack/README.md")
	for _, want := range []string{
		"beacon endpoint datadog validate",
		"service:beacon-endpoint-agent",
		"Beacon endpoint datadog validation event",
		"content retention",
		"_dd-agent",
		"/var/log/beacon-agent/runtime.jsonl",
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
		"README.md", "conf.yaml", "sample-event.jsonl",
	} {
		if !names[required] {
			t.Errorf("Files() missing required file %q", required)
		}
	}
}

func TestFilesFromFS_PropagatesReadError(t *testing.T) {
	emptyFS := fstest.MapFS{}
	_, err := filesFromFS(emptyFS, DefaultLogPath)
	if err == nil {
		t.Fatal("filesFromFS with empty FS should return an error")
	}
	if !strings.Contains(err.Error(), "datadog pack asset") {
		t.Fatalf("error should identify the pack source, got: %v", err)
	}
}

func TestConfigSnippetFromFS_ErrorOnMissingAsset(t *testing.T) {
	emptyFS := fstest.MapFS{}
	_, err := configSnippetFromFS(emptyFS, "/some/path.jsonl")
	if err == nil {
		t.Fatal("configSnippetFromFS with empty FS should return error")
	}
	if !strings.Contains(err.Error(), "datadog pack asset") {
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
