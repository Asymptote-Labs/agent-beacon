package cloudwatch

import (
	"embed"
	"fmt"
	"io/fs"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/siempack"
)

//go:embed pack/*
var packFS embed.FS

const (
	DefaultLogPath   = "/var/log/beacon-agent/runtime.jsonl"
	DefaultOutputDir = "beacon-cloudwatch-pack"
)

type File struct {
	Name            string
	Content         string
	TemplateLogPath bool
	JSONEscape      bool
}

func readAsset(path string) (string, error) {
	data, err := siempack.ReadFile(packFS, path)
	if err != nil {
		return "", fmt.Errorf("cloudwatch pack asset %q: %w", path, err)
	}
	return data, nil
}

func mustRead(path string) string {
	s, err := readAsset(path)
	if err != nil {
		panic(err.Error())
	}
	return s
}

func configSnippetFromFS(fsys fs.FS, logPath string) (string, error) {
	if logPath == "" {
		logPath = DefaultLogPath
	}
	content, err := siempack.ReadFile(fsys, "pack/vector.toml.tmpl")
	if err != nil {
		return "", fmt.Errorf("cloudwatch pack asset %q: %w", "pack/vector.toml.tmpl", err)
	}
	return siempack.RenderLogPath(content, logPath), nil
}

func filesFromFS(fsys fs.FS) ([]File, error) {
	readme, err := siempack.ReadFile(fsys, "pack/README.md")
	if err != nil {
		return nil, fmt.Errorf("cloudwatch pack asset %q: %w", "pack/README.md", err)
	}
	sample, err := siempack.ReadFile(fsys, "pack/sample-event.jsonl")
	if err != nil {
		return nil, fmt.Errorf("cloudwatch pack asset %q: %w", "pack/sample-event.jsonl", err)
	}
	vector, err := siempack.ReadFile(fsys, "pack/vector.toml.tmpl")
	if err != nil {
		return nil, fmt.Errorf("cloudwatch pack asset %q: %w", "pack/vector.toml.tmpl", err)
	}
	return []File{
		{Name: "README.md", Content: readme},
		{Name: "sample-event.jsonl", Content: sample},
		{Name: "vector.toml", Content: vector, TemplateLogPath: true},
	}, nil
}

// ConfigSnippet returns a rendered Vector configuration for AWS CloudWatch Logs.
func ConfigSnippet(logPath string) (string, error) {
	return configSnippetFromFS(packFS, logPath)
}

// Files returns all pack files, propagating any embedded asset read error.
func Files() ([]File, error) {
	return filesFromFS(packFS)
}

// InstallPack writes the AWS CloudWatch Logs pack files to outputDir with logPath substituted.
func InstallPack(outputDir, logPath string) error {
	if outputDir == "" {
		outputDir = DefaultOutputDir
	}
	if logPath == "" {
		logPath = DefaultLogPath
	}
	files, err := filesFromFS(packFS)
	if err != nil {
		return err
	}
	sfiles := make([]siempack.File, 0, len(files))
	for _, file := range files {
		sfiles = append(sfiles, siempack.File(file))
	}
	return siempack.Install(outputDir, sfiles, logPath)
}
