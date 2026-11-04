//go:build !windows

package beaconjsonexporter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setExporterTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func assertExporterPerm(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}

// A user-mode collector runs as its user and writes ~/.beacon/endpoint/logs/runtime.jsonl, which
// holds that user's prompts and command output. It creates the log, lock and directories private.
func TestUserModeExporterLogIsPrivate(t *testing.T) {
	home := setExporterTestHome(t)
	path := filepath.Join(home, ".beacon", "endpoint", "logs", "runtime.jsonl")
	w := jsonlWriter{path: path, maxEventBytes: defaultMaxEventBytes, rotateBytes: defaultRotateBytes, rotateArchives: defaultRotateArchives}
	if err := w.append(beaconEvent{Message: "test"}); err != nil {
		t.Fatal(err)
	}
	assertExporterPerm(t, path, 0o600)
	assertExporterPerm(t, path+".lock", 0o600)
	for _, dir := range []string{
		filepath.Join(home, ".beacon"),
		filepath.Join(home, ".beacon", "endpoint"),
		filepath.Join(home, ".beacon", "endpoint", "logs"),
	} {
		assertExporterPerm(t, dir, 0o700)
	}
}

// An existing world-writable user log is tightened, and rotation keeps archives private.
func TestUserModeExporterLogIsTightenedAndRotatesPrivately(t *testing.T) {
	home := setExporterTestHome(t)
	path := filepath.Join(home, ".beacon", "endpoint", "logs", "runtime.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{path, path + ".lock", path + ".1"} {
		if err := os.WriteFile(target, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(target, 0o666); err != nil {
			t.Fatal(err)
		}
	}
	line := []byte(`{"message":"` + strings.Repeat("x", 64) + `"}` + "\n")
	if err := appendJSONL(path, line, 32, defaultRotateArchives); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".2"); err != nil {
		t.Fatalf("expected the log to rotate: %v", err)
	}
	for _, target := range []string{path, path + ".lock", path + ".1", path + ".2"} {
		assertExporterPerm(t, target, 0o600)
	}
}
