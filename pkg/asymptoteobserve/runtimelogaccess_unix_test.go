//go:build unix

package asymptoteobserve

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// trustMarkersFrom makes markers owned by the test's own account count, as root's do in production.
func trustMarkersFrom(t *testing.T, uid int) {
	t.Helper()
	old := logAccessMarkerOwner
	logAccessMarkerOwner = uid
	t.Cleanup(func() { logAccessMarkerOwner = old })
}

// systemLog returns a log path outside the test's home, so it is a system log, not a private one.
func systemLog(t *testing.T) string {
	t.Helper()
	setTestHome(t, t.TempDir())
	return filepath.Join(t.TempDir(), "beacon-agent", "runtime.jsonl")
}

func mkLogDir(t *testing.T, logPath string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
}

func modesOf(path string) (os.FileMode, os.FileMode) {
	file, dir, _ := RuntimeLogModes(path)
	return file, dir
}

func TestRestrictedMarkerSwitchesTheSystemLogModes(t *testing.T) {
	trustMarkersFrom(t, os.Geteuid())
	log := systemLog(t)
	mkLogDir(t, log)

	if file, dir := modesOf(log); file != SharedRuntimeLogFileMode || dir != SharedRuntimeLogDirMode {
		t.Fatalf("without a marker: %o, %o; want the shared modes", file, dir)
	}
	if RuntimeLogAccessRestricted(log) {
		t.Fatal("restricted without a marker")
	}

	if err := SetRuntimeLogAccess(log, LogAccessRestricted); err != nil {
		t.Fatal(err)
	}
	if !RuntimeLogAccessRestricted(log) {
		t.Fatal("not restricted after SetRuntimeLogAccess")
	}
	file, dir, private := RuntimeLogModes(log)
	if private || file != RestrictedRuntimeLogFileMode || dir != RestrictedRuntimeLogDirMode {
		t.Fatalf("restricted modes = %o, %v, private=%v", file, dir, private)
	}
	info, err := os.Stat(LogAccessMarkerPath(log))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("marker mode = %o; it must be readable by the readers group and writable by root only", info.Mode().Perm())
	}
	// Writing it again replaces it in place.
	if err := SetRuntimeLogAccess(log, LogAccessRestricted); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Dir(log))
	if len(entries) != 1 {
		t.Fatalf("directory holds %d entries; a temporary marker was left behind", len(entries))
	}

	if err := SetRuntimeLogAccess(log, LogAccessShared); err != nil {
		t.Fatal(err)
	}
	if RuntimeLogAccessRestricted(log) {
		t.Fatal("still restricted after switching back to shared")
	}
	if err := SetRuntimeLogAccess(log, LogAccessShared); err != nil {
		t.Fatalf("removing an absent marker: %v", err)
	}
}

func TestUntrustedMarkersAreIgnored(t *testing.T) {
	trustMarkersFrom(t, os.Geteuid())
	cases := map[string]func(t *testing.T, marker string){
		"group writable": func(t *testing.T, marker string) {
			writeMarker(t, marker, "restricted\n")
			if err := os.Chmod(marker, 0o664); err != nil {
				t.Fatal(err)
			}
		},
		"world writable": func(t *testing.T, marker string) {
			writeMarker(t, marker, "restricted\n")
			if err := os.Chmod(marker, 0o646); err != nil {
				t.Fatal(err)
			}
		},
		"owned by another account": func(t *testing.T, marker string) {
			writeMarker(t, marker, "restricted\n")
			logAccessMarkerOwner = os.Geteuid() + 1
		},
		"a symlink": func(t *testing.T, marker string) {
			target := filepath.Join(t.TempDir(), "elsewhere")
			writeMarker(t, target, "restricted\n")
			if err := os.Symlink(target, marker); err != nil {
				t.Fatal(err)
			}
		},
		"a hard link": func(t *testing.T, marker string) {
			writeMarker(t, marker, "restricted\n")
			if err := os.Link(marker, marker+"-twin"); err != nil {
				t.Fatal(err)
			}
		},
		"a directory": func(t *testing.T, marker string) {
			if err := os.Mkdir(marker, 0o755); err != nil {
				t.Fatal(err)
			}
		},
		"a FIFO": func(t *testing.T, marker string) {
			if err := syscall.Mkfifo(marker, 0o644); err != nil {
				t.Skipf("mkfifo: %v", err)
			}
		},
		"another mode": func(t *testing.T, marker string) {
			writeMarker(t, marker, "shared\n")
		},
		"garbage": func(t *testing.T, marker string) {
			writeMarker(t, marker, "restricted please\n")
		},
		"oversized": func(t *testing.T, marker string) {
			writeMarker(t, marker, "restricted"+strings.Repeat(" ", maxLogAccessMarkerBytes))
		},
	}
	for name, plant := range cases {
		t.Run(name, func(t *testing.T) {
			trustMarkersFrom(t, os.Geteuid())
			log := systemLog(t)
			mkLogDir(t, log)
			plant(t, LogAccessMarkerPath(log))
			if RuntimeLogAccessRestricted(log) {
				t.Fatal("an untrusted marker switched the log to restricted")
			}
			if file, _ := modesOf(log); file != SharedRuntimeLogFileMode {
				t.Fatalf("file mode = %o, want shared", file)
			}
		})
	}
}

func TestMarkerToleratesSurroundingWhitespace(t *testing.T) {
	trustMarkersFrom(t, os.Geteuid())
	log := systemLog(t)
	mkLogDir(t, log)
	writeMarker(t, LogAccessMarkerPath(log), "  restricted \r\n")
	if !RuntimeLogAccessRestricted(log) {
		t.Fatal("a marker with surrounding whitespace was not honored")
	}
}

func writeMarker(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAPrivateLogIsNeverRestricted(t *testing.T) {
	trustMarkersFrom(t, os.Geteuid())
	home := t.TempDir()
	setTestHome(t, home)
	log := filepath.Join(home, ".beacon", "endpoint", "logs", "runtime.jsonl")
	mkLogDir(t, log)
	if err := SetRuntimeLogAccess(log, LogAccessRestricted); err == nil {
		t.Fatal("SetRuntimeLogAccess accepted a per-user log")
	}
	writeMarker(t, LogAccessMarkerPath(log), "restricted\n")
	if RuntimeLogAccessRestricted(log) {
		t.Fatal("a marker beside a per-user log was honored")
	}
	if file, dir, private := RuntimeLogModes(log); !private || file != PrivateRuntimeLogFileMode || dir != PrivateRuntimeLogDirMode {
		t.Fatalf("modes = %o, %o, %v; want private", file, dir, private)
	}
}

func TestSetRuntimeLogAccessRejectsAnUnknownMode(t *testing.T) {
	log := systemLog(t)
	mkLogDir(t, log)
	if err := SetRuntimeLogAccess(log, "open"); err == nil {
		t.Fatal("accepted an unknown mode")
	}
	if _, err := os.Lstat(LogAccessMarkerPath(log)); !os.IsNotExist(err) {
		t.Fatal("an unknown mode left a marker")
	}
}

func TestParseLogAccess(t *testing.T) {
	for in, want := range map[string]string{
		"": LogAccessShared, "shared": LogAccessShared, " Shared ": LogAccessShared,
		"restricted": LogAccessRestricted, "RESTRICTED": LogAccessRestricted,
	} {
		got, err := ParseLogAccess(in)
		if err != nil || got != want {
			t.Errorf("ParseLogAccess(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"open", "private", "0640"} {
		if _, err := ParseLogAccess(bad); err == nil {
			t.Errorf("ParseLogAccess(%q) accepted", bad)
		}
	}
}

// The root writers narrow a log that was shared to the restricted mode the next time they open it,
// lock file included, and create new files restricted whatever the umask.
func TestOpenRuntimeLogFileAppliesTheRestrictedMode(t *testing.T) {
	trustMarkersFrom(t, os.Geteuid())
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	log := systemLog(t)
	mkLogDir(t, log)

	for _, path := range []string{log, log + ".lock"} {
		if err := os.WriteFile(path, []byte("{}\n"), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	if err := SetRuntimeLogAccess(log, LogAccessRestricted); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{log, log + ".lock"} {
		f, err := OpenRuntimeLogFile(path, log, os.O_APPEND|os.O_CREATE|os.O_WRONLY)
		if err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
		want := RestrictedRuntimeLogFileMode
		if path == log+".lock" {
			want = RestrictedRuntimeLogLockMode
		}
		if info, _ := os.Stat(path); info.Mode().Perm() != want {
			t.Fatalf("%s mode = %o after open; want it narrowed to %o", filepath.Base(path), info.Mode().Perm(), want)
		}
	}

	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	f, err := OpenRuntimeLogFile(log, log, os.O_APPEND|os.O_CREATE|os.O_WRONLY)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if info, _ := os.Stat(log); info.Mode().Perm() != RestrictedRuntimeLogFileMode {
		t.Fatalf("new log mode = %o with umask 0; want %o", info.Mode().Perm(), RestrictedRuntimeLogFileMode)
	}
}

// Switching back to shared widens the log again at the next root write, so hooks can append.
func TestOpenRuntimeLogFileWidensWhenSwitchedBackToShared(t *testing.T) {
	trustMarkersFrom(t, os.Geteuid())
	log := systemLog(t)
	mkLogDir(t, log)
	if err := os.WriteFile(log, nil, 0o640); err != nil {
		t.Fatal(err)
	}
	f, err := OpenRuntimeLogFile(log, log, os.O_APPEND|os.O_WRONLY)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if info, _ := os.Stat(log); info.Mode().Perm() != SharedRuntimeLogFileMode {
		t.Fatalf("mode = %o; a shared log must be widened back to %o", info.Mode().Perm(), SharedRuntimeLogFileMode)
	}
}

func TestTightenRestrictedRuntimeLogArchives(t *testing.T) {
	trustMarkersFrom(t, os.Geteuid())
	log := systemLog(t)
	mkLogDir(t, log)
	writeArchives := func() {
		for i := 1; i <= 3; i++ {
			path := fmt.Sprintf("%s.%d", log, i)
			if err := os.WriteFile(path, nil, 0o666); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0o666); err != nil {
				t.Fatal(err)
			}
		}
	}
	writeArchives()
	TightenRuntimeLogArchives(log, 3)
	for i := 1; i <= 3; i++ {
		if info, _ := os.Stat(fmt.Sprintf("%s.%d", log, i)); info.Mode().Perm() != 0o666 {
			t.Fatalf("a shared log's archive %d was changed to %o", i, info.Mode().Perm())
		}
	}

	if err := SetRuntimeLogAccess(log, LogAccessRestricted); err != nil {
		t.Fatal(err)
	}
	TightenRuntimeLogArchives(log, 3)
	for i := 1; i <= 3; i++ {
		if info, _ := os.Stat(fmt.Sprintf("%s.%d", log, i)); info.Mode().Perm() != RestrictedRuntimeLogFileMode {
			t.Fatalf("restricted archive %d mode = %o", i, info.Mode().Perm())
		}
	}
}

// A process that cannot read the restricted log's directory -- an ordinary user, once install has
// narrowed it -- cannot read the marker either and sees the shared modes. That is safe: it cannot
// write there at all, and hooks reach the collector through the ingest socket instead.
func TestAnUnreadableDirectoryReadsAsNoMarker(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any directory")
	}
	trustMarkersFrom(t, os.Geteuid())
	log := systemLog(t)
	mkLogDir(t, log)
	if err := SetRuntimeLogAccess(log, LogAccessRestricted); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(log)
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o755)
	if RuntimeLogAccessRestricted(log) {
		t.Fatal("read a marker through a directory with no permissions")
	}
}

func TestARestrictedLogsLockIsRootsAlone(t *testing.T) {
	trustMarkersFrom(t, os.Geteuid())
	log := systemLog(t)
	mkLogDir(t, log)
	if err := SetRuntimeLogAccess(log, LogAccessRestricted); err != nil {
		t.Fatal(err)
	}
	f, err := OpenRuntimeLogFile(log+".lock", log, os.O_CREATE|os.O_RDWR)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if info, _ := os.Stat(log + ".lock"); info.Mode().Perm() != RestrictedRuntimeLogLockMode {
		t.Fatalf("lock mode = %o; a reader that can open the lock can hold it", info.Mode().Perm())
	}
	// An archive is not a lock, whatever its name contains.
	if got := fileModeFor(log+".1", log); got != RestrictedRuntimeLogFileMode {
		t.Fatalf("archive mode = %o", got)
	}
}
