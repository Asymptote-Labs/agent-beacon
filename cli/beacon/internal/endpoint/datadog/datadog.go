package datadog

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/siempack"
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

// readAsset reads an embedded asset by path and returns its contents or an error.
func readAsset(path string) (string, error) {
	data, err := packFS.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("datadog pack asset %q: %w", path, err)
	}
	return string(data), nil
}

// mustRead returns the embedded asset at path or panics. Retained for test use
// where the embedded FS is always present; all production code uses readAsset.
func mustRead(path string) string {
	s, err := readAsset(path)
	if err != nil {
		panic(err.Error())
	}
	return s
}

// configSnippetFromFS renders the Datadog Agent conf.yaml snippet using the supplied FS.
func configSnippetFromFS(fsys fs.FS, logPath string) (string, error) {
	if logPath == "" {
		logPath = DefaultLogPath
	}
	content, err := siempack.ReadFile(fsys, "pack/conf.yaml.tmpl")
	if err != nil {
		return "", fmt.Errorf("datadog pack asset %q: %w", "pack/conf.yaml.tmpl", err)
	}
	return strings.ReplaceAll(content, "{{LOG_PATH}}", logPath), nil
}

// filesFromFS builds the full file list from the supplied FS, propagating any
// read errors instead of panicking.
func filesFromFS(fsys fs.FS, logPath string) ([]File, error) {
	readme, err := siempack.ReadFile(fsys, "pack/README.md")
	if err != nil {
		return nil, fmt.Errorf("datadog pack asset %q: %w", "pack/README.md", err)
	}
	conf, err := configSnippetFromFS(fsys, logPath)
	if err != nil {
		return nil, err
	}
	sample, err := siempack.ReadFile(fsys, "pack/sample-event.jsonl")
	if err != nil {
		return nil, fmt.Errorf("datadog pack asset %q: %w", "pack/sample-event.jsonl", err)
	}
	return []File{
		{Name: "README.md", Content: readme},
		{Name: "conf.yaml", Content: conf},
		{Name: "sample-event.jsonl", Content: sample},
	}, nil
}

// Files returns all pack files rendered with DefaultLogPath, propagating any
// embedded asset read error.
func Files() ([]File, error) {
	return filesFromFS(packFS, DefaultLogPath)
}

// ConfigSnippet returns the Datadog Agent conf.yaml snippet with logPath substituted.
func ConfigSnippet(logPath string) (string, error) {
	return configSnippetFromFS(packFS, logPath)
}

// InstallPack writes the pack files to outputDir with logPath substituted.
func InstallPack(outputDir, logPath string) error {
	if outputDir == "" {
		outputDir = DefaultOutputDir
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return err
	}
	files, err := filesFromFS(packFS, logPath)
	if err != nil {
		return err
	}
	for _, file := range files {
		content := file.Content
		if file.Name == "conf.yaml" {
			s, err := ConfigSnippet(logPath)
			if err != nil {
				return err
			}
			content = s
		}
		if err := os.WriteFile(filepath.Join(outputDir, file.Name), []byte(content), 0644); err != nil {
			return err
		}
	}
	return nil
}
