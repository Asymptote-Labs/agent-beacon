package asymptote

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// MinVectorVersion is the oldest Vector the forwarder template is validated against; the
// `secret` file backend and the http sink options it uses exist from here on.
const MinVectorVersion = "0.50.0"

// VectorBinEnv names an explicit Vector binary, ahead of every search location.
const VectorBinEnv = "BEACON_VECTOR_BIN"

// PackagedVectorPath is where the signed macOS package installs Vector.
const PackagedVectorPath = "/opt/beacon/bin/vector"

// TapVectorFormula is the Homebrew formula the tap mirrors Vector as, and the name of the
// keg `brew install beacon` pulls in on macOS.
//
// It is deliberately not "vector". Homebrew allows exactly one keg named `vector` no matter
// which tap it came from, so a tap formula by that name made `brew install/upgrade beacon`
// fail outright for anyone who already had Vector from vectordotdev/brew. Under its own name
// the keg coexists with any other Vector, and it keeps its binary in libexec -- which
// Homebrew never links into the prefix -- so it also cannot collide on a bin/vector symlink.
// That is why it is only ever found by path, never through PATH.
const TapVectorFormula = "beacon-vector"

// VectorInfo describes the Vector binary connect will run.
type VectorInfo struct {
	Path    string `json:"path"`
	Version string `json:"version"`
}

// ErrVectorNotFound is returned when no usable Vector binary exists.
//
// The Homebrew hint names the tap's own formula rather than a bare "brew install vector":
// Vector is not in homebrew-core, so the bare name is ambiguous once both vectordotdev/brew
// and asymptote-labs/tap are tapped, and Homebrew refuses an ambiguous name. Any Vector at
// or above MinVectorVersion satisfies FindVector, whichever tap or package it came from.
var ErrVectorNotFound = errors.New("vector was not found; install it from https://vector.dev " +
	"(macOS: brew install asymptote-labs/tap/" + TapVectorFormula + ") or set " + VectorBinEnv)

// runCommandOutput is swapped by tests to fake `vector --version`.
var runCommandOutput = func(bin string, args ...string) ([]byte, error) {
	return exec.Command(bin, args...).CombinedOutput()
}

// lookPath is swapped by tests; defaultLookPath restores it.
var (
	defaultLookPath = exec.LookPath
	lookPath        = defaultLookPath
)

// FindVector locates a Vector binary of at least MinVectorVersion.
//
// Order: the explicit argument, BEACON_VECTOR_BIN, the packaged /opt/beacon/bin/vector, the
// beacon-vector keg in each Homebrew prefix, a linked vector in each Homebrew prefix, then
// PATH. The first candidate that exists is checked for version; a too-old binary is an error
// rather than a reason to keep searching, because silently running a different Vector than the
// one the operator pointed at would be worse. That is also why the beacon-vector keg is tried
// ahead of any other Homebrew Vector: it is the copy whose version the tap pins, so it cannot
// be the one that turns a working install into a version error.
func FindVector(explicit string) (VectorInfo, error) {
	for _, candidate := range vectorCandidates(explicit) {
		if candidate == "" {
			continue
		}
		path := candidate
		if !strings.Contains(candidate, string(os.PathSeparator)) {
			resolved, err := lookPath(candidate)
			if err != nil {
				continue
			}
			path = resolved
		} else if info, err := os.Stat(candidate); err != nil || info.IsDir() {
			continue
		}
		version, err := vectorVersion(path)
		if err != nil {
			return VectorInfo{}, fmt.Errorf("%s: %w", path, err)
		}
		if compareVersions(version, MinVectorVersion) < 0 {
			return VectorInfo{}, fmt.Errorf("%s is Vector %s; managed forwarding needs %s or newer", path, version, MinVectorVersion)
		}
		return VectorInfo{Path: path, Version: version}, nil
	}
	return VectorInfo{}, ErrVectorNotFound
}

// vectorSearchPaths lists the fixed install locations tried after the explicit and
// environment candidates. Tests replace it so a Vector installed on the developer's machine
// does not leak into a test that expects none.
var vectorSearchPaths = defaultVectorSearchPaths

func defaultVectorSearchPaths() []string {
	paths := []string{PackagedVectorPath}
	prefixes := homebrewPrefixes()
	// Every beacon-vector keg first, then every linked vector. Interleaving the two per
	// prefix would let an old linked Vector in the first prefix mask the pinned keg in the
	// second, and FindVector treats a too-old binary as an error rather than moving on.
	for _, prefix := range prefixes {
		paths = append(paths, filepath.Join(prefix, "opt", TapVectorFormula, "libexec", "vector"))
	}
	for _, prefix := range prefixes {
		paths = append(paths, filepath.Join(prefix, "bin", "vector"))
	}
	return paths
}

// homebrewPrefixes lists the Homebrew prefixes to look under, HOMEBREW_PREFIX first when the
// caller set one. The defaults are the per-platform standard prefixes: Apple Silicon and Intel
// macOS, and the Linuxbrew prefix. A prefix that does not exist just yields candidates that
// fail the stat in FindVector.
func homebrewPrefixes() []string {
	var prefixes []string
	if prefix := strings.TrimSpace(os.Getenv("HOMEBREW_PREFIX")); prefix != "" {
		prefixes = append(prefixes, prefix)
	}
	switch runtime.GOOS {
	case "darwin":
		prefixes = append(prefixes, "/opt/homebrew", "/usr/local")
	case "linux":
		prefixes = append(prefixes, "/home/linuxbrew/.linuxbrew")
	}
	seen := make(map[string]bool, len(prefixes))
	unique := prefixes[:0]
	for _, prefix := range prefixes {
		if seen[prefix] {
			continue
		}
		seen[prefix] = true
		unique = append(unique, prefix)
	}
	return unique
}

func vectorCandidates(explicit string) []string {
	candidates := []string{explicit, os.Getenv(VectorBinEnv)}
	candidates = append(candidates, vectorSearchPaths()...)
	return append(candidates, "vector")
}

var vectorVersionPattern = regexp.MustCompile(`vector\s+v?(\d+\.\d+\.\d+)`)

func vectorVersion(bin string) (string, error) {
	out, err := runCommandOutput(bin, "--version")
	if err != nil {
		return "", fmt.Errorf("could not run vector --version: %w", err)
	}
	match := vectorVersionPattern.FindStringSubmatch(string(out))
	if match == nil {
		return "", fmt.Errorf("could not read a version from %q", strings.TrimSpace(string(out)))
	}
	return match[1], nil
}

// compareVersions orders dotted numeric versions; unparsable parts compare as zero.
func compareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// ValidateVectorConfig runs `vector validate --skip-healthchecks` so a broken render is caught
// before a service unit points at it. Health checks are skipped because they need the network
// and the freshly written key; the service's own startup healthcheck covers that.
func ValidateVectorConfig(vectorBin, configPath string) error {
	out, err := runCommandOutput(vectorBin, "validate", "--skip-healthchecks", configPath)
	if err != nil {
		return fmt.Errorf("vector validate failed for %s: %s", configPath, strings.TrimSpace(string(out)))
	}
	return nil
}

// dirSize sums regular files under root; used to report the disk buffer size.
func dirSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}
