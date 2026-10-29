package asymptoteobserve

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func setTestHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

// A user-mode log lives in its user's home and is written only by processes running as that user,
// so it is private. Every other path keeps the shared mode the system log needs.
func TestRuntimeLogModesFollowTheHomeDirectory(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	userLog := filepath.Join(home, ".beacon", "endpoint", "logs", "runtime.jsonl")

	file, dir, private := RuntimeLogModes(userLog)
	if !private || file != PrivateRuntimeLogFileMode || dir != PrivateRuntimeLogDirMode {
		t.Fatalf("user log modes = %o, %o, private=%v; want %o, %o, true",
			file, dir, private, PrivateRuntimeLogFileMode, PrivateRuntimeLogDirMode)
	}

	elsewhere := filepath.Join(t.TempDir(), "runtime.jsonl")
	file, dir, private = RuntimeLogModes(elsewhere)
	if private || file != SharedRuntimeLogFileMode || dir != SharedRuntimeLogDirMode {
		t.Fatalf("log outside home modes = %o, %o, private=%v; want the shared modes", file, dir, private)
	}

	if RuntimeLogIsPrivate("") {
		t.Fatal("an empty path must not be private")
	}
	if RuntimeLogIsPrivate(home) {
		t.Fatal("the home directory itself is not a log inside it")
	}
	// A sibling whose name merely starts with the home path is not inside it.
	if RuntimeLogIsPrivate(home + "-other/runtime.jsonl") {
		t.Fatal("a sibling directory sharing the home prefix was treated as inside home")
	}
}

// The system log is shared even for a process whose HOME sits above it -- a service account with
// HOME=/ or HOME=/var must not narrow the log every user's hooks append to.
func TestSystemLogIsNeverPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Unix system log directory does not exist on Windows")
	}
	for _, home := range []string{"/", "/var", "/var/log"} {
		setTestHome(t, home)
		if RuntimeLogIsPrivate("/var/log/beacon-agent/runtime.jsonl") {
			t.Fatalf("with HOME=%s the system log was treated as private", home)
		}
	}
}

func TestSettleRuntimeLogArchives(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits do not exist on Windows")
	}
	home := t.TempDir()
	setTestHome(t, home)
	path := filepath.Join(home, "runtime.jsonl")
	writeWorldWritable := func(name string) {
		t.Helper()
		if err := os.WriteFile(name, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(name, 0o666); err != nil {
			t.Fatal(err)
		}
	}
	writeWorldWritable(path + ".1")
	writeWorldWritable(path + ".2")
	SettleRuntimeLogArchives(path, 5)
	for _, archive := range []string{path + ".1", path + ".2"} {
		info, err := os.Stat(archive)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != PrivateRuntimeLogFileMode {
			t.Fatalf("%s mode = %o, want %o", archive, got, PrivateRuntimeLogFileMode)
		}
	}

	// A shared log's archives are left alone.
	shared := filepath.Join(t.TempDir(), "runtime.jsonl")
	writeWorldWritable(shared + ".1")
	SettleRuntimeLogArchives(shared, 5)
	info, err := os.Stat(shared + ".1")
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != SharedRuntimeLogFileMode {
		t.Fatalf("a shared archive was changed to %o", got)
	}
}
