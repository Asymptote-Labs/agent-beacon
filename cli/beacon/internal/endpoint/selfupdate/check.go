package selfupdate

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/updatecheck"
)

// CheckResult is the outcome of a read-only update check.
type CheckResult struct {
	updatecheck.ManifestResult
	Install     Install
	Mode        Mode
	ArchKey     string
	Artifact    updatecheck.Artifact
	HasArtifact bool
}

// resolveManifestURL picks the manifest URL with precedence env > managed >
// default, mirroring ResolveMode's "managed can override default, env wins".
func resolveManifestURL() string {
	if v := strings.TrimSpace(os.Getenv(updatecheck.ManifestURLEnv)); v != "" {
		return v
	}
	if m := ManagedManifestURL(); m != "" {
		return m
	}
	return updatecheck.DefaultManifestURL
}

// Check fetches the manifest and compares it to the running build. It performs
// no privileged actions and makes a single network request.
func Check(ctx context.Context, currentVersion string) (CheckResult, error) {
	install := DetectInstall()
	localMode := "" // the cmd layer passes the config value via CheckWithMode
	res := CheckResult{Install: install, Mode: ResolveMode(localMode), ArchKey: updatecheck.RuntimeArchKey()}

	src := updatecheck.ManifestSource{
		Client:   &http.Client{Timeout: 10 * time.Second},
		Endpoint: resolveManifestURL(),
	}
	manifest, err := src.Fetch(ctx)
	if err != nil {
		return res, err
	}
	eval, err := updatecheck.EvaluateManifest(currentVersion, manifest)
	if err != nil {
		return res, err
	}
	res.ManifestResult = eval
	if a, ok := manifest.ArtifactFor(res.ArchKey); ok {
		res.Artifact = a
		res.HasArtifact = true
	}
	return res, nil
}

// CheckWithMode is like Check but resolves the effective mode using the local
// config mode value as the lowest-precedence input.
func CheckWithMode(ctx context.Context, currentVersion, localMode string) (CheckResult, error) {
	res, err := Check(ctx, currentVersion)
	res.Mode = ResolveMode(localMode)
	return res, err
}
