package datadog

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
	DefaultOutputDir = "beacon-datadog-pack"
)

type File struct {
	Name    string
	Content string
}

func Files() []File {
	return []File{
		{Name: "README.md", Content: mustRead("pack/README.md")},
		{Name: "conf.yaml", Content: ConfigSnippet(DefaultLogPath)},
		{Name: "sample-event.jsonl", Content: mustRead("pack/sample-event.jsonl")},
	}
}

func ConfigSnippet(logPath string) string {
	if logPath == "" {
		logPath = DefaultLogPath
	}
	return strings.ReplaceAll(mustRead("pack/conf.yaml.tmpl"), "{{LOG_PATH}}", logPath)
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
		if file.Name == "conf.yaml" {
			content = ConfigSnippet(logPath)
		}
		if err := os.WriteFile(filepath.Join(outputDir, file.Name), []byte(content), 0644); err != nil {
			return err
		}
	}
	return nil
}

func mustRead(path string) string {
	data, err := packFS.ReadFile(path)
	if err != nil {
		panic(fmt.Sprintf("datadog pack asset %s: %v", path, err))
	}
	return string(data)
}
