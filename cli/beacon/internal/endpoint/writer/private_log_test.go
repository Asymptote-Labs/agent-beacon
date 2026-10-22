package writer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

func privateLogTestEvent(message string) schema.Event {
	return schema.NewEvent(schema.NewEventOptions{
		Action:  "agent.detected",
		Harness: schema.HarnessInfo{Name: "test"},
		Message: message,
	})
}

func assertPerm(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}

// A user-mode log holds that user's prompts, command output and diffs, and everything that writes
// it runs as that user. It used to be created 0666 in 0755 directories, readable and writable by
// every local account that could traverse the home directory.
func TestUserModeLogIsPrivate(t *testing.T) {
	testenv.RequirePOSIXFileModes(t)
	home := t.TempDir()
	testenv.SetHome(t, home)

	path := DefaultPath(true)
	if !strings.HasPrefix(path, home) {
		t.Fatalf("user log path %s is not under the test home %s", path, home)
	}
	if _, err := AppendEvent(privateLogTestEvent("first"), Options{UserMode: true}); err != nil {
		t.Fatal(err)
	}
	assertPerm(t, path, 0o600)
	assertPerm(t, path+".lock", 0o600)
	for _, dir := range []string{
		filepath.Join(home, ".beacon"),
		filepath.Join(home, ".beacon", "endpoint"),
		filepath.Join(home, ".beacon", "endpoint", "logs"),
	} {
		assertPerm(t, dir, 0o700)
	}
}

// A log an older release created world-writable is tightened the next time it is written. The
// writer owns it, so the chmod cannot fail for want of permission.
func TestExistingWorldWritableUserLogIsTightened(t *testing.T) {
	testenv.RequirePOSIXFileModes(t)
	home := t.TempDir()
	testenv.SetHome(t, home)
	path := DefaultPath(true)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{path, path + ".lock"} {
		if err := os.WriteFile(target, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(target, 0o666); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := AppendEvent(privateLogTestEvent("again"), Options{UserMode: true}); err != nil {
		t.Fatal(err)
	}
	assertPerm(t, path, 0o600)
	assertPerm(t, path+".lock", 0o600)

	// EnsureRuntimeFile (doctor --fix, Elastic setup) follows the same rule.
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := EnsureRuntimeFile(path); err != nil {
		t.Fatal(err)
	}
	assertPerm(t, path, 0o600)
}

// Rotation renames the live file, so a fresh archive inherits its private mode; archives an older
// release rotated world-writable are narrowed on the way.
func TestUserModeRotatedArchivesArePrivate(t *testing.T) {
	testenv.RequirePOSIXFileModes(t)
	home := t.TempDir()
	testenv.SetHome(t, home)
	path := DefaultPath(true)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".1", []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path+".1", 0o666); err != nil {
		t.Fatal(err)
	}
	opts := Options{UserMode: true, RotateSize: 256}
	for i := 0; i < 3; i++ {
		if _, err := AppendEvent(privateLogTestEvent(strings.Repeat("x", 100)+string(rune('a'+i))), opts); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(path + ".2"); err != nil {
		t.Fatalf("expected the log to rotate at least twice: %v", err)
	}
	for _, target := range []string{path, path + ".1", path + ".2"} {
		assertPerm(t, target, 0o600)
	}
}
