package sumo

import (
	"embed"
	"fmt"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/siempack"
)

//go:embed pack/*
var packFS embed.FS

const (
	DefaultLogPath   = "/var/log/beacon-agent/runtime.jsonl"
	DefaultOutputDir = "beacon-sumo-pack"
)

type File struct {
	Name            string
	Content         string
	TemplateLogPath bool
}

func Files() []File {
	return []File{
		{Name: "README.md", Content: mustRead("pack/README.md")},
		{Name: "sumo-upload-smoke-test.sh", Content: mustRead("pack/sumo-upload-smoke-test.sh.tmpl"), TemplateLogPath: true},
		{Name: "sample-event.jsonl", Content: mustRead("pack/sample-event.jsonl")},
		{Name: "vector.toml", Content: mustRead("pack/vector.toml.tmpl"), TemplateLogPath: true},
	}
}

func UploadSmokeTest(logPath string) string {
	if logPath == "" {
		logPath = DefaultLogPath
	}
	return siempack.RenderLogPath(mustRead("pack/sumo-upload-smoke-test.sh.tmpl"), logPath)
}

func InstallPack(outputDir, logPath string) error {
	if outputDir == "" {
		outputDir = DefaultOutputDir
	}
	if logPath == "" {
		logPath = DefaultLogPath
	}
	files := make([]siempack.File, 0, len(Files()))
	for _, file := range Files() {
		files = append(files, siempack.File(file))
	}
	return siempack.Install(outputDir, files, logPath)
}

func mustRead(path string) string {
	data, err := siempack.ReadFile(packFS, path)
	if err != nil {
		panic(fmt.Sprintf("sumo pack asset %s: %v", path, err))
	}
	return data
}
