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

// VectorInfo describes the Vector binary connect will run.
type VectorInfo struct {
	Path    string `json:"path"`
	Version string `json:"version"`
}

// ErrVectorNotFound is returned when no usable Vector binary exists.
var ErrVectorNotFound = errors.New("vector was not found; install it from https://vector.dev (Homebrew: brew install vector) or set " + VectorBinEnv)

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
// Homebrew prefixes, then PATH. The first candidate that exists is checked for version; a too-old
// binary is an error rather than a reason to keep searching, because silently running a
// different Vector than the one the operator pointed at would be worse.
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
	if runtime.GOOS == "darwin" {
		paths = append(paths, "/opt/homebrew/bin/vector", "/usr/local/bin/vector")
	}
	return paths
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
