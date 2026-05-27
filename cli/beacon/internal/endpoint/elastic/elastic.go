package elastic

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
	DefaultOutputDir = "beacon-elastic-pack"
)

type File struct {
	Name    string
	Content string
}

// readAsset reads an embedded asset by path and returns its contents or an error.
func readAsset(path string) (string, error) {
	data, err := packFS.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("elastic pack asset %q: %w", path, err)
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

// inputSnippetFromFS renders the filebeat input snippet using the supplied FS.
func inputSnippetFromFS(fsys fs.FS, logPath string) (string, error) {
	if logPath == "" {
		logPath = DefaultLogPath
	}
	content, err := siempack.ReadFile(fsys, "pack/filebeat.yml.tmpl")
	if err != nil {
		return "", fmt.Errorf("elastic pack asset %q: %w", "pack/filebeat.yml.tmpl", err)
	}
	return strings.ReplaceAll(content, "{{LOG_PATH}}", logPath), nil
}

// standaloneAgentConfigFromFS renders the elastic-agent standalone config using the supplied FS.
func standaloneAgentConfigFromFS(fsys fs.FS, logPath string) (string, error) {
	if logPath == "" {
		logPath = DefaultLogPath
	}
	content, err := siempack.ReadFile(fsys, "pack/elastic-agent-standalone.yml.tmpl")
	if err != nil {
		return "", fmt.Errorf("elastic pack asset %q: %w", "pack/elastic-agent-standalone.yml.tmpl", err)
	}
	return strings.ReplaceAll(content, "{{LOG_PATH}}", logPath), nil
}

// filesFromFS builds the full file list from the supplied FS, propagating any
// read errors instead of panicking.
func filesFromFS(fsys fs.FS, logPath string) ([]File, error) {
	readme, err := siempack.ReadFile(fsys, "pack/README.md")
	if err != nil {
		return nil, fmt.Errorf("elastic pack asset %q: %w", "pack/README.md", err)
	}
	fbSnippet, err := inputSnippetFromFS(fsys, logPath)
	if err != nil {
		return nil, err
	}
	agentConfig, err := standaloneAgentConfigFromFS(fsys, logPath)
	if err != nil {
		return nil, err
	}
	ilm, err := siempack.ReadFile(fsys, "pack/ilm-policy.json")
	if err != nil {
		return nil, fmt.Errorf("elastic pack asset %q: %w", "pack/ilm-policy.json", err)
	}
	compMappings, err := siempack.ReadFile(fsys, "pack/component-template-mappings.json")
	if err != nil {
		return nil, fmt.Errorf("elastic pack asset %q: %w", "pack/component-template-mappings.json", err)
	}
	compSettings, err := siempack.ReadFile(fsys, "pack/component-template-settings.json")
	if err != nil {
		return nil, fmt.Errorf("elastic pack asset %q: %w", "pack/component-template-settings.json", err)
	}
	idxTemplate, err := siempack.ReadFile(fsys, "pack/index-template.json")
	if err != nil {
		return nil, fmt.Errorf("elastic pack asset %q: %w", "pack/index-template.json", err)
	}
	ingestPipeline, err := siempack.ReadFile(fsys, "pack/ingest-pipeline.json")
	if err != nil {
		return nil, fmt.Errorf("elastic pack asset %q: %w", "pack/ingest-pipeline.json", err)
	}
	kibana, err := siempack.ReadFile(fsys, "pack/kibana-assets.ndjson")
	if err != nil {
		return nil, fmt.Errorf("elastic pack asset %q: %w", "pack/kibana-assets.ndjson", err)
	}
	compose, err := siempack.ReadFile(fsys, "pack/docker-compose.yml")
	if err != nil {
		return nil, fmt.Errorf("elastic pack asset %q: %w", "pack/docker-compose.yml", err)
	}
	sample, err := siempack.ReadFile(fsys, "pack/sample-event.jsonl")
	if err != nil {
		return nil, fmt.Errorf("elastic pack asset %q: %w", "pack/sample-event.jsonl", err)
	}
	return []File{
		{Name: "README.md", Content: readme},
		{Name: "filebeat.yml", Content: fbSnippet},
		{Name: "elastic-agent-standalone.yml", Content: agentConfig},
		{Name: "ilm-policy.json", Content: ilm},
		{Name: "component-template-mappings.json", Content: compMappings},
		{Name: "component-template-settings.json", Content: compSettings},
		{Name: "index-template.json", Content: idxTemplate},
		{Name: "ingest-pipeline.json", Content: ingestPipeline},
		{Name: "kibana-assets.ndjson", Content: kibana},
		{Name: "docker-compose.yml", Content: compose},
		{Name: "sample-event.jsonl", Content: sample},
	}, nil
}

// Files returns all pack files rendered with DefaultLogPath, propagating any
// embedded asset read error.
func Files() ([]File, error) {
	return filesFromFS(packFS, DefaultLogPath)
}

// InputSnippet returns the Filebeat input snippet with logPath substituted.
func InputSnippet(logPath string) (string, error) {
	return inputSnippetFromFS(packFS, logPath)
}

// standaloneAgentConfig returns the elastic-agent standalone config with logPath substituted.
func standaloneAgentConfig(logPath string) (string, error) {
	return standaloneAgentConfigFromFS(packFS, logPath)
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
		switch file.Name {
		case "filebeat.yml":
			s, err := InputSnippet(logPath)
			if err != nil {
				return err
			}
			content = s
		case "elastic-agent-standalone.yml":
			s, err := standaloneAgentConfig(logPath)
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
