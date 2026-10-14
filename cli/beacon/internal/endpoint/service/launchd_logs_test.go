package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// pointSystemLogDir moves the system-mode launchd log directory into a temp dir and pretends the
// test is not root, so nothing here touches /Library or tries to chown.
func pointSystemLogDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "Logs", "Beacon")
	oldDir, oldRoot := launchdSystemLogDir, launchdIsRoot
	launchdSystemLogDir = dir
	launchdIsRoot = func() bool { return false }
	t.Cleanup(func() { launchdSystemLogDir, launchdIsRoot = oldDir, oldRoot })
	return dir
}

// Every LaunchDaemon Beacon renders must log to the root-owned system directory and never to
// /tmp, where any local user could pre-create the file or plant a symlink for root to append to.
func TestSystemLaunchDaemonsNeverLogToTmp(t *testing.T) {
	plists := map[string]string{}
	paths := func(label string) (string, string) {
		out, errPath, err := launchdLogPaths(false, label)
		if err != nil {
			t.Fatal(err)
		}
		return out, errPath
	}
	out, errPath := paths(SystemLabel)
	plists[SystemLabel] = plist(SystemLabel, "/opt/beacon/bin/beacon-otelcol", "/etc/beacon/endpoint/otelcol.yaml", out, errPath)
	out, errPath = paths(ForwarderLabel)
	plists[ForwarderLabel] = forwarderPlist(ForwarderLabel, "/opt/beacon/bin/vector", "/etc/beacon/endpoint/asymptote/vector.toml", out, errPath)
	out, errPath = paths(UpdaterLabel)
	plists[UpdaterLabel] = updaterPlist(UpdaterLabel, "/opt/beacon/bin/beacon", out, errPath)
	out, errPath = paths(InventoryLabel)
	plists[InventoryLabel] = inventoryPlist(InventoryLabel, "/opt/beacon/bin/beacon", inventoryJobArgs(false, ""), InventoryInterval(), out, errPath, nil)

	for label, content := range plists {
		if strings.Contains(content, "/tmp/") {
			t.Errorf("%s LaunchDaemon logs to /tmp:\n%s", label, content)
		}
		for _, want := range []string{
			"<key>StandardOutPath</key>\n  <string>/Library/Logs/Beacon/" + label + ".out</string>",
			"<key>StandardErrorPath</key>\n  <string>/Library/Logs/Beacon/" + label + ".err</string>",
		} {
			if !strings.Contains(content, want) {
				t.Errorf("%s LaunchDaemon missing %q:\n%s", label, want, content)
			}
		}
	}
}

func TestUserLaunchAgentsLogUnderHome(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	want := filepath.Join(home, "Library", "Logs", "Beacon")
	for _, label := range []string{UserLabel, ForwarderLabel, InventoryLabel} {
		out, errPath, err := launchdLogPaths(true, label)
		if err != nil {
			t.Fatal(err)
		}
		if out != filepath.Join(want, label+".out") || errPath != filepath.Join(want, label+".err") {
			t.Errorf("%s user logs = %q, %q; want under %s", label, out, errPath, want)
		}
	}
}

func TestCollectorPlistEscapesEveryValue(t *testing.T) {
	got := plist(`l&"x"`, "/opt/<b>/otelcol", "/etc/a&b.yaml", "/x/<o>.out", `/x/"e".err`)
	for _, want := range []string{
		"<string>l&amp;&quot;x&quot;</string>",
		"<string>/opt/&lt;b&gt;/otelcol</string>",
		"<string>/etc/a&amp;b.yaml</string>",
		"<string>/x/&lt;o&gt;.out</string>",
		"<string>/x/&quot;e&quot;.err</string>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("collector plist missing escaped %q:\n%s", want, got)
		}
	}
	for _, raw := range []string{"a&b", "<b>", `"e"`} {
		if strings.Contains(got, raw) {
			t.Errorf("collector plist carries unescaped %q:\n%s", raw, got)
		}
	}
}

func TestEnsureLaunchdLogDirCreatesTheSystemDir(t *testing.T) {
	dir := pointSystemLogDir(t)
	if err := ensureLaunchdLogDir(false, UpdaterLabel); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() {
		t.Fatalf("log dir not created: %v", err)
	}
	if testenv.HasPOSIXFileModes() && fi.Mode().Perm() != 0o755 {
		t.Errorf("system log dir mode = %v, want 0755", fi.Mode().Perm())
	}
}

func TestEnsureLaunchdLogDirTightensUserDir(t *testing.T) {
	testenv.RequirePOSIXFileModes(t)
	home := t.TempDir()
	testenv.SetHome(t, home)
	dir := filepath.Join(home, "Library", "Logs", "Beacon")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := ensureLaunchdLogDir(true, UserLabel); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("user log dir mode = %v, want 0700", fi.Mode().Perm())
	}
}

// A symlink where the directory or a job's log file should be is removed, so launchd cannot be
// steered into appending to its target.
func TestEnsureLaunchdLogDirRemovesPlantedSymlinks(t *testing.T) {
	dir := pointSystemLogDir(t)
	target := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	elsewhere := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, dir); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := ensureLaunchdLogDir(false, UpdaterLabel); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(dir); err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		t.Fatalf("symlinked log dir was not replaced by a real directory: %v", err)
	}

	_, errPath, err := launchdLogPaths(false, UpdaterLabel)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, errPath); err != nil {
		t.Fatal(err)
	}
	if err := ensureLaunchdLogDir(false, UpdaterLabel); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(errPath); !os.IsNotExist(err) {
		t.Fatalf("planted log symlink survived: %v", err)
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "keep" {
		t.Fatalf("symlink target was touched: %q, %v", b, err)
	}
}

func TestEnsureLaunchdLogDirRefusesAFileInItsPlace(t *testing.T) {
	dir := pointSystemLogDir(t)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureLaunchdLogDir(false, UpdaterLabel); err == nil {
		t.Fatal("a regular file at the log directory path must be refused")
	}
}
