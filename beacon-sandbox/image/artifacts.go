package image

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The collector carries the telemetry normalization, so which build you run matters more than it
// looks: a stale or downloaded collector will not contain local exporter changes, and a run
// against it silently verifies the wrong code.
const (
	collectorRelPath = "collector-builder/dist/beacon-otelcol/linux_amd64/beacon-otelcol"
	beaconRelPath    = "cli/beacon/beacon-linux-amd64"

	// releaseAPI is queried for the newest published tarball. Only used to fill in a missing
	// collector; nothing here ever fetches the `beacon` CLI, which must come from your tree.
	releaseAPI = "https://api.github.com/repos/Asymptote-Labs/agent-beacon/releases/latest"
)

// BeaconPath and CollectorPath report where each artifact is expected.
func BeaconPath(repoRoot string) string    { return filepath.Join(repoRoot, beaconRelPath) }
func CollectorPath(repoRoot string) string { return filepath.Join(repoRoot, collectorRelPath) }

// BuildBeaconHint is the command that produces the Beacon binary under test.
const BuildBeaconHint = "cd cli/beacon && make build-linux-amd64"

// EnsureCollector returns the collector path, downloading a released build if absent.
//
// Deliberately does not attempt to *build* the collector: that needs the OpenTelemetry Collector
// Builder and a multi-step invocation, which is the single biggest setup cliff. Published
// releases ship `beacon-otelcol` inside the Linux tarball, so a download covers everyone who has
// not modified the exporter.
func EnsureCollector(repoRoot string, log func(string, ...any)) (string, error) {
	if log == nil {
		log = func(string, ...any) {}
	}
	dst := CollectorPath(repoRoot)
	if fi, err := os.Stat(dst); err == nil && fi.Size() > 0 {
		return dst, nil
	}

	rel, err := latestLinuxTarball()
	if err != nil {
		return "", fmt.Errorf("collector missing at %s and no release could be resolved: %w\n"+
			"build it instead:\n"+
			"  go install go.opentelemetry.io/collector/cmd/builder@v0.121.0\n"+
			"  cd collector-builder && mkdir -p dist && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \"$(go env GOPATH)/bin/builder\" --config builder.yaml", dst, err)
	}
	log("collector not found; downloading beacon-otelcol from release %s", rel.Version)
	if err := downloadCollector(rel, dst, log); err != nil {
		return "", fmt.Errorf("download collector from %s: %w", rel.URL, err)
	}
	if err := writeProvenance(repoRoot, rel.Version); err != nil {
		// Not fatal: the binary is usable. But freshness checking degrades without it, so say so.
		log("warning: could not record collector provenance (%v); freshness checking will be weaker", err)
	}
	log("collector ready at %s", dst)
	return dst, nil
}

// provenancePath records where the collector binary came from. Without it, a downloaded release
// binary is indistinguishable from one built from the current tree, which is the difference
// between "verifies your change" and "verifies last release".
func provenancePath(repoRoot string) string {
	return CollectorPath(repoRoot) + ".provenance"
}

// writeProvenance records the release a binary was downloaded from, together with enough of the
// binary's identity to tell later whether the marker still describes it.
//
// The identity matters more than it looks. A first version recorded only the release tag, and
// nothing clears the marker when someone rebuilds locally -- which is exactly what the freshness
// warning tells them to do. CollectorIsStale then kept reporting "downloaded from release X, and
// collector-builder/ has changed since" forever, so the warning could never be cleared by doing
// the thing it asked for. A warning that cannot be satisfied trains people to ignore it, which is
// worse than the false pass it replaced. Reported by Cursor Bugbot.
func writeProvenance(repoRoot, version string) error {
	fi, err := os.Stat(CollectorPath(repoRoot))
	if err != nil {
		return err
	}
	body := fmt.Sprintf("release:%s\nsize:%d\nmtime:%d\n",
		version, fi.Size(), fi.ModTime().UnixNano())
	return os.WriteFile(provenancePath(repoRoot), []byte(body), 0o644)
}

// CollectorIsStale reports whether the on-disk beacon-otelcol may not contain the exporter code
// in the current tree, and why.
//
// The first version only looked at uncommitted `git status` under collector-builder/, which left
// the two most likely false passes wide open: exporter changes that were already committed, and a
// binary fetched by `doctor --fix` from a release that predates the branch entirely. Either way
// `collector_freshness` reported green while the binary under test lacked the change -- exactly
// the trap this check exists to catch, and the trap that already cost one wasted investigation.
// Cursor Bugbot flagged the gap.
//
// Three signals now, cheapest first:
//
//   - uncommitted changes under collector-builder/
//   - a downloaded release binary with any collector-builder/ change since that release tag
//   - a locally built binary older than the newest collector-builder/ source file
func CollectorIsStale(repoRoot string) (bool, string) {
	if files := uncommittedCollectorChanges(repoRoot); files != "" {
		return true, "uncommitted changes under collector-builder/: " + files
	}

	binary := CollectorPath(repoRoot)
	fi, err := os.Stat(binary)
	if err != nil {
		// No binary is a separate check's problem, not a freshness question.
		return false, ""
	}

	if tag := recordedRelease(repoRoot); tag != "" {
		changed, resolved := collectorChangesSince(repoRoot, tag)
		switch {
		case resolved && changed != "":
			return true, fmt.Sprintf("binary was downloaded from release %s, but collector-builder/ "+
				"has changed since: %s", tag, changed)
		case resolved:
			return false, ""
		default:
			// The tag cannot be resolved, so drift is unknowable. Reported rather than assumed
			// clean: "I could not check" must never render as "nothing to check".
			return true, fmt.Sprintf("binary was downloaded from release %s, which this clone "+
				"cannot resolve, so drift cannot be checked (try `git fetch --tags`, or rebuild "+
				"the collector locally)", tag)
		}
	}

	// Locally built: a source file newer than the binary means the binary predates it.
	if newer := sourcesNewerThan(repoRoot, fi.ModTime()); newer != "" {
		return true, "collector-builder/ sources are newer than the built binary: " + newer
	}
	return false, ""
}

func uncommittedCollectorChanges(repoRoot string) string {
	out, err := exec.Command("git", "-C", repoRoot, "status", "--porcelain", "--", "collector-builder").Output()
	if err != nil {
		return ""
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// dist/ is the build output itself, so changes there are the *result* of rebuilding,
		// never a reason to rebuild. Excluded explicitly rather than relying on the repo's
		// .gitignore: a checkout that does not ignore it would otherwise report the collector
		// binary as a reason the collector is stale, which is circular and always true.
		if strings.Contains(line, "collector-builder/dist") {
			continue
		}
		files = append(files, line)
	}
	return summarize3(files)
}

// recordedRelease returns the release tag a downloaded binary came from, empty when the binary was
// built locally, predates provenance recording, or has been replaced since the marker was written.
//
// That last case is the important one: a marker that outlives the binary it described would keep a
// freshness warning permanently lit after a local rebuild, so the size and mtime recorded at
// download time are checked against the binary that is actually there now.
func recordedRelease(repoRoot string) string {
	b, err := os.ReadFile(provenancePath(repoRoot))
	if err != nil {
		return ""
	}
	fields := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), ":"); ok {
			fields[k] = v
		}
	}
	tag := strings.TrimSpace(fields["release"])
	if tag == "" {
		return ""
	}
	fi, err := os.Stat(CollectorPath(repoRoot))
	if err != nil {
		return ""
	}
	// A marker written before identity was recorded cannot be validated, so it is not trusted.
	if fields["size"] == "" || fields["mtime"] == "" {
		return ""
	}
	if fields["size"] != strconv.FormatInt(fi.Size(), 10) ||
		fields["mtime"] != strconv.FormatInt(fi.ModTime().UnixNano(), 10) {
		// The binary was replaced after the marker was written -- almost certainly a local
		// rebuild, which is what we asked for. Fall through to the mtime comparison.
		return ""
	}
	return tag
}

// collectorChangesSince lists collector-builder/ files that changed between a release tag and HEAD,
// and reports whether the tag could be resolved at all.
//
// The distinction is load-bearing. An unresolvable tag used to return an empty list, which the
// caller read as "no drift" and turned into a confident *fresh* -- skipping the mtime fallback
// entirely. `doctor --fix` can easily record a tag the local clone does not have yet (no
// `git fetch --tags`, or a release newer than the checkout), so with exporter changes present the
// freshness check reported green and a run verified the wrong beacon-otelcol. That is precisely the
// false pass this check exists to prevent. Reported by Cursor Bugbot.
func collectorChangesSince(repoRoot, tag string) (changed string, resolved bool) {
	if err := exec.Command("git", "-C", repoRoot, "rev-parse", "--verify", tag+"^{commit}").Run(); err != nil {
		return "", false
	}
	out, err := exec.Command("git", "-C", repoRoot, "diff", "--name-only", tag+"..HEAD", "--", "collector-builder").Output()
	if err != nil {
		return "", false
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		// Same reasoning as the uncommitted signal: dist/ is the build output, so listing it
		// would report the collector binary as a reason the collector is stale.
		if line == "" || strings.Contains(line, "collector-builder/dist") {
			continue
		}
		files = append(files, line)
	}
	return summarize3(files), true
}

// sourcesNewerThan lists collector-builder Go/config files modified after the binary was built.
func sourcesNewerThan(repoRoot string, built time.Time) string {
	var newer []string
	root := filepath.Join(repoRoot, "collector-builder")
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			// dist/ holds the build output itself, so walking it would compare the binary
			// against its own siblings and report churn that means nothing.
			if err == nil && d != nil && d.IsDir() && d.Name() == "dist" {
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(p) {
		case ".go", ".yaml", ".yml", ".mod", ".sum":
		default:
			return nil
		}
		if fi, err := d.Info(); err == nil && fi.ModTime().After(built) {
			rel, _ := filepath.Rel(repoRoot, p)
			newer = append(newer, rel)
		}
		return nil
	})
	sort.Strings(newer)
	return summarize3(newer)
}

func summarize3(files []string) string {
	if len(files) == 0 {
		return ""
	}
	if len(files) > 3 {
		files = append(files[:3:3], fmt.Sprintf("… and %d more", len(files)-3))
	}
	return strings.Join(files, "; ")
}

func latestLinuxTarball() (rel releaseAsset, err error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(releaseAPI)
	if err != nil {
		return releaseAsset{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return releaseAsset{}, fmt.Errorf("release API returned %s", resp.Status)
	}
	var body struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return releaseAsset{}, err
	}
	out := releaseAsset{Version: body.TagName}
	for _, a := range body.Assets {
		switch {
		case strings.HasSuffix(a.Name, "_linux_amd64.tar.gz"):
			out.Name, out.URL = a.Name, a.URL
		case a.Name == "checksums.txt":
			out.ChecksumsURL = a.URL
		}
	}
	if out.URL == "" {
		return releaseAsset{}, fmt.Errorf("no linux_amd64 tarball in release %s", body.TagName)
	}
	// Refusing rather than proceeding unverified: this binary gets executed inside the sandbox,
	// so an unauthenticated download is a supply-chain hole, not a convenience.
	if out.ChecksumsURL == "" {
		return releaseAsset{}, fmt.Errorf("release %s publishes no checksums.txt, so the "+
			"collector download cannot be verified; build it locally instead", body.TagName)
	}
	return out, nil
}

// releaseAsset is the tarball to fetch plus the means to verify it.
type releaseAsset struct {
	Version      string
	Name         string
	URL          string
	ChecksumsURL string
}

// expectedSHA256 pulls one file's digest out of a GoReleaser checksums.txt ("<hex>  <name>").
func expectedSHA256(client *http.Client, checksumsURL, name string) (string, error) {
	resp, err := client.Get(checksumsURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("checksums.txt returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(body), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[1] == name {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("checksums.txt has no entry for %s", name)
}

// downloadCollector fetches the release tarball, verifies its SHA-256 against the release's own
// checksums.txt, and only then extracts beacon-otelcol.
//
// Verification is not optional even for contributor tooling: this binary is executed inside the
// sandbox, so accepting whatever the network returned would make a compromised proxy or mirror
// enough to run arbitrary code. The tarball is buffered to a temp file rather than streamed,
// because a digest cannot be checked until the whole body has been read and extracting first
// would defeat the point. Reported by the Copilot reviewer.
func downloadCollector(rel releaseAsset, dst string, log func(string, ...any)) error {
	client := &http.Client{Timeout: 10 * time.Minute}
	want, err := expectedSHA256(client, rel.ChecksumsURL, rel.Name)
	if err != nil {
		return fmt.Errorf("resolve expected checksum: %w", err)
	}
	resp, err := client.Get(rel.URL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %s", resp.Status)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	// Buffer the tarball and hash it as it lands, so the digest can be checked before anything
	// is extracted from it.
	tarball, err := os.CreateTemp(filepath.Dir(dst), "beacon-otelcol-*.tar.gz")
	if err != nil {
		return err
	}
	defer os.Remove(tarball.Name())
	sum := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tarball, sum), resp.Body); err != nil {
		tarball.Close()
		return err
	}
	if err := tarball.Close(); err != nil {
		return err
	}
	got := hex.EncodeToString(sum.Sum(nil))
	if got != want {
		return fmt.Errorf("checksum mismatch for %s: release checksums.txt says %s, downloaded "+
			"bytes hash to %s; refusing to use it", rel.Name, want, got)
	}
	log("verified %s against release checksums.txt (sha256 %s…)", rel.Name, got[:12])

	verified, err := os.Open(tarball.Name())
	if err != nil {
		return err
	}
	defer verified.Close()
	gz, err := gzip.NewReader(verified)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("beacon-otelcol not found in the tarball")
		}
		if err != nil {
			return err
		}
		if filepath.Base(hdr.Name) != "beacon-otelcol" || hdr.Typeflag != tar.TypeReg {
			continue
		}
		// Write to a temp file and rename, so an interrupted download never leaves a
		// truncated binary that would fail confusingly inside the sandbox.
		tmp := dst + ".partial"
		f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, tr); err != nil {
			// Report both: a close failure here can itself indicate why the copy failed
			// (a full disk surfaces on either call depending on buffering).
			closeErr := f.Close()
			os.Remove(tmp)
			if closeErr != nil {
				return fmt.Errorf("%w (and closing %s failed: %v)", err, tmp, closeErr)
			}
			return err
		}
		if err := f.Close(); err != nil {
			os.Remove(tmp)
			return err
		}
		return os.Rename(tmp, dst)
	}
}
