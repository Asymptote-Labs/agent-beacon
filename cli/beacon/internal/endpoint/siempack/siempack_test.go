package siempack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestRenderLogPath(t *testing.T) {
	got := RenderLogPath("path={{LOG_PATH}}", "/var/log/beacon-agent/runtime.jsonl")
	if got != "path=/var/log/beacon-agent/runtime.jsonl" {
		t.Fatalf("RenderLogPath() = %q", got)
	}
}

func TestReadFile(t *testing.T) {
	got, err := ReadFile(fstest.MapFS{
		"pack/example.txt": {Data: []byte("hello")},
	}, "pack/example.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello" {
		t.Fatalf("ReadFile() = %q", got)
	}
}

func TestInstallWritesFilesWithModesAndLogPath(t *testing.T) {
	dir := t.TempDir()
	err := Install(dir, []File{
		{Name: "README.md", Content: "docs"},
		{Name: "upload.sh", Content: "log={{LOG_PATH}}", TemplateLogPath: true},
		{Name: "vector.toml", Content: "path={{LOG_PATH}}", TemplateLogPath: true},
	}, "/tmp/beacon/runtime.jsonl")
	if err != nil {
		t.Fatal(err)
	}

	scriptPath := filepath.Join(dir, "upload.sh")
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "/tmp/beacon/runtime.jsonl") {
		t.Fatalf("script missing rendered log path: %s", script)
	}
	info, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0755 {
		t.Fatalf("script mode = %s, want 0755", info.Mode().Perm())
	}

	configInfo, err := os.Stat(filepath.Join(dir, "vector.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if configInfo.Mode().Perm() != 0644 {
		t.Fatalf("config mode = %s, want 0644", configInfo.Mode().Perm())
	}
}
