package sentinel

import (
	"embed"
	"fmt"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/siempack"
)

//go:embed pack/*
var packFS embed.FS

const (
	DefaultLogPath   = "/var/log/beacon-agent/runtime.jsonl"
	DefaultOutputDir = "beacon-sentinel-pack"
)

func Files() []siempack.File {
	return []siempack.File{
		{Name: "README.md", Content: mustRead("pack/README.md")},
		{Name: "dcr-transform.kql", Content: DCRTransform()},
		{Name: "table-schema.json", Content: mustRead("pack/table-schema.json")},
		{Name: "dcr-template.json", Content: renderDCRTemplate(), TemplateLogPath: true, JSONEscape: true},
		{Name: "queries.kql", Content: mustRead("pack/queries.kql")},
		{Name: "detections.kql", Content: mustRead("pack/detections.kql")},
		{Name: "sample-event.jsonl", Content: mustRead("pack/sample-event.jsonl")},
	}
}

func DCRTransform() string {
	return mustRead("pack/dcr-transform.kql")
}

func InstallPack(outputDir, logPath string) error {
	if outputDir == "" {
		outputDir = DefaultOutputDir
	}
	if logPath == "" {
		logPath = DefaultLogPath
	}
	return siempack.Install(outputDir, Files(), logPath)
}

func renderDCRTemplate() string {
	tmpl := mustRead("pack/dcr-template.json")
	return strings.ReplaceAll(tmpl, "{{DCR_TRANSFORM}}", siempack.JSONEscapeForString(minifyKQL(DCRTransform())))
}

func minifyKQL(kql string) string {
	lines := strings.Split(kql, "\n")
	parts := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, " ")
}

func mustRead(path string) string {
	data, err := siempack.ReadFile(packFS, path)
	if err != nil {
		panic(fmt.Sprintf("sentinel pack asset %s: %v", path, err))
	}
	return data
}
