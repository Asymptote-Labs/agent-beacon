package rapid7

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed pack/*
var packFS embed.FS

const (
	DefaultLogPath   = "/var/log/beacon-agent/runtime.jsonl"
	DefaultOutputDir = "beacon-rapid7-pack"
)

type File struct {
	Name    string
	Content string
}

func Files() []File {
	return []File{
		{Name: "README.md", Content: mustRead("pack/README.md")},
		{Name: "rapid7-upload-smoke-test.sh", Content: UploadSmokeTest(DefaultLogPath)},
		{Name: "sample-event.jsonl", Content: mustRead("pack/sample-event.jsonl")},
	}
}

func UploadSmokeTest(logPath string) string {
	if logPath == "" {
		logPath = DefaultLogPath
	}
	return strings.ReplaceAll(mustRead("pack/rapid7-upload-smoke-test.sh.tmpl"), "{{LOG_PATH}}", logPath)
}

func InstallPack(outputDir, logPath string) error {
	if outputDir == "" {
		outputDir = DefaultOutputDir
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return err
	}
	for _, file := range Files() {
		content := file.Content
		if file.Name == "rapid7-upload-smoke-test.sh" {
			content = UploadSmokeTest(logPath)
		}
		if err := os.WriteFile(filepath.Join(outputDir, file.Name), []byte(content), fileMode(file.Name)); err != nil {
			return err
		}
	}
	return nil
}

func fileMode(name string) os.FileMode {
	if strings.HasSuffix(name, ".sh") {
		return 0755
	}
	return 0644
}

func mustRead(path string) string {
	data, err := packFS.ReadFile(path)
	if err != nil {
		panic(fmt.Sprintf("rapid7 pack asset %s: %v", path, err))
	}
	return string(data)
}
