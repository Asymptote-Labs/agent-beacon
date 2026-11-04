package wazuh

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/siempack"
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

func files(localfile string) []File {
	return []File{
		{Name: "ossec-localfile.xml", Content: localfile},
		{Name: "beacon-rules.xml", Content: mustRead("pack/beacon-rules.xml")},
		{Name: "sample-event.jsonl", Content: mustRead("pack/sample-event.jsonl")},
		{Name: "apply-dashboard-default-columns.sh", Content: mustRead("pack/apply-dashboard-default-columns.sh")},
		{Name: "README.md", Content: mustRead("pack/README.md")},
	}
}

// LocalfileSnippet returns the ossec.conf <localfile> block for logPath. Wazuh reads the path as
// written and has no way to escape a '<' in it, so such a path is refused.
func LocalfileSnippet(logPath string) (string, error) {
	if logPath == "" {
		logPath = DefaultLogPath
	}
	if strings.Contains(logPath, "<") {
		return "", fmt.Errorf("wazuh cannot read a log path containing '<': %s", logPath)
	}
	return siempack.RenderLogPath("ossec-localfile.xml", mustRead("pack/ossec-localfile.xml"), logPath), nil
}

func InstallPack(outputDir, logPath string) error {
	localfile, err := LocalfileSnippet(logPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return err
	}
	for _, file := range files(localfile) {
		if err := os.WriteFile(filepath.Join(outputDir, file.Name), []byte(file.Content), 0644); err != nil {
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
