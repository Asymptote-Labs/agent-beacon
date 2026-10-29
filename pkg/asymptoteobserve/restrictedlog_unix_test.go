//go:build unix

package asymptoteobserve

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
)

func writeTestRestrictedMarker(t *testing.T, logPath, socket string) {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "log-access.json")
	t.Cleanup(UseRestrictedLogMarkerForTest(marker))
	if err := WriteRestrictedLogMarker(RestrictedLog{LogPath: logPath, Socket: socket, Group: RuntimeLogGroup, GID: os.Getgid() + 1}); err != nil {
		t.Fatal(err)
	}
}

const testEventLine = `{"vendor":"beacon","product":"endpoint-agent","schema_version":"1.0","event":{"action":"prompt.submitted"}}`

func TestRestrictedLogModesApplyOnlyInsideTheMarkedDirectory(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "logs", "runtime.jsonl")
	if _, ok := LoadRestrictedLog(); ok {
		t.Skip("this machine is in restricted log mode")
	}
	file, dirMode, _ := RuntimeLogModes(logPath)
	if file != SharedRuntimeLogFileMode || dirMode != SharedRuntimeLogDirMode {
		t.Fatalf("unmarked log modes = %o/%o, want shared", file, dirMode)
	}
	writeTestRestrictedMarker(t, logPath, filepath.Join(dir, "hooks.sock"))
	for _, path := range []string{logPath, logPath + ".lock", logPath + ".1", filepath.Join(dir, "logs", "system.jsonl")} {
		file, dirMode, private := RuntimeLogModes(path)
		if file != RestrictedRuntimeLogFileMode || dirMode != RestrictedRuntimeLogDirMode || private {
			t.Errorf("%s modes = %o/%o private=%v, want restricted", path, file, dirMode, private)
		}
	}
	if file, _, _ := RuntimeLogModes(filepath.Join(dir, "elsewhere", "runtime.jsonl")); file == RestrictedRuntimeLogFileMode {
		t.Error("a log outside the marked directory took the restricted mode")
	}
}

func TestRestrictedLogMarkerIsIgnoredWhenMalformed(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "log-access.json")
	t.Cleanup(UseRestrictedLogMarkerForTest(marker))
	for _, content := range []string{"", "{", `{"log_access":"shared","log_path":"/var/log/beacon-agent/runtime.jsonl","socket":"/s","gid":5}`, `{"log_access":"restricted","log_path":"relative","socket":"/s","gid":5}`, `{"log_access":"restricted","log_path":"/l","socket":"/s","gid":0}`} {
		if err := os.WriteFile(marker, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, ok := LoadRestrictedLog(); ok {
			t.Errorf("marker %q was accepted", content)
		}
	}
}

func TestRestrictedLogMarkerMustBeRootOwned(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root owns every file it writes")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "log-access.json")
	if err := os.WriteFile(path, []byte(`{"log_access":"restricted","log_path":"/l/runtime.jsonl","socket":"/s","gid":5}`), 0o644); err != nil {
		t.Fatal(err)
	}
	previous := RestrictedLogMarkerPath
	RestrictedLogMarkerPath = path
	t.Cleanup(func() { RestrictedLogMarkerPath = previous })
	if _, ok := LoadRestrictedLog(); ok {
		t.Fatal("a marker an ordinary user wrote switched the machine to restricted mode")
	}
}

func TestOpenRuntimeLogFileNarrowsARestrictedLog(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "runtime.jsonl")
	if err := os.WriteFile(logPath, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(logPath, 0o666); err != nil {
		t.Fatal(err)
	}
	writeTestRestrictedMarker(t, logPath, filepath.Join(dir, "hooks.sock"))
	f, err := OpenRuntimeLogFile(logPath, logPath, os.O_APPEND|os.O_WRONLY)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != RestrictedRuntimeLogFileMode {
		t.Fatalf("restricted log mode = %o, want %o", got, RestrictedRuntimeLogFileMode)
	}
	created := filepath.Join(dir, "runtime.jsonl.lock")
	f, err = OpenRuntimeLogFile(created, logPath, os.O_CREATE|os.O_RDWR)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if info, err := os.Stat(created); err != nil || info.Mode().Perm() != RestrictedRuntimeLogFileMode {
		t.Fatalf("new lock mode = %v (%v), want %o", info.Mode().Perm(), err, RestrictedRuntimeLogFileMode)
	}
	// Root also hands both files to the reader group; nobody else can.
	if os.Geteuid() == 0 {
		marker, _ := LoadRestrictedLog()
		for _, path := range []string{logPath, created} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if gid := int(info.Sys().(*syscall.Stat_t).Gid); gid != marker.GID {
				t.Errorf("%s group = %d, want the reader group %d", path, gid, marker.GID)
			}
		}
	}
}

func TestValidateRuntimeLogLine(t *testing.T) {
	if err := ValidateRuntimeLogLine([]byte(testEventLine + "\n")); err != nil {
		t.Fatalf("a valid event was refused: %v", err)
	}
	for name, line := range map[string]string{
		"empty":         "",
		"not json":      "hello",
		"array":         "[1]",
		"two values":    testEventLine + testEventLine,
		"no action":     `{"vendor":"beacon","product":"endpoint-agent","schema_version":"1.0","event":{}}`,
		"no vendor":     `{"product":"endpoint-agent","schema_version":"1.0","event":{"action":"x"}}`,
		"inner newline": strings.Replace(testEventLine, ",", ",\n", 1),
		"oversized":     `{"vendor":"beacon","product":"p","schema_version":"1","event":{"action":"x"},"pad":"` + strings.Repeat("a", maxRuntimeLogLineBytes) + `"}`,
	} {
		if err := ValidateRuntimeLogLine([]byte(line)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func shortSocketPath(t *testing.T) string {
	t.Helper()
	// Unix socket paths are limited to about 104 bytes, which a macOS t.TempDir() can exceed.
	dir, err := os.MkdirTemp("", "bsock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "hooks.sock")
}

func TestRuntimeLogSocketRoundTrip(t *testing.T) {
	path := shortSocketPath(t)
	var mu sync.Mutex
	var got [][]byte
	server, err := ListenRuntimeLogSocket(path, func(line []byte) error {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, append([]byte{}, line...))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o666 {
		t.Fatalf("socket mode = %v (%v), want 0666 so every user's hooks can connect", info.Mode().Perm(), err)
	}
	if err := SendRuntimeLogLine(path, []byte(testEventLine), 0); err != nil {
		t.Fatal(err)
	}
	if err := SendRuntimeLogLine(path, []byte("not an event\n"), 0); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("invalid line error = %v, want a refusal", err)
	}
	mu.Lock()
	if len(got) != 1 || !bytes.Equal(got[0], []byte(testEventLine+"\n")) {
		t.Fatalf("appended %q, want the one valid line", got)
	}
	mu.Unlock()
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("socket left behind after Close: %v", err)
	}
	if err := SendRuntimeLogLine(path, []byte(testEventLine), 0); err == nil {
		t.Fatal("send succeeded with no collector listening")
	}
}

func TestRuntimeLogSocketReplacesOnlyAStaleSocket(t *testing.T) {
	path := shortSocketPath(t)
	if err := os.WriteFile(path, []byte("not a socket"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ListenRuntimeLogSocket(path, func([]byte) error { return nil }); err == nil {
		t.Fatal("listen replaced a regular file")
	}
	_ = os.Remove(path)
	first, err := ListenRuntimeLogSocket(path, func([]byte) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a crash: the listener goes away without unlinking.
	first.listener.(interface{ SetUnlinkOnClose(bool) }).SetUnlinkOnClose(false)
	_ = first.Close()
	second, err := ListenRuntimeLogSocket(path, func([]byte) error { return nil })
	if err != nil {
		t.Fatalf("a stale socket blocked the restart: %v", err)
	}
	_ = second.Close()
}

func TestRuntimeLogSocketReportsAppendFailure(t *testing.T) {
	path := shortSocketPath(t)
	server, err := ListenRuntimeLogSocket(path, func([]byte) error { return os.ErrPermission })
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if err := SendRuntimeLogLine(path, []byte(testEventLine), 0); err == nil {
		t.Fatal("a failed append was reported as written")
	}
}
