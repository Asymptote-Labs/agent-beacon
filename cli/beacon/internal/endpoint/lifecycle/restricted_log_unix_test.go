//go:build linux || darwin

package lifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func stubLogGroup(t *testing.T, gid string) *[]string {
	t.Helper()
	var calls []string
	previous := runGroupCommand
	runGroupCommand = func(name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		switch name {
		case "getent":
			return []byte("beacon-log:x:" + gid + ":\n"), nil
		case "dscl":
			return []byte("PrimaryGroupID: " + gid + "\n"), nil
		}
		return nil, nil
	}
	t.Cleanup(func() { runGroupCommand = previous })
	return &calls
}

func statOwner(t *testing.T, path string) (os.FileMode, uint32, uint32) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	return info.Mode().Perm(), stat.Uid, stat.Gid
}

// Turning restricted mode on narrows what is already in the log directory -- the log, its lock and
// archives were written 0666 under the shared contract -- and turning it off reopens the log and
// its lock to hooks at once.
func TestPrepareSystemLogEntersAndLeavesRestrictedMode(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("handing files to root:beacon-log needs root")
	}
	stubLogGroup(t, "4242")
	t.Cleanup(asymptoteobserve.UseRestrictedLogMarkerForTest(filepath.Join(t.TempDir(), "log-access.json")))
	dir := filepath.Join(t.TempDir(), "beacon-agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "runtime.jsonl")
	family := []string{logPath, logPath + ".lock", logPath + ".1"}
	for _, path := range family {
		if err := os.WriteFile(path, []byte("{}\n"), 0o666); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o666); err != nil {
			t.Fatal(err)
		}
	}
	elsewhere := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(elsewhere, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(dir, "planted")); err != nil {
		t.Fatal(err)
	}

	cfg := endpointconfig.Config{LogPath: logPath, LogAccess: asymptoteobserve.RestrictedLogAccess}
	if err := prepareSystemLog(cfg); err != nil {
		t.Fatal(err)
	}
	if mode, uid, gid := statOwner(t, dir); mode != 0o750 || uid != 0 || gid != 4242 {
		t.Errorf("log directory = %o %d:%d, want 750 0:4242", mode, uid, gid)
	}
	for _, path := range family {
		if mode, uid, gid := statOwner(t, path); mode != 0o640 || uid != 0 || gid != 4242 {
			t.Errorf("%s = %o %d:%d, want 640 0:4242", path, mode, uid, gid)
		}
	}
	if mode, _, gid := statOwner(t, elsewhere); mode != 0o600 || gid == 4242 {
		t.Errorf("a link in the log directory was followed: target is %o group %d", mode, gid)
	}
	marker, ok := asymptoteobserve.LoadRestrictedLog()
	if !ok || marker.GID != 4242 || marker.LogPath != logPath || marker.Socket != asymptoteobserve.DefaultRuntimeLogSocketPath {
		t.Fatalf("marker = %+v (%v)", marker, ok)
	}

	cfg.LogAccess = ""
	if err := prepareSystemLog(cfg); err != nil {
		t.Fatal(err)
	}
	if _, ok := asymptoteobserve.LoadRestrictedLog(); ok {
		t.Fatal("the marker survived leaving restricted mode")
	}
	for _, path := range family[:2] {
		if mode, _, _ := statOwner(t, path); mode != 0o666 {
			t.Errorf("%s = %o after leaving restricted mode, want 666 so hooks can append again", path, mode)
		}
	}
	if mode, _, _ := statOwner(t, dir); mode != 0o755 {
		t.Errorf("log directory = %o after leaving restricted mode, want 755", mode)
	}
}

func TestEnsureLogGroupCreatesAMissingGroup(t *testing.T) {
	created := false
	var calls []string
	previous := runGroupCommand
	runGroupCommand = func(name string, args ...string) ([]byte, error) {
		calls = append(calls, name)
		switch name {
		case "getent", "dscl":
			if !created {
				return nil, os.ErrNotExist
			}
			if name == "dscl" {
				return []byte("PrimaryGroupID: 977\n"), nil
			}
			return []byte("beacon-log:x:977:\n"), nil
		case "groupadd", "dseditgroup":
			created = true
		}
		return nil, nil
	}
	t.Cleanup(func() { runGroupCommand = previous })
	gid, err := ensureLogGroup()
	if err != nil || gid != 977 {
		t.Fatalf("ensureLogGroup = %d, %v; want 977 (calls %v)", gid, err, calls)
	}
}
