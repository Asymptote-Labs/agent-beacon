package diagnostics

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
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
	dir := t.TempDir()
	logPath := filepath.Join(dir, "runtime.jsonl")

	if check := checkLogPermissions(logPath); check.Status != "warn" || check.Severity != "low" {
		t.Fatalf("missing log permissions check = %#v", check)
	}

	if err := os.WriteFile(logPath, []byte("{}\n"), 0644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	if check := checkLogPermissions(logPath); check.Status != "ok" {
		t.Fatalf("0644 log permissions check = %#v", check)
	}

	if err := os.Chmod(logPath, 0666); err != nil {
		t.Fatalf("chmod writable log: %v", err)
	}
	if check := checkLogPermissions(logPath); check.Status != "ok" {
		t.Fatalf("0666 log permissions check = %#v", check)
	}

	if err := os.Chmod(logPath, 0200); err != nil {
		t.Fatalf("chmod unreadable log: %v", err)
	}
	if check := checkLogPermissions(logPath); check.Status != "warn" || check.Severity != "low" {
		t.Fatalf("0200 log permissions check = %#v", check)
	}

	if err := os.Chmod(logPath, 0444); err != nil {
		t.Fatalf("chmod non-writable log: %v", err)
	}
	if check := checkLogPermissions(logPath); check.Status != "fail" || check.Severity != "high" || check.Evidence != "not_writable" {
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
	if runtime.GOOS == "darwin" {
		plistPath := launchPlistPath(true)
		if err := os.MkdirAll(filepath.Dir(plistPath), 0755); err != nil {
			t.Fatalf("mkdir plist dir: %v", err)
		}
		if err := os.WriteFile(plistPath, []byte("<plist/>"), 0644); err != nil {
			t.Fatalf("write plist: %v", err)
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

	userPath := launchPlistPath(true)
	wantUserPath := filepath.Join(home, "Library", "LaunchAgents", service.UserLabel+".plist")
	if userPath != wantUserPath {
		t.Fatalf("user launchPlistPath = %q, want %q", userPath, wantUserPath)
	}

	systemPath := launchPlistPath(false)
	wantSystemPath := filepath.Join("/Library/LaunchDaemons", service.SystemLabel+".plist")
	if systemPath != wantSystemPath {
		t.Fatalf("system launchPlistPath = %q, want %q", systemPath, wantSystemPath)
	}
}
