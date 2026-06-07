package gcs

import (
	"embed"
	"io/fs"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/siempack"
)

//go:embed pack/*
var packFS embed.FS

const (
	DefaultLogPath   = "/var/log/beacon-agent/runtime.jsonl"
	DefaultOutputDir = "beacon-gcs-pack"
)

const smokeTestAsset = "pack/gcs-upload-smoke-test.sh.tmpl"

// File is the installable pack-file type, shared with siempack.
type File = siempack.File

var pack = siempack.Pack{
	Label:            "gcs",
	FS:               packFS,
	DefaultLogPath:   DefaultLogPath,
	DefaultOutputDir: DefaultOutputDir,
	Assets: []siempack.Asset{
		{Source: "pack/README.md", Name: "README.md"},
		{Source: smokeTestAsset, Name: "gcs-upload-smoke-test.sh", TemplateLogPath: true},
		{Source: "pack/sample-event.jsonl", Name: "sample-event.jsonl"},
		{Source: "pack/vector.toml.tmpl", Name: "vector.toml", TemplateLogPath: true},
	},
}

// mustRead returns the embedded asset at path or panics. Retained for test use.
func mustRead(path string) string { return pack.MustRead(path) }

// filesFromFS builds the file list from the supplied FS; tests use it to inject
// read-error conditions.
func filesFromFS(fsys fs.FS) ([]File, error) { return pack.WithFS(fsys).Files() }

// uploadSmokeTestFromFS renders the GCS upload smoke-test script using the supplied FS.
func uploadSmokeTestFromFS(fsys fs.FS, logPath string) (string, error) {
	return pack.WithFS(fsys).Render(smokeTestAsset, logPath)
}

// Files returns all pack files, propagating any embedded asset read error.
func Files() ([]File, error) { return pack.Files() }

// UploadSmokeTest returns the GCS upload smoke-test script with logPath substituted.
func UploadSmokeTest(logPath string) (string, error) { return pack.Render(smokeTestAsset, logPath) }

// InstallPack writes the pack files to outputDir with logPath substituted.
func InstallPack(outputDir, logPath string) error { return pack.Install(outputDir, logPath) }
