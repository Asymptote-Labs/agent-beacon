package datadog

import (
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"io/fs"

	"embed"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/siempack"
)

//go:embed pack/*
var packFS embed.FS

const (
	DefaultOutputDir = "beacon-datadog-pack"
)

// DefaultLogPath is a var rather than a const because the system log location is now
// resolved per platform by one function instead of repeated as a literal in every pack.
var DefaultLogPath = endpointconfig.SystemLogPath()

const configAsset = "pack/conf.yaml.tmpl"

// File is the installable pack-file type, shared with siempack.
type File = siempack.File

var pack = siempack.Pack{
	Label:            "datadog",
	FS:               packFS,
	DefaultLogPath:   DefaultLogPath,
	DefaultOutputDir: DefaultOutputDir,
	Assets: []siempack.Asset{
		{Source: "pack/README.md", Name: "README.md"},
		{Source: configAsset, Name: "conf.yaml", TemplateLogPath: true},
		{Source: "pack/sample-event.jsonl", Name: "sample-event.jsonl"},
	},
}

// mustRead returns the embedded asset at path or panics. Retained for test use.
func mustRead(path string) string { return pack.MustRead(path) }

// filesFromFS builds the file list from the supplied FS; tests use it to inject
// read-error conditions. Log-path substitution is deferred to InstallPack, so the
// logPath argument is accepted for signature compatibility but unused here.
func filesFromFS(fsys fs.FS, logPath string) ([]File, error) { return pack.WithFS(fsys).Files() }

// configSnippetFromFS renders the Datadog Agent conf.yaml snippet using the supplied FS.
func configSnippetFromFS(fsys fs.FS, logPath string) (string, error) {
	return pack.WithFS(fsys).Render(configAsset, logPath)
}

// ConfigSnippet returns the Datadog Agent conf.yaml snippet with logPath substituted.
func ConfigSnippet(logPath string) (string, error) { return pack.Render(configAsset, logPath) }

// Files returns all pack files, propagating any embedded asset read error.
func Files() ([]File, error) { return pack.Files() }

// InstallPack writes the pack files to outputDir with logPath substituted.
func InstallPack(outputDir, logPath string) error { return pack.Install(outputDir, logPath) }
