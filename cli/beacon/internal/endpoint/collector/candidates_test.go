package collector

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestDefaultBinaryCandidatesLooksForTheNameOnDisk covers the release archive's whole reason to exist.
//
// An extracted archive is beacon and beacon-otelcol sitting next to each other in a directory the
// user chose, which is almost never on PATH. So the sibling candidate is the one that has to work,
// and it has to ask for the file by the name the file actually has.
//
// On Windows those differ. BinaryName has no extension because it is what exec.LookPath is given, so
// PATHEXT can resolve it; the file on disk is beacon-otelcol.exe. Using the PATH spelling for a direct
// stat meant `beacon endpoint install` reported that no collector was installed, on a machine where
// the user had just extracted one next to the CLI.
func TestDefaultBinaryCandidatesLooksForTheNameOnDisk(t *testing.T) {
	candidates := defaultBinaryCandidates()
	if len(candidates) == 0 {
		t.Fatal("no candidate paths at all")
	}

	// The sibling of the running executable comes first: an extracted archive should win over a
	// machine-wide install, because it is the more specific answer to "which collector did this CLI
	// come with".
	first := candidates[0]
	if got := filepath.Base(first); got != binaryFileName() {
		t.Fatalf("first candidate is %q, want a file named %q", got, binaryFileName())
	}

	if runtime.GOOS == "windows" {
		if !strings.HasSuffix(first, ".exe") {
			t.Fatalf("first candidate %q has no .exe; os.Stat will not find the extracted collector", first)
		}
	} else if strings.HasSuffix(first, ".exe") {
		t.Fatalf("first candidate %q has a .exe on a POSIX platform", first)
	}
}

// A Homebrew CLI runs from its versioned keg. The collector path found here goes into the service
// unit, and the keg is deleted at the next `brew upgrade`, so the sibling has to be the
// <prefix>/bin link. PATH would usually find the link first, but not under sudo on Linuxbrew.
func TestDefaultBinaryCandidatesUseTheHomebrewLink(t *testing.T) {
	prefix := t.TempDir()
	kegBin := filepath.Join(prefix, "Cellar", "beacon", "1.3.20", "bin")
	link := filepath.Join(prefix, "bin", binaryFileName())
	for _, dir := range []string{kegBin, filepath.Dir(link)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(kegBin, binaryFileName()), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(link, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	old := currentExecutable
	currentExecutable = func() (string, error) { return filepath.Join(kegBin, "beacon"), nil }
	t.Cleanup(func() { currentExecutable = old })

	if got := defaultBinaryCandidates()[0]; got != link {
		t.Fatalf("first candidate = %q, want the Homebrew link %q", got, link)
	}
}

func TestPackagedBinaryPathsAreAbsoluteAndPlatformShaped(t *testing.T) {
	paths := packagedBinaryPaths()
	if len(paths) == 0 {
		t.Fatal("no packaged install location to look in")
	}
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			t.Fatalf("packaged path %q is not absolute", path)
		}
		if filepath.Base(path) != binaryFileName() {
			t.Fatalf("packaged path %q does not end in %q", path, binaryFileName())
		}
	}

	if runtime.GOOS != "windows" {
		// The POSIX location is a release contract: selfupdate classifies a package install by it.
		if paths[0] != PackagedBinaryPath {
			t.Fatalf("packaged path = %q, want %q", paths[0], PackagedBinaryPath)
		}
		return
	}

	// The POSIX constant is /opt/beacon/bin/..., which is not a path on Windows. Looking there would
	// always miss, and the miss would read as "no collector installed".
	for _, path := range paths {
		if strings.HasPrefix(path, "/opt/") {
			t.Fatalf("packaged path %q is the POSIX location", path)
		}
	}
}

// TestWindowsPackagedPathsHonorARelocatedProgramFiles guards against a hardcoded C:\Program Files.
//
// A machine with a relocated or localized program-files root must not be told its collector is
// missing, which is the same reason SystemBaseDir reads %ProgramData% instead of assuming it.
func TestWindowsPackagedPathsHonorARelocatedProgramFiles(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only path resolution")
	}
	t.Setenv("ProgramW6432", `D:\Apps`)
	t.Setenv("ProgramFiles", `D:\Apps`)

	paths := packagedBinaryPaths()
	want := filepath.Join(`D:\Apps`, "Beacon", "bin", binaryFileName())
	if len(paths) != 1 || paths[0] != want {
		t.Fatalf("packaged paths = %#v, want exactly [%q] (deduplicated when both roots agree)", paths, want)
	}
}
