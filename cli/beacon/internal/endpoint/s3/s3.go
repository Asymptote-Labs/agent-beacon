package s3

import (
	"embed"
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/siempack"
)

//go:embed pack/*
var packFS embed.FS

const (
	DefaultOutputDir = "beacon-s3-pack"
)

// DefaultLogPath is a var rather than a const because the system log location is now
// resolved per platform by one function instead of repeated as a literal in every pack.
var DefaultLogPath = endpointconfig.SystemLogPath()

const smokeTestAsset = "pack/s3-upload-smoke-test.sh.tmpl"

// File is the installable pack-file type, shared with siempack.
type File = siempack.File

var pack = siempack.Pack{
	Label:            "s3",
	FS:               packFS,
	DefaultLogPath:   DefaultLogPath,
	DefaultOutputDir: DefaultOutputDir,
	Assets: []siempack.Asset{
		{Source: "pack/README.md", Name: "README.md"},
		{Source: smokeTestAsset, Name: "s3-upload-smoke-test.sh", TemplateLogPath: true},
		{Source: "pack/sample-event.jsonl", Name: "sample-event.jsonl"},
		{Source: "pack/vector.toml.tmpl", Name: "vector.toml", TemplateLogPath: true},
	},
}

// mustRead returns the embedded asset at path or panics. Retained for test use.
func mustRead(path string) string { return pack.MustRead(path) }

// Files returns all pack files, propagating any embedded asset read error.
func Files() ([]File, error) { return pack.Files() }

// UploadSmokeTest returns the S3 upload smoke-test script with logPath substituted.
func UploadSmokeTest(logPath string) (string, error) { return pack.Render(smokeTestAsset, logPath) }

// InstallPack writes the pack files to outputDir with logPath substituted.
func InstallPack(outputDir, logPath string) error { return pack.Install(outputDir, logPath) }
