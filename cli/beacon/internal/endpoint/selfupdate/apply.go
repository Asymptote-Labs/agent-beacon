package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/updatecheck"
)

// maxPackageBytes caps a downloaded update artifact. The signed .pkg is tens of
// MB; this is a generous ceiling that still bounds a hostile response.
const maxPackageBytes = 512 << 20

const collectorHealthTimeout = 60 * time.Second

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
	// Verification names the checks the artifact actually passed. Recorded rather than implied,
	// because it differs by platform: macOS adds notarization on top of the checksum, Linux has
	// no OS-level equivalent, and "verified" must not paper over which of the two happened.
	Verification string
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
	restart    func() error
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
	if a.AllowInsecureTest && filepath.Clean(a.prefix()) == string(filepath.Separator) {
		return ApplyResult{}, fmt.Errorf("AllowInsecureTest requires a non-root install prefix")
	}
	result := ApplyResult{FromVersion: a.CurrentVersion}
	current := strings.TrimSpace(a.CurrentVersion)
	if current == "dev" {
		result.Message = "dev build; skipping"
		return result, nil
	}
	if !updatecheck.CanCheckVersion(current) {
		result.Message = "current version cannot be compared to releases"
		return result, nil
	}

	// Discover.
	src := updatecheck.ManifestSource{
		Client:   &http.Client{Timeout: 30 * time.Second},
		Endpoint: a.manifestURL(),
	}
	manifest, err := src.Fetch(ctx)
	if err != nil {
		a.emit(false, result, "fetch update manifest: "+err.Error())
		return result, fmt.Errorf("fetch update manifest: %w", err)
	}
	eval, err := updatecheck.EvaluateManifest(a.CurrentVersion, manifest)
	if err != nil {
		a.emit(false, result, err.Error())
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

	artifact, key, ok := selectArtifact(manifest, a.AllowInsecureTest)
	if !ok {
		a.emit(false, result, fmt.Sprintf("no update artifact this host can install (looked for %s)", key))
		return result, fmt.Errorf("no update artifact this host can install (looked for %s)", key)
	}

	// Serialize concurrent updaters.
	stage := a.stageDir()
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return result, fmt.Errorf("create staging dir: %w", err)
	}
	unlock, err := acquireLock(filepath.Join(stage, ".update.lock"))
	if err != nil {
		a.emit(false, result, "another update is already running: "+err.Error())
		return result, fmt.Errorf("another update is already running: %w", err)
	}
	defer unlock()

	// Download + verify before touching the system.
	pkgPath := filepath.Join(stage, "download"+packageExt(artifact.URL))
	if err := a.download(ctx, artifact.URL, pkgPath); err != nil {
		a.emit(false, result, "download update: "+err.Error())
		return result, fmt.Errorf("download update: %w", err)
	}
	defer os.Remove(pkgPath)

	if err := verifySHA256(pkgPath, artifact.SHA256); err != nil {
		a.emit(false, result, err.Error())
		return result, fmt.Errorf("verify checksum: %w", err)
	}
	// Gatekeeper is macOS-only: pkgutil, stapler and spctl do not exist elsewhere, and there is
	// no OS-level equivalent on Linux. Skipped explicitly and logged rather than silently, so
	// the weaker verification is visible in the system log rather than assumed.
	//
	// SHA-256 verification above still applies on every platform and is not optional. The trust
	// root that leaves is HTTPS plus the release checksum -- which is the same trust root the
	// user relied on to obtain the package they are updating, so this is parity with the install
	// path rather than a downgrade. Notarization gives macOS assurance above that; adding a
	// comparable Linux story means a signing scheme (GPG-signed metadata or Sigstore) with real
	// key management, and that is a deliberate decision rather than something to bolt on here.
	if runtime.GOOS != "darwin" {
		result.Verification = "sha256"
	} else if !a.SkipGatekeeper {
		result.Verification = "sha256+notarization"
		if err := a.verifyGatekeeper(ctx, pkgPath, manifest.TeamID); err != nil {
			a.emit(false, result, err.Error())
			return result, fmt.Errorf("verify signature/notarization: %w", err)
		}
	}

	// Snapshot the whole install tree for rollback, then install. A snapshot
	// failure on an existing install aborts before we touch anything: proceeding
	// would leave us unable to roll back.
	backup, err := a.snapshotInstall()
	if err != nil {
		a.emit(false, result, "rollback snapshot failed: "+err.Error())
		return result, fmt.Errorf("snapshot current install for rollback: %w", err)
	}

	if err := a.install(ctx, pkgPath); err != nil {
		rollbackErr := a.rollback(backup, &result)
		message := err.Error()
		if rollbackErr != nil {
			message += "; rollback failed: " + rollbackErr.Error()
		}
		a.emit(false, result, message)
		a.cleanupFailedUpdate(result, message, rollbackErr)
		return result, fmt.Errorf("install update: %w", err)
	}

	if !a.AllowInsecureTest {
		if err := a.restartCollector(); err != nil {
			rollbackErr := a.rollback(backup, &result)
			message := "post-install collector restart failed: " + err.Error()
			if rollbackErr != nil {
				message += "; rollback failed: " + rollbackErr.Error()
			}
			a.emit(false, result, message)
			a.cleanupFailedUpdate(result, message, rollbackErr)
			return result, fmt.Errorf("post-install collector restart failed: %w", err)
		}
	}

	if err := a.healthCheck(ctx, manifest.Version); err != nil {
		rollbackErr := a.rollback(backup, &result)
		message := "post-install health check failed: " + err.Error()
		if rollbackErr != nil {
			message += "; rollback failed: " + rollbackErr.Error()
		}
		a.emit(false, result, message)
		a.cleanupFailedUpdate(result, message, rollbackErr)
		return result, fmt.Errorf("post-install health check failed: %w", err)
	}

	result.Applied = true
	result.Message = fmt.Sprintf("updated %s -> %s", a.CurrentVersion, manifest.Version)
	a.cleanupSuccessfulUpdate()
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

func (a *Applier) cleanupSuccessfulUpdate() {
	a.cleanupLargeUpdateArtifacts()
	_ = os.Remove(filepath.Join(a.stageDir(), "last_failure.json"))
}

func (a *Applier) cleanupLargeUpdateArtifacts() {
	stage := a.stageDir()
	for _, pattern := range []string{
		filepath.Join(stage, "download*"),
		filepath.Join(stage, "rollback"),
	} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for _, match := range matches {
			_ = os.RemoveAll(match)
		}
	}
}

type failureDiagnostic struct {
	Timestamp     string `json:"timestamp"`
	FromVersion   string `json:"from_version,omitempty"`
	ToVersion     string `json:"to_version,omitempty"`
	Reason        string `json:"reason"`
	RolledBack    bool   `json:"rolled_back"`
	RollbackError string `json:"rollback_error,omitempty"`
}

func (a *Applier) cleanupFailedUpdate(result ApplyResult, reason string, rollbackErr error) {
	a.writeFailureDiagnostic(result, reason, rollbackErr)
	if rollbackErr != nil || !result.RolledBack {
		return
	}
	a.cleanupLargeUpdateArtifacts()
}

func (a *Applier) writeFailureDiagnostic(result ApplyResult, reason string, rollbackErr error) {
	diag := failureDiagnostic{
		Timestamp:   a.clock()().UTC().Format(time.RFC3339),
		FromVersion: result.FromVersion,
		ToVersion:   result.ToVersion,
		Reason:      reason,
		RolledBack:  result.RolledBack,
	}
	if rollbackErr != nil {
		diag.RollbackError = rollbackErr.Error()
	}
	data, err := json.MarshalIndent(diag, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(a.stageDir(), "last_failure.json"), append(data, '\n'), 0o644)
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

// verifyGatekeeper confirms the .pkg is Developer ID Installer-signed and
// accepted by Gatekeeper before it is run. stapler validation is best-effort:
// endpoint machines commonly have only Command Line Tools installed, where the
// stapler shim exists but refuses to run without full Xcode.
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
		if !staplerUnavailable(out, err) {
			return fmt.Errorf("stapler validate: %s: %w", strings.TrimSpace(out), err)
		}
	}
	if out, err := run(ctx, "spctl", "--assess", "--type", "install", "-vv", pkgPath); err != nil {
		return fmt.Errorf("spctl assessment failed: %s: %w", strings.TrimSpace(out), err)
	}
	return nil
}

func staplerUnavailable(out string, err error) bool {
	msg := strings.ToLower(strings.TrimSpace(out + " " + err.Error()))
	return strings.Contains(msg, "requires xcode") ||
		strings.Contains(msg, "command line tools instance") ||
		strings.Contains(msg, "executable file not found") ||
		strings.Contains(msg, "no such file or directory")
}

// install applies the package. Production runs the macOS installer into "/";
// the insecure test seam expands a tarball into a temp prefix instead.
func (a *Applier) install(ctx context.Context, pkgPath string) error {
	if a.AllowInsecureTest {
		return extractTarballInto(pkgPath, a.prefix())
	}
	name, args := installerCommand(pkgPath, a.prefix())
	out, err := a.runner()(ctx, name, args...)
	if err != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(out), err)
	}
	return nil
}

// installerCommand picks the tool that installs a staged package.
//
// dpkg and rpm are used directly rather than apt or dnf: the artifact is a local file already
// downloaded and checksum-verified, so there is no repository to consult, and the higher-level
// tools would only add a dependency-resolution step that can prompt or reach the network. An
// update must not become interactive halfway through.
//
// Both are asked to upgrade in place rather than install fresh, so the running version is
// replaced rather than conflicting with itself.
func installerCommand(pkgPath, prefix string) (string, []string) {
	switch {
	case strings.HasSuffix(pkgPath, ".deb"):
		return "dpkg", []string{"--install", pkgPath}
	case strings.HasSuffix(pkgPath, ".rpm"):
		return "rpm", []string{"--upgrade", "--replacepkgs", pkgPath}
	default:
		// Everything else keeps the path it took before Linux support existed, so macOS
		// behaviour is unchanged. Note filepath.Ext is unusable here: it reports ".gz" for a
		// .tar.gz, which is why this matches suffixes the way packageExt does.
		return "installer", []string{"-pkg", pkgPath, "-target", prefix}
	}
}

// linuxPackageExt reports which native package format this host can install.
func linuxPackageExt() string {
	if _, err := exec.LookPath("dpkg"); err == nil {
		return ".deb"
	}
	if _, err := exec.LookPath("rpm"); err == nil {
		return ".rpm"
	}
	// Neither is present, so no native install is possible. Assume deb so the resulting failure
	// names dpkg, which is actionable, rather than the absence of the macOS installer. Callers that
	// need to know whether an install is possible at all must ask hasPackageManager, not this.
	return ".deb"
}

// hasPackageManager reports whether this host has any native package tool.
func hasPackageManager() bool {
	if _, err := exec.LookPath("dpkg"); err == nil {
		return true
	}
	_, err := exec.LookPath("rpm")
	return err == nil
}

// installDir is the root of the installed tree under the active prefix.
func (a *Applier) installDir() string {
	return filepath.Join(a.prefix(), "opt", "beacon")
}

// binDir is the install tree's binary directory under the active prefix.
func (a *Applier) binDir() string {
	return filepath.Join(a.installDir(), "bin")
}

// snapshotInstall copies the whole /opt/beacon tree (binaries and scripts) to a
// rollback location so a failed update can be reverted as a unit, not just its
// binaries. It returns ("", nil) when there is nothing installed yet (a fresh
// install — no rollback is possible or needed), and an error only on a real
// copy failure, which the caller treats as fatal before touching the system.
func (a *Applier) snapshotInstall() (string, error) {
	src := a.installDir()
	info, err := os.Stat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", src)
	}
	dst := filepath.Join(a.stageDir(), "rollback", "beacon")
	if err := os.RemoveAll(dst); err != nil {
		return "", err
	}
	if err := copyTree(src, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// rollback restores the pre-update install tree and restarts the collector. It
// sets result.RolledBack only when a snapshot existed and was restored
// successfully; with no snapshot (a first update) the failed new version stays
// in place and RolledBack remains false, so telemetry never over-claims a
// rollback that did not happen. Endpoint config/plists live outside the tree but
// reference stable paths and a release-stable schema, so the restored older
// binaries run against them consistently after the collector restarts.
func (a *Applier) rollback(backup string, result *ApplyResult) error {
	if backup == "" {
		return nil
	}
	live := a.installDir()
	restore := filepath.Join(a.stageDir(), "rollback", "restore")
	failed := filepath.Join(a.stageDir(), "rollback", "failed-install")
	if err := os.RemoveAll(restore); err != nil {
		return err
	}
	if err := copyTree(backup, restore); err != nil {
		return err
	}
	_ = os.RemoveAll(failed)
	if err := os.Rename(live, failed); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(restore, live); err != nil {
		if _, statErr := os.Stat(failed); statErr == nil {
			_ = os.Rename(failed, live)
		}
		return err
	}
	_ = os.RemoveAll(failed)
	result.RolledBack = true
	if !a.AllowInsecureTest {
		if err := a.restartCollector(); err != nil {
			return err
		}
	}
	return nil
}

// healthCheck confirms the freshly installed beacon reports the expected
// version and, in production, that the collector service is running.
func (a *Applier) healthCheck(ctx context.Context, wantVersion string) error {
	bin := filepath.Join(a.binDir(), "beacon")
	out, err := a.runner()(ctx, bin, "version")
	if err != nil {
		return fmt.Errorf("run %s version: %s: %w", bin, strings.TrimSpace(out), err)
	}
	if !versionLineMatches(out, wantVersion) {
		return fmt.Errorf("installed binary reports %q, expected version %s", strings.TrimSpace(out), wantVersion)
	}
	if a.AllowInsecureTest {
		return nil
	}
	// Give launchd a moment to relaunch the collector via the pkg postinstall.
	mgr := service.Manager{UserMode: false}
	deadline := a.clock()().Add(collectorHealthTimeout)
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

func (a *Applier) restartCollector() error {
	if a.restart != nil {
		return a.restart()
	}
	return service.Manager{UserMode: false}.Restart()
}

// versionLineMatches reports whether `beacon version` output
// ("beacon version <V> (<commit>) built on <date>") reports exactly wantVersion.
// It matches the version token after "version" for equality rather than a
// substring, so e.g. an installed 0.0.6 does not satisfy an expected 0.0.69.
func versionLineMatches(out, want string) bool {
	want = strings.TrimPrefix(strings.TrimSpace(want), "v")
	fields := strings.Fields(out)
	for i := 0; i+1 < len(fields); i++ {
		if fields[i] == "version" {
			return strings.TrimPrefix(fields[i+1], "v") == want
		}
	}
	return false
}

// selectArtifact picks the release artifact this host can actually install.
//
// One key per architecture is not enough on Linux, because two incompatible package formats share
// an architecture. A manifest that published only the .deb for linux_arm64 would hand a Fedora host
// a .deb, and installerCommand would dutifully run dpkg -- which is not installed there. The update
// would fail at the last step, after downloading and verifying, with an error about a missing
// program rather than about the wrong format.
//
// So a format-qualified key is preferred (linux_arm64_rpm), and the bare architecture key is
// accepted only when what it points at is installable here. That keeps a single-format manifest
// working on the platform it was built for while refusing, clearly, on the platform it was not.
// extractsTarballs is the applier's AllowInsecureTest seam: with it on, `install` expands a tarball
// into a temp prefix, so a tarball artifact is genuinely installable. It is threaded through rather
// than assumed, because the answer to "can this host install this" differs between the two modes and
// guessing either way breaks something real.
func selectArtifact(m updatecheck.UpdateManifest, extractsTarballs bool) (updatecheck.Artifact, string, bool) {
	arch := updatecheck.RuntimeArchKey()
	if runtime.GOOS != "linux" {
		a, ok := m.ArtifactFor(arch)
		return a, arch, ok
	}
	format := strings.TrimPrefix(linuxPackageExt(), ".")
	qualified := arch + "_" + format
	if a, ok := m.ArtifactFor(qualified); ok {
		return a, qualified, true
	}
	if a, ok := m.ArtifactFor(arch); ok && installableHere(a.URL, extractsTarballs) {
		return a, arch, true
	}
	return updatecheck.Artifact{}, qualified, false
}

// installableHere reports whether a URL names something this host can install.
//
// A native package qualifies only in the format this host has a tool for. A tarball qualifies only
// when the caller extracts tarballs: in production `install` does not, so a .tar.gz reaches
// installerCommand's default branch and is handed to the macOS `installer`, which cannot open it and
// does not exist on Linux at all. Accepting one unconditionally would mean downloading and
// checksum-verifying an artifact and only then discovering it is uninstallable.
func installableHere(url string, extractsTarballs bool) bool {
	if strings.HasSuffix(url, ".tar.gz") || strings.HasSuffix(url, ".tgz") {
		return extractsTarballs
	}
	if !strings.HasSuffix(url, ".deb") && !strings.HasSuffix(url, ".rpm") {
		return false
	}
	// linuxPackageExt falls back to .deb when neither tool exists, so the absence of a package
	// manager has to be checked separately or a .deb would look installable on a host with no dpkg
	// at all.
	return hasPackageManager() && strings.HasSuffix(url, linuxPackageExt())
}

// packageExt names the staged artifact so the installer that runs on it can dispatch correctly.
//
// The URL wins when it already carries a recognised extension, because the manifest is the
// authority on what was published. Otherwise fall back to the platform's native format: .pkg on
// macOS, and on Linux whichever of dpkg or rpm this host actually has.
func packageExt(url string) string {
	switch {
	case strings.HasSuffix(url, ".tar.gz"):
		return ".tar.gz"
	case strings.HasSuffix(url, ".tgz"):
		return ".tgz"
	case strings.HasSuffix(url, ".deb"):
		return ".deb"
	case strings.HasSuffix(url, ".rpm"):
		return ".rpm"
	case runtime.GOOS == "linux":
		return linuxPackageExt()
	default:
		return ".pkg"
	}
}
