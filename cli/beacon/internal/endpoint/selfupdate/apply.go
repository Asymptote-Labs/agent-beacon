package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/updatecheck"
)

// maxPackageBytes caps a downloaded update artifact. The signed .pkg is tens of
// MB; this is a generous ceiling that still bounds a hostile response.
const maxPackageBytes = 512 << 20

// runnerFunc executes an external command and returns combined output. It is
// injectable so tests can avoid real installer/pkgutil/launchctl calls.
type runnerFunc func(ctx context.Context, name string, args ...string) (string, error)

func execRun(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

// ApplyResult summarizes an apply attempt.
type ApplyResult struct {
	FromVersion string
	ToVersion   string
	Applied     bool
	RolledBack  bool
	Message     string
}

// Applier performs the full download → verify → install → health-check →
// rollback flow. Zero value is not usable; use NewApplier.
type Applier struct {
	CurrentVersion string
	ManifestURL    string // override; empty uses resolveManifestURL()
	StageDir       string // default StateDir()
	InstallPrefix  string // default "/" (real installer); a temp dir in tests
	LogPath        string // telemetry log; empty uses the system runtime log

	// AllowInsecureTest enables the install-prefix tarball seam and is required
	// for SkipGatekeeper. It is never set by the launchd job or normal CLI use.
	AllowInsecureTest bool
	// SkipGatekeeper relaxes notarization/staple checks. Requires
	// AllowInsecureTest. The sha256 check is always enforced regardless.
	SkipGatekeeper bool

	HTTPClient *http.Client
	run        runnerFunc
	now        func() time.Time
}

// NewApplier returns an Applier with production defaults.
func NewApplier(currentVersion string) *Applier {
	return &Applier{
		CurrentVersion: currentVersion,
		StageDir:       StateDir(),
		InstallPrefix:  "/",
		HTTPClient:     &http.Client{Timeout: 10 * time.Minute},
		run:            execRun,
		now:            time.Now,
	}
}

func (a *Applier) runner() runnerFunc {
	if a.run != nil {
		return a.run
	}
	return execRun
}

func (a *Applier) clock() func() time.Time {
	if a.now != nil {
		return a.now
	}
	return time.Now
}

// Apply runs the full update flow. It is safe to call when no update is
// available (returns Applied=false). Any failure before the install step leaves
// the system untouched; a failed install triggers a binary rollback.
func (a *Applier) Apply(ctx context.Context) (ApplyResult, error) {
	if a.SkipGatekeeper && !a.AllowInsecureTest {
		return ApplyResult{}, fmt.Errorf("SkipGatekeeper requires AllowInsecureTest")
	}
	result := ApplyResult{FromVersion: a.CurrentVersion}

	// Discover.
	src := updatecheck.ManifestSource{
		Client:   &http.Client{Timeout: 30 * time.Second},
		Endpoint: a.manifestURL(),
	}
	manifest, err := src.Fetch(ctx)
	if err != nil {
		return result, fmt.Errorf("fetch update manifest: %w", err)
	}
	eval, err := updatecheck.EvaluateManifest(a.CurrentVersion, manifest)
	if err != nil {
		return result, err
	}
	if eval.CurrentIsDev {
		result.Message = "dev build; skipping"
		return result, nil
	}
	if !eval.UpdateAvailable {
		result.Message = "already up to date"
		return result, nil
	}
	result.ToVersion = manifest.Version

	artifact, ok := manifest.ArtifactFor(updatecheck.RuntimeArchKey())
	if !ok {
		return result, fmt.Errorf("no update artifact for %s", updatecheck.RuntimeArchKey())
	}

	// Serialize concurrent updaters.
	stage := a.stageDir()
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return result, fmt.Errorf("create staging dir: %w", err)
	}
	unlock, err := acquireLock(filepath.Join(stage, ".update.lock"))
	if err != nil {
		return result, fmt.Errorf("another update is already running: %w", err)
	}
	defer unlock()

	// Download + verify before touching the system.
	pkgPath := filepath.Join(stage, "download"+packageExt(artifact.URL))
	if err := a.download(ctx, artifact.URL, pkgPath); err != nil {
		return result, fmt.Errorf("download update: %w", err)
	}
	defer os.Remove(pkgPath)

	if err := verifySHA256(pkgPath, artifact.SHA256); err != nil {
		a.emit(false, result, err.Error())
		return result, fmt.Errorf("verify checksum: %w", err)
	}
	if !a.SkipGatekeeper {
		if err := a.verifyGatekeeper(ctx, pkgPath, manifest.TeamID); err != nil {
			a.emit(false, result, err.Error())
			return result, fmt.Errorf("verify signature/notarization: %w", err)
		}
	}

	// Snapshot binaries for rollback, then install.
	backup, _ := a.snapshotBinaries()
	if err := a.install(ctx, pkgPath); err != nil {
		// Install itself failed; attempt restore if we have a snapshot.
		if backup != "" {
			_ = a.restoreBinaries(backup)
			result.RolledBack = true
		}
		a.emit(false, result, err.Error())
		return result, fmt.Errorf("install update: %w", err)
	}

	// Health-check; roll back binaries if the new install is unhealthy.
	if err := a.healthCheck(ctx, manifest.Version); err != nil {
		if backup != "" {
			if rbErr := a.restoreBinaries(backup); rbErr == nil {
				result.RolledBack = true
				_ = a.restartCollector(ctx)
			}
		}
		a.emit(false, result, "post-install health check failed: "+err.Error())
		return result, fmt.Errorf("post-install health check failed: %w", err)
	}

	result.Applied = true
	result.Message = fmt.Sprintf("updated %s -> %s", a.CurrentVersion, manifest.Version)
	a.emit(true, result, result.Message)
	return result, nil
}

func (a *Applier) manifestURL() string {
	if strings.TrimSpace(a.ManifestURL) != "" {
		return a.ManifestURL
	}
	return resolveManifestURL()
}

func (a *Applier) stageDir() string {
	if a.StageDir != "" {
		return a.StageDir
	}
	return StateDir()
}

func (a *Applier) prefix() string {
	if a.InstallPrefix != "" {
		return a.InstallPrefix
	}
	return "/"
}

// download streams the artifact to dest with a size cap.
func (a *Applier) download(ctx context.Context, url, dest string) error {
	client := a.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "beacon-self-update")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxPackageBytes+1))
	if err != nil {
		return err
	}
	if n > maxPackageBytes {
		return fmt.Errorf("artifact exceeds %d bytes", maxPackageBytes)
	}
	return nil
}

func verifySHA256(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, strings.TrimSpace(want)) {
		return fmt.Errorf("sha256 mismatch: got %s want %s", got, want)
	}
	return nil
}

// verifyGatekeeper confirms the .pkg is a Developer ID Installer-signed,
// notarized, and stapled package before it is run.
func (a *Applier) verifyGatekeeper(ctx context.Context, pkgPath, teamID string) error {
	run := a.runner()
	out, err := run(ctx, "pkgutil", "--check-signature", pkgPath)
	if err != nil {
		return fmt.Errorf("pkgutil --check-signature: %s: %w", strings.TrimSpace(out), err)
	}
	if teamID != "" && !strings.Contains(out, teamID) {
		return fmt.Errorf("package signature does not match expected team id %s", teamID)
	}
	if out, err := run(ctx, "stapler", "validate", pkgPath); err != nil {
		return fmt.Errorf("stapler validate: %s: %w", strings.TrimSpace(out), err)
	}
	if out, err := run(ctx, "spctl", "--assess", "--type", "install", "-vv", pkgPath); err != nil {
		return fmt.Errorf("spctl assessment failed: %s: %w", strings.TrimSpace(out), err)
	}
	return nil
}

// install applies the package. Production runs the macOS installer into "/";
// the insecure test seam expands a tarball into a temp prefix instead.
func (a *Applier) install(ctx context.Context, pkgPath string) error {
	if a.AllowInsecureTest {
		return extractTarballInto(pkgPath, a.prefix())
	}
	out, err := a.runner()(ctx, "installer", "-pkg", pkgPath, "-target", a.prefix())
	if err != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(out), err)
	}
	return nil
}

// binDir is the install tree's binary directory under the active prefix.
func (a *Applier) binDir() string {
	return filepath.Join(a.prefix(), "opt", "beacon", "bin")
}

func (a *Applier) snapshotBinaries() (string, error) {
	src := a.binDir()
	if _, err := os.Stat(src); err != nil {
		return "", err // nothing to snapshot (fresh/test); rollback unavailable
	}
	dst := filepath.Join(a.stageDir(), "rollback")
	_ = os.RemoveAll(dst)
	if err := copyTree(src, dst); err != nil {
		return "", err
	}
	return dst, nil
}

func (a *Applier) restoreBinaries(backup string) error {
	return copyTree(backup, a.binDir())
}

// healthCheck confirms the freshly installed beacon reports the expected
// version and, in production, that the collector service is running.
func (a *Applier) healthCheck(ctx context.Context, wantVersion string) error {
	bin := filepath.Join(a.binDir(), "beacon")
	out, err := a.runner()(ctx, bin, "version")
	if err != nil {
		return fmt.Errorf("run %s version: %s: %w", bin, strings.TrimSpace(out), err)
	}
	if !strings.Contains(out, wantVersion) {
		return fmt.Errorf("installed binary reports %q, expected version %s", strings.TrimSpace(out), wantVersion)
	}
	if a.AllowInsecureTest {
		return nil
	}
	// Give launchd a moment to relaunch the collector via the pkg postinstall.
	mgr := service.Manager{UserMode: false}
	deadline := a.clock()().Add(15 * time.Second)
	for {
		if mgr.Status().Running {
			return nil
		}
		if !a.clock()().Before(deadline) {
			return fmt.Errorf("collector service is not running after update")
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (a *Applier) restartCollector(ctx context.Context) error {
	mgr := service.Manager{UserMode: false}
	_ = mgr.Unload()
	return mgr.Load()
}

func packageExt(url string) string {
	switch {
	case strings.HasSuffix(url, ".tar.gz"):
		return ".tar.gz"
	case strings.HasSuffix(url, ".tgz"):
		return ".tgz"
	default:
		return ".pkg"
	}
}
