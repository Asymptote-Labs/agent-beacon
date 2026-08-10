package wazuh

import (
	"embed"
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"os"
	"path/filepath"
	"strings"
)

//go:embed pack/*
var packFS embed.FS

// DefaultLogPath is a var rather than a const because the system log location is now
// resolved per platform by one function instead of repeated as a literal in every pack.
var DefaultLogPath = endpointconfig.SystemLogPath()

type File struct {
	Name    string
	Content string
}

func Files() []File {
	localfile := LocalfileSnippet(DefaultLogPath)
	rules := mustRead("pack/beacon-rules.xml")
	sample := mustRead("pack/sample-event.jsonl")
	readme := mustRead("pack/README.md")
	dashboardColumns := mustRead("pack/apply-dashboard-default-columns.sh")
	return []File{
		{Name: "ossec-localfile.xml", Content: localfile},
		{Name: "beacon-rules.xml", Content: rules},
		{Name: "sample-event.jsonl", Content: sample},
		{Name: "apply-dashboard-default-columns.sh", Content: dashboardColumns},
		{Name: "README.md", Content: readme},
	}
}

func LocalfileSnippet(logPath string) string {
	if logPath == "" {
		logPath = DefaultLogPath
	}
	return strings.ReplaceAll(mustRead("pack/ossec-localfile.xml"), "{{LOG_PATH}}", logPath)
}

func InstallPack(outputDir, logPath string) error {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return err
	}
	for _, file := range Files() {
		content := file.Content
		if file.Name == "ossec-localfile.xml" {
			content = LocalfileSnippet(logPath)
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
		return ""
	}
	return string(data)
}
