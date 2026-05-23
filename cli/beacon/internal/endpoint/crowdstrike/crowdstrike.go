package crowdstrike

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
	DefaultOutputDir = "beacon-crowdstrike-pack"
)

type File struct {
	Name    string
	Content string
}

func Files() []File {
	return []File{
		{Name: "README.md", Content: mustRead("pack/README.md")},
		{Name: "otel-collector-config.yaml", Content: CollectorConfig(DefaultLogPath)},
		{Name: "docker-compose.yml", Content: mustRead("pack/docker-compose.yml")},
		{Name: "sample-event.jsonl", Content: mustRead("pack/sample-event.jsonl")},
	}
}

func CollectorConfig(logPath string) string {
	if logPath == "" {
		logPath = DefaultLogPath
	}
	return strings.ReplaceAll(mustRead("pack/otel-collector-config.yaml.tmpl"), "{{LOG_PATH}}", logPath)
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
		if file.Name == "otel-collector-config.yaml" {
			content = CollectorConfig(logPath)
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
		panic(fmt.Sprintf("crowdstrike pack asset %s: %v", path, err))
	}
	return string(data)
}
