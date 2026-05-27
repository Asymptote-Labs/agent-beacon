package sumo

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
	DefaultOutputDir = "beacon-sumo-pack"
)

type File struct {
	Name            string
	Content         string
	TemplateLogPath bool
	JSONEscape      bool
}

// readAsset reads an embedded asset using the module-level packFS and returns its
// contents or an error.
func readAsset(path string) (string, error) {
	data, err := siempack.ReadFile(packFS, path)
	if err != nil {
		return "", fmt.Errorf("sumo pack asset %q: %w", path, err)
	}
	return data, nil
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

// uploadSmokeTestFromFS renders the Sumo upload smoke-test script using the supplied FS.
func uploadSmokeTestFromFS(fsys fs.FS, logPath string) (string, error) {
	if logPath == "" {
		logPath = DefaultLogPath
	}
	content, err := siempack.ReadFile(fsys, "pack/sumo-upload-smoke-test.sh.tmpl")
	if err != nil {
		return "", fmt.Errorf("sumo pack asset %q: %w", "pack/sumo-upload-smoke-test.sh.tmpl", err)
	}
	return siempack.RenderLogPath(content, logPath), nil
}

// filesFromFS builds the full file list from the supplied FS, propagating any
// read errors instead of panicking. Template substitution of LOG_PATH is
// deferred to siempack.Install via the TemplateLogPath field.
func filesFromFS(fsys fs.FS) ([]File, error) {
	readme, err := siempack.ReadFile(fsys, "pack/README.md")
	if err != nil {
		return nil, fmt.Errorf("sumo pack asset %q: %w", "pack/README.md", err)
	}
	smokeTest, err := siempack.ReadFile(fsys, "pack/sumo-upload-smoke-test.sh.tmpl")
	if err != nil {
		return nil, fmt.Errorf("sumo pack asset %q: %w", "pack/sumo-upload-smoke-test.sh.tmpl", err)
	}
	sample, err := siempack.ReadFile(fsys, "pack/sample-event.jsonl")
	if err != nil {
		return nil, fmt.Errorf("sumo pack asset %q: %w", "pack/sample-event.jsonl", err)
	}
	vector, err := siempack.ReadFile(fsys, "pack/vector.toml.tmpl")
	if err != nil {
		return nil, fmt.Errorf("sumo pack asset %q: %w", "pack/vector.toml.tmpl", err)
	}
	return []File{
		{Name: "README.md", Content: readme},
		{Name: "sumo-upload-smoke-test.sh", Content: smokeTest, TemplateLogPath: true},
		{Name: "sample-event.jsonl", Content: sample},
		{Name: "vector.toml", Content: vector, TemplateLogPath: true},
	}, nil
}

// Files returns all pack files, propagating any embedded asset read error.
func Files() ([]File, error) {
	return filesFromFS(packFS)
}

// UploadSmokeTest returns the Sumo upload smoke-test script with logPath substituted.
func UploadSmokeTest(logPath string) (string, error) {
	return uploadSmokeTestFromFS(packFS, logPath)
}

// InstallPack writes the pack files to outputDir with logPath substituted.
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
