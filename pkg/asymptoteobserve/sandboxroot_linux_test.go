//go:build linux

package asymptoteobserve

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// standInSandboxRoot makes root stand in for /, a tmpfs (bubblewrap's) or not.
func standInSandboxRoot(t *testing.T, root string, tmpfs bool) {
	t.Helper()
	previousRoot, previousTmpfs := sandboxRootDir, filesystemIsTmpfs
	sandboxRootDir = root
	filesystemIsTmpfs = func(string) bool { return tmpfs }
	t.Cleanup(func() { sandboxRootDir, filesystemIsTmpfs = previousRoot, previousTmpfs })
}

// A launcher that bind-mounts a project under the home makes the home exist, as an empty mount
// point on the sandbox's own root filesystem. A log created there would take every event and
// vanish with the sandbox; a directory the sandbox mounts is another filesystem and stays real.
func TestNoLogIsCreatedOnASandboxRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root's writes never count as a sandbox's")
	}
	root := t.TempDir() // owned by this account, as bubblewrap's tmpfs root is
	standInSandboxRoot(t, root, true)

	home := filepath.Join(root, "home", "u")
	if err := os.MkdirAll(filepath.Join(home, "src", "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	setTestHome(t, home)
	userLog := filepath.Join(home, ".beacon", "endpoint", "logs", "runtime.jsonl")
	if !OnSandboxRoot(userLog) || RuntimeLogDirCreatable(userLog) {
		t.Fatalf("a log under a home that exists only on the sandbox's root was creatable (OnSandboxRoot %v)", OnSandboxRoot(userLog))
	}

	var rootStat, devStat syscall.Stat_t
	if err := syscall.Stat(root, &rootStat); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Stat("/dev", &devStat); err == nil && devStat.Dev != rootStat.Dev {
		if OnSandboxRoot(filepath.Join("/dev", "beacon-test-missing", "spool")) {
			t.Fatal("a path on another filesystem (a bind mount) counted as the sandbox's root")
		}
	}

	standInSandboxRoot(t, "/", true)
	if OnSandboxRoot(userLog) {
		t.Fatal("a root directory this account does not own counted as a sandbox it assembled")
	}
}

// A sandbox whose / is a persistent directory the account owns (a rootfs bound at /, an
// Apptainer sandbox directory), with the home bound from the same disk, keeps what is written
// there. Its writes are real and must go ahead.
func TestAPersistentRootIsNotASandboxRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root's writes never count as a sandbox's")
	}
	root := t.TempDir()
	standInSandboxRoot(t, root, false)
	home := filepath.Join(root, "home", "u")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	setTestHome(t, home)
	userLog := filepath.Join(home, ".beacon", "endpoint", "logs", "runtime.jsonl")
	if OnSandboxRoot(userLog) || !RuntimeLogDirCreatable(userLog) {
		t.Fatal("a log on a persistent, account-owned root was refused as if the root were a sandbox's tmpfs")
	}
}
