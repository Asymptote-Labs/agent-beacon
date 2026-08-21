package diagnostics

import (
	"os"
	"path/filepath"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

func TestCheckFileRequiredOptionalAndDirectory(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing")

	if check := checkFile("required", missing, true); check.Status != "fail" || check.Severity != "medium" {
		t.Fatalf("required missing check = %#v", check)
	}
	if check := checkFile("optional", missing, false); check.Status != "warn" || check.Severity != "low" {
		t.Fatalf("optional missing check = %#v", check)
	}
	if check := checkFile("dir", dir, true); check.Status != "fail" || check.Severity != "medium" {
		t.Fatalf("directory check = %#v", check)
	}

	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("ok"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if check := checkFile("file", file, true); check.Status != "ok" || check.Severity != "info" {
		t.Fatalf("file check = %#v", check)
	}
}

func TestCheckLogPermissions(t *testing.T) {
	// Mode bits are the POSIX form of this question; Windows answers it through the ACL and the
	// per-user write test instead, both of which need a real Windows host to exercise.
	testenv.RequirePOSIXFileModes(t)
	dir := t.TempDir()
	logPath := filepath.Join(dir, "runtime.jsonl")

	if check := checkLogPermissions(logPath, false); check.Status != "warn" || check.Severity != "low" {
		t.Fatalf("missing log permissions check = %#v", check)
	}

	// 0644 is owner-write only. In system mode the owner is root and the hooks are not, so this is
	// a log no hook can append to -- their events are lost silently. This case asserted "ok" until
	// the check learned to ask whether a *non-root* process can write, rather than whether anyone
	// can.
	if err := os.WriteFile(logPath, []byte("{}\n"), 0644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	if check := checkLogPermissions(logPath, false); check.Status != "fail" || check.Evidence != "not_writable_by_hooks" {
		t.Fatalf("0644 log permissions check = %#v", check)
	}

	// 0666 is what install actually creates, and the only mode that lets the root collector and
	// the user's hooks share one file.
	if err := os.Chmod(logPath, 0666); err != nil {
		t.Fatalf("chmod writable log: %v", err)
	}
	if check := checkLogPermissions(logPath, false); check.Status != "ok" {
		t.Fatalf("0666 log permissions check = %#v", check)
	}

	// 0200 is owner-write, no read for anyone. Previously reported as a low-severity readability
	// warning; the more serious half is that hooks cannot write it either.
	if err := os.Chmod(logPath, 0200); err != nil {
		t.Fatalf("chmod unreadable log: %v", err)
	}
	if check := checkLogPermissions(logPath, false); check.Status != "fail" || check.Evidence != "not_writable_by_hooks" {
		t.Fatalf("0200 log permissions check = %#v", check)
	}

	if err := os.Chmod(logPath, 0444); err != nil {
		t.Fatalf("chmod non-writable log: %v", err)
	}
	if check := checkLogPermissions(logPath, false); check.Status != "fail" || check.Severity != "high" || check.Evidence != "not_writable" {
		t.Fatalf("0444 log permissions check = %#v", check)
	}
}

func TestRunAndHasFailures(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := endpointconfig.Default(true, filepath.Join(home, ".beacon", "endpoint", "logs", "runtime.jsonl"))
	if _, err := endpointconfig.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.Collector.ConfigPath), 0755); err != nil {
		t.Fatalf("mkdir collector dir: %v", err)
	}
	if err := os.WriteFile(cfg.Collector.ConfigPath, []byte("receivers: {}\n"), 0644); err != nil {
		t.Fatalf("write collector config: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.LogPath), 0755); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}
	if err := os.WriteFile(cfg.LogPath, []byte("{}\n"), 0644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	// Run names the service-definition check after whichever manager the host actually uses, so
	// the fixture has to satisfy that manager rather than a hardcoded one. Keying off GOOS was
	// enough while launchd was the only backend; it made this test pass on any host without
	// systemd and fail on a GitHub runner, where systemd genuinely is PID 1 and a unit check runs
	// that nothing had created a unit for.
	if path := serviceUnitPathForTest(true); path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatalf("mkdir service dir: %v", err)
		}
		if err := os.WriteFile(path, []byte("# fixture\n"), 0644); err != nil {
			t.Fatalf("write service definition: %v", err)
		}
	}

	checks := Run(cfg)
	if HasFailures(checks) {
		t.Fatalf("expected no failures, got %#v", checks)
	}
	if !HasFailures([]Check{{Name: "x", Status: "fail"}}) {
		t.Fatal("expected HasFailures to report failed check")
	}
}

func TestLaunchPlistPathMatchesServiceManager(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	userPath := servicePlistPathForTest(true)
	wantUserPath := filepath.Join(home, "Library", "LaunchAgents", service.UserLabel+".plist")
	if userPath != wantUserPath {
		t.Fatalf("user launchPlistPath = %q, want %q", userPath, wantUserPath)
	}

	systemPath := servicePlistPathForTest(false)
	wantSystemPath := filepath.Join("/Library/LaunchDaemons", service.SystemLabel+".plist")
	if systemPath != wantSystemPath {
		t.Fatalf("system launchPlistPath = %q, want %q", systemPath, wantSystemPath)
	}
}

// servicePlistPathForTest mirrors what the service package now owns, so these tests keep
// exercising the launchd check without diagnostics duplicating the path logic.
// serviceUnitPathForTest is the definition file the host's own service manager expects, or empty
// when it has none -- the supervised backend has no unit file, so there is nothing to create.
func serviceUnitPathForTest(userMode bool) string {
	mgr := service.Manager{UserMode: userMode}
	switch mgr.ResolvedKind() {
	case service.KindLaunchd, service.KindSystemd:
		path, err := mgr.UnitPath()
		if err != nil {
			return ""
		}
		return path
	}
	return ""
}

func servicePlistPathForTest(userMode bool) string {
	path, err := (service.Manager{UserMode: userMode, Kind: service.KindLaunchd}).UnitPath()
	if err != nil {
		return ""
	}
	return path
}
