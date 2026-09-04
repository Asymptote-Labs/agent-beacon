// Package asymptote is the content pack for forwarding Beacon endpoint JSONL to
// Asymptote's managed ingest service.
//
// It is the one destination that sends telemetry to an Asymptote-run endpoint,
// so it is deliberately shaped like every other Vector pack: Beacon stays the
// local JSONL producer, Vector does the network, and the device key lives in a
// separate 0600 secrets file read through Vector's secret backend. The rendered
// files are valid for running Vector by hand; `beacon endpoint connect` fills
// the same template in for the managed forwarder service.
package asymptote

import (
	"io/fs"
	"strings"

	"embed"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/siempack"
)

//go:embed pack/*
var packFS embed.FS

const (
	// DefaultOutputDir is where install-pack writes when --output is omitted.
	DefaultOutputDir = "beacon-asymptote-pack"

	// EnvIngestURL, EnvSecretsFile and EnvDataDir are the environment variables the
	// rendered vector.toml reads. connect writes them into the forwarder unit; a
	// hand-run Vector exports them.
	EnvIngestURL   = "BEACON_ASYMPTOTE_INGEST_URL"
	EnvSecretsFile = "BEACON_ASYMPTOTE_SECRETS_FILE"
	EnvDataDir     = "BEACON_ASYMPTOTE_DATA_DIR"

	// SecretsKey is the JSON key inside the secrets file; the template references it
	// as SECRET[beacon.device_key].
	SecretsKey = "device_key"

	// RuntimeIngestPath and InventoryIngestPath are appended to the ingest URL.
	RuntimeIngestPath   = "/v1/ingest/runtime"
	InventoryIngestPath = "/v1/ingest/inventory"
	// CredentialCheckPath answers 200 or 401 for a bearer device key. Vector's
	// sink healthcheck sends its auth header, so pointing it here makes a revoked
	// key visible at startup. HealthPath is the unauthenticated liveness probe.
	CredentialCheckPath = "/v1/ingest/health"
	HealthPath          = "/health"
)

// DefaultLogPath is resolved per platform like the other packs.
var DefaultLogPath = endpointconfig.SystemLogPath()

const (
	vectorAsset    = "pack/vector.toml.tmpl"
	smokeTestAsset = "pack/asymptote-ingest-smoke-test.sh.tmpl"
)

// File is the installable pack-file type, shared with siempack.
type File = siempack.File

var pack = siempack.Pack{
	Label:            "asymptote",
	FS:               packFS,
	DefaultLogPath:   DefaultLogPath,
	DefaultOutputDir: DefaultOutputDir,
	Assets: []siempack.Asset{
		{Source: "pack/README.md", Name: "README.md"},
		{Source: smokeTestAsset, Name: "asymptote-ingest-smoke-test.sh", TemplateLogPath: true},
		{Source: "pack/sample-event.jsonl", Name: "sample-event.jsonl"},
		{Source: vectorAsset, Name: "vector.toml", TemplateLogPath: true},
	},
}

// mustRead returns the embedded asset at path or panics. Retained for test use.
func mustRead(path string) string { return pack.MustRead(path) }

// filesFromFS builds the file list from the supplied FS; tests use it to inject
// read-error conditions.
func filesFromFS(fsys fs.FS) ([]File, error) { return pack.WithFS(fsys).Files() }

// Files returns all pack files, propagating any embedded asset read error.
func Files() ([]File, error) { return pack.Files() }

// VectorConfig returns the Vector forwarder config with logPath substituted. The
// ingest URL, secrets file and data directory stay as environment references.
func VectorConfig(logPath string) (string, error) { return pack.Render(vectorAsset, logPath) }

// IngestSmokeTest returns the one-shot credential and upload check script with
// logPath substituted.
func IngestSmokeTest(logPath string) (string, error) { return pack.Render(smokeTestAsset, logPath) }

// InstallPack writes the pack files to outputDir with logPath substituted.
func InstallPack(outputDir, logPath string) error { return pack.Install(outputDir, logPath) }

// RenderOptions are the concrete values a managed forwarder needs in place of the
// template's environment references.
type RenderOptions struct {
	LogPath     string
	IngestURL   string
	SecretsFile string
	DataDir     string
}

// RenderVectorConfig returns vector.toml with every environment reference replaced
// by a literal value, for a forwarder unit that must not depend on its
// environment. It refuses non-https ingest URLs so a downgraded endpoint can never
// receive a device key.
func RenderVectorConfig(opts RenderOptions) (string, error) {
	if !strings.HasPrefix(strings.ToLower(opts.IngestURL), "https://") {
		return "", ErrInsecureIngestURL
	}
	if opts.SecretsFile == "" || opts.DataDir == "" {
		return "", ErrIncompleteRender
	}
	content, err := VectorConfig(opts.LogPath)
	if err != nil {
		return "", err
	}
	ingest := strings.TrimRight(opts.IngestURL, "/")
	replacements := []struct{ from, to string }{
		{`"${` + EnvDataDir + `:-/var/lib/vector/beacon-asymptote}"`, tomlString(opts.DataDir)},
		{`"${` + EnvSecretsFile + `}"`, tomlString(opts.SecretsFile)},
		{`"${` + EnvIngestURL + `}` + RuntimeIngestPath + `"`, tomlString(ingest + RuntimeIngestPath)},
		{`"${` + EnvIngestURL + `}` + InventoryIngestPath + `"`, tomlString(ingest + InventoryIngestPath)},
		{`"${` + EnvIngestURL + `}` + CredentialCheckPath + `"`, tomlString(ingest + CredentialCheckPath)},
	}
	for _, r := range replacements {
		content = strings.ReplaceAll(content, r.from, r.to)
	}
	if strings.Contains(content, "${"+EnvIngestURL) || strings.Contains(content, "${"+EnvSecretsFile) || strings.Contains(content, "${"+EnvDataDir) {
		return "", ErrIncompleteRender
	}
	return content, nil
}

// tomlString quotes s as a TOML basic string.
func tomlString(s string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`)
	return `"` + replacer.Replace(s) + `"`
}

// SecretsFileContent returns the JSON document the secrets file must hold for the
// template's SECRET[beacon.device_key] reference.
func SecretsFileContent(deviceKey string) string {
	return `{"` + SecretsKey + `": ` + jsonString(deviceKey) + "}\n"
}

func jsonString(s string) string {
	return `"` + siempack.JSONEscapeForString(s) + `"`
}

// Sentinel errors returned by RenderVectorConfig.
type renderError string

func (e renderError) Error() string { return string(e) }

const (
	ErrInsecureIngestURL renderError = "asymptote ingest URL must use https://"
	ErrIncompleteRender  renderError = "asymptote forwarder render needs ingest URL, secrets file and data dir"
)
